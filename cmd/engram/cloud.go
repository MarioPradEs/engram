package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudserver"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// resolveJWTTTL reads ENGRAM_JWT_TTL and returns a clamped duration.
//
//   - Empty or unset → auth.DefaultJWTTTL (90d), no warning.
//   - Unparseable    → auth.DefaultJWTTTL + warning on stderr.
//   - < auth.MinJWTTTL (24h) → auth.MinJWTTTL + warning on stderr.
//   - Otherwise      → parsed value, no warning.
//
// This function never returns a value below auth.MinJWTTTL and never panics.
// Both the server (handleAuth / autoLoginFromHeader) and the CLI login path
// call this so there is exactly one parse-and-clamp site.
func resolveJWTTTL() time.Duration {
	return resolveJWTTTLWithWriter(os.Stderr, strings.TrimSpace(os.Getenv("ENGRAM_JWT_TTL")))
}

// resolveJWTTTLWithWriter is the testable core: accepts an explicit writer for
// warnings and the raw env string so tests do not need to mutate os.Environ.
func resolveJWTTTLWithWriter(w io.Writer, raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return auth.DefaultJWTTTL
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fmt.Fprintf(w, "[engram] WARN: ENGRAM_JWT_TTL=%q is not a valid duration; using default %v\n", raw, auth.DefaultJWTTTL)
		return auth.DefaultJWTTTL
	}
	if d < auth.MinJWTTTL {
		fmt.Fprintf(w, "[engram] WARN: ENGRAM_JWT_TTL=%q is below minimum %v; clamping to %v\n", raw, auth.MinJWTTTL, auth.MinJWTTTL)
		return auth.MinJWTTTL
	}
	return d
}

type cloudManifestReader interface {
	ReadManifest(ctx context.Context, project string) (*engramsync.Manifest, error)
}

type cloudDashboardStatusProvider struct {
	store    cloudManifestReader
	projects []string
}

func (p cloudDashboardStatusProvider) Status() dashboard.SyncStatus {
	if len(p.projects) == 0 {
		return dashboard.SyncStatus{
			Phase:         "degraded",
			ReasonCode:    constants.ReasonBlockedUnenrolled,
			ReasonMessage: "cloud project allowlist is empty",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	totalChunks := 0
	for _, project := range p.projects {
		manifest, err := p.store.ReadManifest(ctx, project)
		if err != nil {
			log.Printf("[engram] cloud dashboard status manifest read failed for project %q: %v", project, err)
			return dashboard.SyncStatus{
				Phase:         "degraded",
				ReasonCode:    constants.ReasonTransportFailed,
				ReasonMessage: "cloud sync status is temporarily unavailable",
			}
		}
		totalChunks += len(manifest.Chunks)
	}

	return dashboard.SyncStatus{
		Phase:         "healthy",
		ReasonMessage: fmt.Sprintf("cloud chunks available across %d project(s): %d", len(p.projects), totalChunks),
	}
}

type cloudServerRuntime interface {
	Start() error
}

type defaultCloudRuntime struct {
	server   *cloudserver.CloudServer
	store    *cloudstore.CloudStore
	onSIGHUP func()                          // called when SIGHUP is received (e.g. users.Loader.Reload)
	onWatch  func(ctx context.Context) error // fsnotify watcher; nil when no users file is configured
	ctx      context.Context                 // cancelled by Start() on return; drives onWatch lifecycle
	cancel   context.CancelFunc
}

func (r *defaultCloudRuntime) Start() error {
	// Cancel the runtime context when Start returns so the watcher goroutine
	// (and any other ctx-aware goroutine) terminates cleanly.
	defer r.cancel()
	defer r.store.Close()
	// Wire SIGHUP → onSIGHUP (e.g. users.YAMLLoader.Reload) when registered.
	if r.onSIGHUP != nil {
		sighupCh := make(chan os.Signal, 1)
		signal.Notify(sighupCh, syscall.SIGHUP)
		go func() {
			for range sighupCh {
				log.Printf("[engram-cloud] SIGHUP received — reloading user directory")
				r.onSIGHUP()
			}
		}()
		defer func() {
			signal.Stop(sighupCh)
			close(sighupCh)
		}()
	}
	// Start the fsnotify watcher alongside SIGHUP as a belt-and-suspenders
	// live-reload path: file edits and atomic renames are detected automatically
	// without requiring an operator-sent SIGHUP signal.
	if r.onWatch != nil {
		go func() {
			if err := r.onWatch(r.ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[engram-cloud] users.Watch: terminated unexpectedly: %v", err)
			}
		}()
	}
	return r.server.Start()
}

var newCloudRuntime = func(cfg cloud.Config) (cloudServerRuntime, error) {
	// Create the runtime context up-front; Start() will cancel it on return so
	// all ctx-aware goroutines (e.g. the fsnotify watcher) terminate cleanly.
	ctx, cancel := context.WithCancel(context.Background())

	cs, err := cloudstore.New(cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	allowedProjects := normalizeAllowedProjects(cfg.AllowedProjects)
	if err := backfillAllowedProjectMutationChunks(context.Background(), cs, allowedProjects); err != nil {
		cancel()
		_ = cs.Close()
		return nil, err
	}
	cs.SetDashboardAllowedProjects(allowedProjects)

	runtime := &defaultCloudRuntime{store: cs, ctx: ctx, cancel: cancel}

	// When ENGRAM_USERS_FILE is set, use HeaderAuthenticator (X-Forwarded-Email +
	// YAMLLoader) instead of static bearer-token auth. This is the OAuth-era auth
	// seam: an upstream proxy (oauth2-proxy) sets X-Forwarded-Email and we resolve
	// the identity from the user directory for per-request authorization.
	usersFile := strings.TrimSpace(cfg.UsersFile)
	if usersFile != "" {
		loader, err := users.NewYAMLLoader(usersFile)
		if err != nil {
			cancel()
			_ = cs.Close()
			return nil, fmt.Errorf("newCloudRuntime: load users file %q: %w", usersFile, err)
		}
		// Pass ENGRAM_CLOUD_TOKEN as emergency bypass token. When set, requests
		// bearing it as a Bearer credential bypass header auth and authenticate
		// as the directory's sole admin (spec: oauth-authentication §Emergency Bypass).
		bypassToken := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN"))
		// Pass ENGRAM_JWT_SECRET to activate Bearer JWT verification on /sync/* routes.
		// CLI sends Bearer <engram-JWT> on sync routes (which bypass oauth2-proxy per
		// Caddy routing split — no X-Forwarded-Email there).
		jwtSecretForAuth := strings.TrimSpace(os.Getenv("ENGRAM_JWT_SECRET"))
		headerAuth, err := auth.NewHeaderAuthenticatorWithJWT(loader, bypassToken, jwtSecretForAuth)
		if err != nil {
			cancel()
			_ = cs.Close()
			return nil, fmt.Errorf("newCloudRuntime: configure header authenticator: %w", err)
		}
		// SIGHUP → reload user directory so operator changes take effect without restart.
		// D6: also reload classrules when configured (fan-out, each reload is independent).
		classrulesFile := strings.TrimSpace(cfg.ClassrulesFile)
		var classrulesLoader *classrules.ClassrulesLoader
		if classrulesFile != "" {
			cl, clErr := classrules.NewClassrulesLoader(classrulesFile)
			if clErr != nil {
				cancel()
				_ = cs.Close()
				return nil, fmt.Errorf("newCloudRuntime: load classrules file %q: %w", classrulesFile, clErr)
			}
			classrulesLoader = cl
			log.Printf("[engram-cloud] classrules loaded from %s (%d games)", classrulesFile, func() int {
				if cfg := cl.Current(); cfg != nil {
					return len(cfg.Games)
				}
				return 0
			}())
		}
		runtime.onSIGHUP = func() {
			if err := loader.Reload(); err != nil {
				log.Printf("[engram-cloud] users.Reload: %v (retaining last-good directory)", err)
			} else {
				log.Printf("[engram-cloud] user directory reloaded from %s", usersFile)
			}
			if classrulesLoader != nil {
				if err := classrulesLoader.Reload(); err != nil {
					log.Printf("[engram-cloud] classrules.Reload: %v (retaining last-good config)", err)
				} else {
					log.Printf("[engram-cloud] classrules reloaded from %s", classrulesFile)
				}
			}
		}
		// D3: also watch the parent directory via fsnotify so that admin writes
		// (atomic rename path) are detected automatically without SIGHUP.
		// loader.Watch and onSIGHUP both call loader.Reload() — idempotent.
		runtime.onWatch = loader.Watch
		jwtSecret := strings.TrimSpace(os.Getenv("ENGRAM_JWT_SECRET"))
		runtime.server = cloudserver.New(
			cs,
			headerAuth,
			cfg.Port,
			cloudserver.WithHost(cfg.BindHost),
			cloudserver.WithProjectAuthorizer(headerAuth),
			cloudserver.WithDashboardAdminToken(cfg.AdminToken),
			cloudserver.WithMaxPushBodyBytes(cfg.MaxPushBodyBytes),
			cloudserver.WithSyncStatusProvider(cloudDashboardStatusProvider{store: cs, projects: allowedProjects}),
			// Thread the build-time version (ldflags main.version) into the server so
			// the dashboard version indicator can display cloud version. (REQ-VID-04, ADR-4)
			cloudserver.WithServerVersion(version),
			// Register GET /auth endpoint for CLI OAuth loopback flow (Opción A).
			// Requires ENGRAM_JWT_SECRET to be set (validated below via validateCloudServeAuthConfig).
			cloudserver.WithAuthEndpoint(loader, jwtSecret),
			// Wire configurable JWT TTL: parse ENGRAM_JWT_TTL once at startup; both
			// handleAuth and autoLoginFromHeader use s.jwtTTL via MintJWT.
			cloudserver.WithJWTTTL(resolveJWTTTL()),
			// D4: wire YAMLLoader.Reload as the user-directory reload callback so
			// admin member-management writes take effect in-process without restart.
			cloudserver.WithUserDirectoryReload(loader.Reload),
			// D4: set usersFilePath so admin handlers can write + git-commit users.yaml.
			cloudserver.WithUsersFilePath(usersFile),
			// D6: wire ClassrulesLoader.Reload so admin games writes update the in-memory
			// config without a process restart. Only wired when ENGRAM_CLASSIFICATION_RULES
			// is set and the loader was successfully initialised.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					return cloudserver.WithClassrulesReload(classrulesLoader.Reload)
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// D6: wire ClassrulesLoader.Current().Games as the live games getter for the admin UI.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader // capture
					return cloudserver.WithClassrulesCurrentGames(func() []string {
						cfg := cl.Current()
						if cfg == nil {
							return nil
						}
						return cfg.Games
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// D6: set classrulesFilePath so admin games handlers know where to write.
			cloudserver.WithClassrulesFilePath(classrulesFile),
			// Slice B: wire ListColors — returns current GraphColors from in-memory config.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader // capture
					return cloudserver.WithClassrulesCurrentColors(func() (map[string]string, map[string]string) {
						cfg := cl.Current()
						if cfg == nil {
							return nil, nil
						}
						return cfg.GraphColors.Games, cfg.GraphColors.Departments
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// Block A: WriteGameColor — writes graph_colors.games[name] only.
			// Separated from dept colors (no isDeptKey disambiguation needed).
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					path := classrulesFile
					return cloudserver.WithClassrulesWriteColor(func(name, color string) error {
						return writeColorToMap(cl, path, name, color, true /* isGame */)
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// Block A: WriteDeptColor — writes graph_colors.departments[name] only.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					path := classrulesFile
					return cloudserver.WithClassrulesWriteDeptColor(func(name, color string) error {
						return writeColorToMap(cl, path, name, color, false /* isGame */)
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// SaveGame: combined games list + game colors write (atomic via WriteGameEntry).
			// Used by POST /dashboard/admin/games/save (add, rename, color-only update).
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					path := classrulesFile
					return cloudserver.WithSaveGame(func(newGames []string, newGameColors map[string]string) error {
						return writeGameEntry(cl, path, newGames, newGameColors)
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// DeleteGame: combined games list + game colors write after removing one entry.
			// Used by POST /dashboard/admin/games/delete.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					path := classrulesFile
					return cloudserver.WithDeleteGame(func(newGames []string, newGameColors map[string]string) error {
						return writeGameEntryAllowEmpty(cl, path, newGames, newGameColors)
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// ListDepartmentsCanonical: returns dept names from the canonical classrules config.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					return cloudserver.WithClassrulesCurrentDepts(func() []string {
						cfg := cl.Current()
						if cfg == nil {
							return nil
						}
						names := make([]string, 0, len(cfg.Departments))
						for _, d := range cfg.Departments {
							names = append(names, d.Name)
						}
						return names
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// ListDeptEntriesCanonical: returns full dept entries (name + aliases) from classrules.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					return cloudserver.WithClassrulesCurrentDeptEntries(func() []dashboard.DeptEntry {
						cfg := cl.Current()
						if cfg == nil {
							return nil
						}
						entries := make([]dashboard.DeptEntry, 0, len(cfg.Departments))
						for _, d := range cfg.Departments {
							entries = append(entries, dashboard.DeptEntry{
								Name:    d.Name,
								Aliases: d.Aliases,
							})
						}
						return entries
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// SaveDept: combined dept list + dept colors write (atomic via WriteDeptEntry).
			// Used by POST /dashboard/admin/departments/save (add, rename, color-only update).
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					path := classrulesFile
					return cloudserver.WithSaveDept(func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error {
						return writeDeptEntry(cl, path, newDepts, newDeptColors)
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// DeleteDept: combined dept list + dept colors write after removing one entry.
			// Used by POST /dashboard/admin/departments/delete.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					path := classrulesFile
					return cloudserver.WithDeleteDept(func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error {
						return writeDeptEntry(cl, path, newDepts, newDeptColors)
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// ListRules: returns current cfg.Rules text from the in-memory ClassrulesLoader.
			// Used by GET /dashboard/admin/rules to pre-fill the textarea.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					return cloudserver.WithClassrulesListRules(func() string {
						cfg := cl.Current()
						if cfg == nil {
							return ""
						}
						return cfg.Rules
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
			// SaveRules: persists updated rules text atomically via classrules.WriteRules.
			// Used by POST /dashboard/admin/rules.
			func() cloudserver.Option {
				if classrulesLoader != nil {
					cl := classrulesLoader
					path := classrulesFile
					return cloudserver.WithClassrulesSaveRules(func(newRules string) error {
						return classrules.WriteRules(path, cl, newRules)
					})
				}
				return func(*cloudserver.CloudServer) {} // no-op option
			}(),
		)
		return runtime, nil
	}

	// Legacy path: bearer-token auth (ENGRAM_CLOUD_TOKEN).
	projectAuth := auth.NewProjectScopeAuthorizer(allowedProjects)
	token := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN"))
	insecureNoAuth := token == "" && envBool("ENGRAM_CLOUD_INSECURE_NO_AUTH")
	var authenticator cloudserver.Authenticator
	if !insecureNoAuth {
		authSvc, err := auth.NewService(cs, cfg.JWTSecret)
		if err != nil {
			_ = cs.Close()
			return nil, err
		}
		authSvc.SetBearerToken(token)
		authSvc.SetAllowedProjects(allowedProjects)
		authSvc.SetDashboardSessionTokens([]string{cfg.AdminToken})
		authenticator = authSvc
	}
	runtime.server = cloudserver.New(
		cs,
		authenticator,
		cfg.Port,
		cloudserver.WithHost(cfg.BindHost),
		cloudserver.WithProjectAuthorizer(projectAuth),
		cloudserver.WithDashboardAdminToken(cfg.AdminToken),
		cloudserver.WithMaxPushBodyBytes(cfg.MaxPushBodyBytes),
		cloudserver.WithSyncStatusProvider(cloudDashboardStatusProvider{store: cs, projects: allowedProjects}),
		// Wire the build-time version so the dashboard version indicator renders
		// the cloud's own version. Per-contributor client-version RECORDING is NOT
		// available on this legacy path because auth.Service does not implement the
		// Attribution interface (withAuth requires it to resolve the caller's email).
		// Only the OAuth2/header path (above) supports per-contributor recording.
		cloudserver.WithServerVersion(version),
	)
	return runtime, nil
}

func backfillAllowedProjectMutationChunks(ctx context.Context, cs *cloudstore.CloudStore, projects []string) error {
	for _, project := range projects {
		report, err := cs.BackfillMutationChunks(ctx, project, true)
		if err != nil {
			return fmt.Errorf("cloud repair materialize-mutations for project %q: %w", project, err)
		}
		if report.CandidateMutations > 0 || report.ChunksInserted > 0 {
			fmt.Fprintf(os.Stderr,
				"engram cloud repair materialize-mutations: project=%s candidates=%d already_materialized=%d chunks_planned=%d chunks_inserted=%d\n",
				report.Project, report.CandidateMutations, report.AlreadyMaterialized, report.ChunksPlanned, report.ChunksInserted,
			)
		}
	}
	return nil
}

var runUpgradeBootstrap = func(s *store.Store, project string, cc *cloudConfig) (*engramsync.UpgradeBootstrapResult, error) {
	transport, err := remote.NewRemoteTransport(cc.ServerURL, cc.Token, project, version)
	if err != nil {
		return nil, err
	}
	return engramsync.BootstrapProject(s, transport, engramsync.UpgradeBootstrapOptions{Project: project, CreatedBy: "engram-cloud-upgrade"})
}

type cloudConfig struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

func cmdCloud(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram cloud <subcommand> [options]")
		fmt.Fprintln(os.Stderr, "supported subcommands: status, enroll, config, serve, upgrade, repair")
		exitFunc(1)
	}
	if os.Args[2] == "--help" || os.Args[2] == "-h" || os.Args[2] == "help" {
		fmt.Println("usage: engram cloud <subcommand> [options]")
		fmt.Println("supported subcommands: status, enroll, config, serve, upgrade, repair")
		return
	}

	switch os.Args[2] {
	case "status":
		cmdCloudStatus(cfg)
	case "enroll":
		cmdCloudEnroll(cfg)
	case "config":
		cmdCloudConfig(cfg)
	case "serve":
		cmdCloudServe()
	case "upgrade":
		cmdCloudUpgrade(cfg)
	case "repair":
		cmdCloudRepair()
	default:
		fmt.Fprintf(os.Stderr, "unknown cloud command: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, "supported subcommands: status, enroll, config, serve, upgrade, repair")
		exitFunc(1)
	}
}

func cmdCloudRepair() {
	if len(os.Args) < 4 || os.Args[3] == "--help" || os.Args[3] == "-h" || os.Args[3] == "help" {
		fmt.Println("usage: engram cloud repair materialize-mutations --project <name> (--dry-run|--apply)")
		fmt.Println("repairs existing cloud_mutations into compatible cloud_chunks without deleting remote data")
		return
	}
	command := strings.TrimSpace(strings.ToLower(os.Args[3]))
	if command != "materialize-mutations" {
		fmt.Fprintf(os.Stderr, "unknown cloud repair command: %s\n", command)
		fmt.Fprintln(os.Stderr, "supported cloud repair commands: materialize-mutations")
		exitFunc(1)
		return
	}
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud repair materialize-mutations --project <name> (--dry-run|--apply)")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	dryRun := hasCloudUpgradeFlag(os.Args[4:], "--dry-run")
	apply := hasCloudUpgradeFlag(os.Args[4:], "--apply")
	if dryRun == apply {
		fmt.Fprintln(os.Stderr, "usage: engram cloud repair materialize-mutations --project <name> (--dry-run|--apply)")
		fmt.Fprintln(os.Stderr, "error: exactly one of --dry-run or --apply is required")
		exitFunc(1)
		return
	}

	cs, err := cloudstore.New(cloud.ConfigFromEnv())
	if err != nil {
		fatal(err)
		return
	}
	defer cs.Close()
	report, err := cs.BackfillMutationChunks(context.Background(), project, apply)
	if err != nil {
		fatal(err)
		return
	}
	encoded, err := jsonMarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(encoded))
}

func cmdCloudUpgrade(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade <doctor|repair|bootstrap|status|rollback> --project <name>")
		exitFunc(1)
		return
	}
	command := strings.TrimSpace(strings.ToLower(os.Args[3]))
	if command == "--help" || command == "-h" || command == "help" {
		fmt.Println("engram cloud upgrade")
		fmt.Println("workflow: doctor -> repair -> bootstrap -> status/rollback")
		fmt.Println("cloud is opt-in replication/shared access; local SQLite remains source of truth")
		fmt.Println("usage: engram cloud upgrade <doctor|repair|bootstrap|status|rollback> --project <name>")
		return
	}
	switch command {
	case "doctor":
		cmdCloudUpgradeDoctor(cfg)
	case "repair":
		cmdCloudUpgradeRepair(cfg)
	case "bootstrap":
		cmdCloudUpgradeBootstrap(cfg)
	case "status":
		cmdCloudUpgradeStatus(cfg)
	case "rollback":
		cmdCloudUpgradeRollback(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown cloud upgrade command: %s\n", command)
		fmt.Fprintln(os.Stderr, "supported cloud upgrade commands: doctor, repair, bootstrap, status, rollback")
		exitFunc(1)
	}
}

func cmdCloudUpgradeDoctor(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade doctor --project <name>")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	cloudConfigured := false
	if cc, cfgErr := resolveCloudRuntimeConfig(cfg); cfgErr == nil {
		if cc != nil {
			if validated, err := validateCloudServerURL(cc.ServerURL); err == nil && strings.TrimSpace(validated) != "" {
				cloudConfigured = true
			}
		}
	}
	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade doctor enrollment check: %w", err))
		return
	}
	policyDenied, err := cloudUpgradePolicyDenied(s, project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade doctor policy check: %w", err))
		return
	}

	report, err := engramsync.DiagnoseCloudUpgrade(engramsync.UpgradeDiagnosisInput{
		Project:         project,
		CloudConfigured: cloudConfigured,
		ProjectEnrolled: enrolled,
		PolicyDenied:    policyDenied,
	})
	if err != nil {
		fatal(err)
		return
	}

	legacyReport, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade doctor legacy mutation diagnosis: %w", err))
		return
	}
	if legacyReport.BlockedCount > 0 {
		first := legacyReport.Findings[0]
		report = engramsync.UpgradeDiagnosisReport{
			Status:  engramsync.UpgradeStatusBlocked,
			Class:   engramsync.UpgradeReasonClassBlocked,
			Code:    store.UpgradeReasonBlockedLegacyMutationManual,
			Message: fmt.Sprintf("manual-action-required: %s (seq=%d entity=%s op=%s)", first.Message, first.Seq, first.Entity, first.Op),
		}
	} else if legacyReport.RepairableCount > 0 {
		report = engramsync.UpgradeDiagnosisReport{
			Status:  engramsync.UpgradeStatusBlocked,
			Class:   engramsync.UpgradeReasonClassRepairable,
			Code:    store.UpgradeReasonRepairableLegacyMutationPayload,
			Message: fmt.Sprintf("project %q has %d repairable legacy mutation payload issue(s); run `engram cloud upgrade repair --project %s --apply`", project, legacyReport.RepairableCount, project),
		}
	}

	stage := store.UpgradeStageDoctorBlocked
	if report.Status == engramsync.UpgradeStatusReady {
		stage = store.UpgradeStageDoctorReady
	}
	_ = s.SaveCloudUpgradeState(store.CloudUpgradeState{
		Project:          project,
		Stage:            stage,
		RepairClass:      report.Class,
		LastErrorCode:    report.Code,
		LastErrorMessage: report.Message,
	})

	fmt.Printf("project: %s\n", project)
	fmt.Printf("status: %s\n", report.Status)
	fmt.Printf("class: %s\n", report.Class)
	fmt.Printf("reason_code: %s\n", report.Code)
	fmt.Printf("message: %s\n", report.Message)
}

func cloudUpgradePolicyDenied(s *store.Store, project string) (bool, error) {
	targets := []string{cloudTargetKeyForProject(project)}
	if cloudTargetKeyForProject(project) != constants.TargetKeyCloud {
		targets = append(targets, constants.TargetKeyCloud)
	}
	for _, targetKey := range targets {
		state, err := s.GetSyncState(targetKey)
		if err != nil {
			return false, err
		}
		if state == nil {
			continue
		}
		if strings.TrimSpace(derefString(state.ReasonCode)) == constants.ReasonPolicyForbidden {
			return true, nil
		}
	}
	return false, nil
}

func parseCloudUpgradeProjectArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if strings.TrimSpace(args[i]) != "--project" {
			continue
		}
		if i+1 >= len(args) {
			return ""
		}
		project, _ := store.NormalizeProject(args[i+1])
		return strings.TrimSpace(project)
	}
	return ""
}

func cmdCloudUpgradeRepair(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade repair --project <name> [--dry-run|--apply]")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	apply := hasCloudUpgradeFlag(os.Args[4:], "--apply")
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	report, err := s.RepairCloudUpgrade(project, apply)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("class: %s\n", report.Class)
	fmt.Printf("reason_code: %s\n", report.ReasonCode)
	fmt.Printf("message: %s\n", report.Message)
	fmt.Printf("applied: %t\n", report.Applied)
}

func cmdCloudUpgradeBootstrap(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade bootstrap --project <name> [--resume]")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		fatal(err)
		return
	}
	if cc == nil || strings.TrimSpace(cc.ServerURL) == "" {
		fatal(fmt.Errorf("cloud upgrade bootstrap requires configured cloud server"))
		return
	}
	validatedURL, err := validateCloudServerURL(cc.ServerURL)
	if err != nil {
		fatal(fmt.Errorf("invalid cloud runtime server URL: %w", err))
		return
	}
	cc.ServerURL = validatedURL
	if err := captureUpgradeSnapshotBeforeBootstrap(s, cfg, project); err != nil {
		fatal(err)
		return
	}
	legacyReport, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade bootstrap legacy mutation diagnosis: %w", err))
		return
	}
	if legacyReport.BlockedCount > 0 {
		first := legacyReport.Findings[0]
		fatal(fmt.Errorf("legacy mutation payloads require manual action before bootstrap (seq=%d entity=%s op=%s): %s", first.Seq, first.Entity, first.Op, first.Message))
		return
	}
	if legacyReport.RepairableCount > 0 {
		fatal(fmt.Errorf("legacy mutation payloads require repair before bootstrap: run `engram cloud upgrade repair --project %s --apply`", project))
		return
	}

	result, err := runUpgradeBootstrap(s, project, cc)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("stage: %s\n", result.Stage)
	fmt.Printf("resumed: %t\n", result.Resumed)
	fmt.Printf("noop: %t\n", result.NoOp)
}

func captureUpgradeSnapshotBeforeBootstrap(s *store.Store, cfg store.Config, project string) error {
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		return fmt.Errorf("load cloud upgrade state before bootstrap snapshot: %w", err)
	}
	if state != nil {
		snapshot := state.Snapshot
		if snapshot.CloudConfigPresent || strings.TrimSpace(snapshot.CloudConfigJSON) != "" || snapshot.ProjectEnrolled {
			return nil
		}
	}

	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		return fmt.Errorf("load project enrollment before bootstrap snapshot: %w", err)
	}

	var snapshot store.CloudUpgradeSnapshot
	configBytes, err := os.ReadFile(cloudConfigPath(cfg))
	if err == nil {
		snapshot.CloudConfigPresent = true
		snapshot.CloudConfigJSON = string(configBytes)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read cloud config for bootstrap snapshot: %w", err)
	}
	snapshot.ProjectEnrolled = enrolled

	next := store.CloudUpgradeState{Project: project, Stage: store.UpgradeStagePlanned, RepairClass: store.UpgradeRepairClassNone, Snapshot: snapshot}
	if state != nil {
		next = *state
		next.Snapshot = snapshot
	}
	if err := s.SaveCloudUpgradeState(next); err != nil {
		return fmt.Errorf("persist pre-bootstrap rollback snapshot: %w", err)
	}
	return nil
}

func cmdCloudUpgradeStatus(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade status --project <name>")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		fatal(err)
		return
	}
	if state == nil {
		fmt.Printf("project: %s\n", project)
		fmt.Printf("stage: %s\n", store.UpgradeStagePlanned)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("stage: %s\n", state.Stage)
	fmt.Printf("class: %s\n", state.RepairClass)
	fmt.Printf("reason_code: %s\n", strings.TrimSpace(state.LastErrorCode))
	fmt.Printf("reason_message: %s\n", strings.TrimSpace(state.LastErrorMessage))
}

func cmdCloudUpgradeRollback(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade rollback --project <name>")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		fatal(err)
		return
	}
	if state == nil {
		fatal(fmt.Errorf("rollback requires existing upgrade checkpoint state"))
		return
	}
	canRollback, err := s.CanRollbackCloudUpgrade(project)
	if err != nil {
		fatal(err)
		return
	}
	if !canRollback {
		fmt.Fprintln(os.Stderr, "rollback is unavailable post-bootstrap; use explicit disconnect/unenroll flows")
		exitFunc(1)
		return
	}
	if state.Snapshot.CloudConfigPresent {
		if err := os.WriteFile(cloudConfigPath(cfg), []byte(state.Snapshot.CloudConfigJSON), 0o644); err != nil {
			fatal(err)
			return
		}
	} else {
		_ = os.Remove(cloudConfigPath(cfg))
	}
	rolledBack, err := engramsync.RollbackProject(s, engramsync.UpgradeRollbackOptions{Project: project})
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("stage: %s\n", rolledBack.Stage)
}

func hasCloudUpgradeFlag(args []string, flag string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == flag {
			return true
		}
	}
	return false
}

func cmdCloudStatus(cfg store.Config) {
	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: unable to read cloud runtime config: %v\n", err)
		exitFunc(1)
		return
	}
	if cc == nil || cc.ServerURL == "" {
		fmt.Println("Cloud status: not configured")
		return
	}
	validatedURL, err := validateCloudServerURL(cc.ServerURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid cloud runtime server URL: %v\n", err)
		exitFunc(1)
		return
	}
	cc.ServerURL = validatedURL
	token := strings.TrimSpace(cc.Token)
	insecureNoAuth := envBool("ENGRAM_CLOUD_INSECURE_NO_AUTH")
	fmt.Printf("Cloud status: configured (target=%s)\n", constants.TargetKeyCloud)
	fmt.Printf("Server: %s\n", cc.ServerURL)
	if token == "" {
		if insecureNoAuth {
			fmt.Println("Auth status: ready (insecure local-dev mode: ENGRAM_CLOUD_INSECURE_NO_AUTH=1)")
			fmt.Println("Sync readiness: ready for explicit --project sync (project must be enrolled)")
			fmt.Println("Warning: bearer auth is disabled in insecure mode; do not use in production")
			printCloudStatusDaemonProbe()
			printCloudStatusSyncDiagnostic(cfg)
			return
		}
		fmt.Println("Auth status: token not configured (client token is optional at preflight)")
		fmt.Println("Sync readiness: ready to attempt explicit --project sync (project must be enrolled)")
		fmt.Println("Hint: if the remote server enforces bearer auth, set ENGRAM_CLOUD_TOKEN")
		printCloudStatusDaemonProbe()
		printCloudStatusSyncDiagnostic(cfg)
		return
	}
	fmt.Println("Auth status: ready (token provided via runtime cloud config)")
	fmt.Println("Sync readiness: ready for explicit --project sync (project must be enrolled)")
	printCloudStatusDaemonProbe()
	printCloudStatusSyncDiagnostic(cfg)
}

func printCloudStatusSyncDiagnostic(cfg store.Config) {
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); err != nil {
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fmt.Printf("Sync diagnostic: unavailable (%v)\n", err)
		return
	}
	defer s.Close()
	state, err := s.GetSyncState(constants.TargetKeyCloud)
	if err != nil || state == nil {
		return
	}
	code := strings.TrimSpace(derefString(state.ReasonCode))
	message := strings.TrimSpace(derefString(state.ReasonMessage))
	if code == "" && message == "" {
		return
	}
	fmt.Printf("Sync diagnostic: %s\n", state.Lifecycle)
	if code != "" {
		fmt.Printf("reason_code: %s\n", code)
	}
	if message != "" {
		fmt.Printf("reason_message: %s\n", message)
	}
}

func cmdCloudEnroll(cfg store.Config) {
	if len(os.Args) >= 4 {
		arg := strings.TrimSpace(os.Args[3])
		if arg == "--help" || arg == "-h" || arg == "help" {
			fmt.Println("usage: engram cloud enroll <project>")
			fmt.Println("Enroll a local-first project for explicit cloud replication.")
			return
		}
	}
	if len(os.Args) < 4 || strings.TrimSpace(os.Args[3]) == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud enroll <project>")
		exitFunc(1)
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	projectName := strings.TrimSpace(os.Args[3])
	if err := s.EnrollProject(projectName); err != nil {
		fatal(err)
		return
	}

	fmt.Printf("✓ Project %q enrolled for cloud sync\n", projectName)
}

func cmdCloudConfig(cfg store.Config) {
	if len(os.Args) < 5 || os.Args[3] != "--server" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud config --server <url>")
		exitFunc(1)
	}
	cc := &cloudConfig{ServerURL: strings.TrimSpace(os.Args[4])}
	if cc.ServerURL == "" {
		fmt.Fprintln(os.Stderr, "error: server URL is required")
		exitFunc(1)
	}
	validatedURL, err := validateCloudServerURL(cc.ServerURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid server URL: %v\n", err)
		exitFunc(1)
	}
	cc.ServerURL = validatedURL
	if err := saveCloudConfig(cfg, cc); err != nil {
		fatal(err)
		return
	}
	fmt.Printf("✓ Cloud server set to %s\n", cc.ServerURL)
}

func validateCloudServerURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", fmt.Errorf("host is required")
	}
	if strings.TrimSpace(parsed.RawQuery) != "" {
		return "", fmt.Errorf("query is not allowed")
	}
	if strings.TrimSpace(parsed.Fragment) != "" {
		return "", fmt.Errorf("fragment is not allowed")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func cmdCloudServe() {
	runtimeCfg := cloud.ConfigFromEnv()
	if err := validateCloudServeAuthConfig(); err != nil {
		fatal(err)
		return
	}
	runtime, err := newCloudRuntime(runtimeCfg)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("Starting Engram cloud server on port %d\n", runtimeCfg.Port)
	if err := runtime.Start(); err != nil {
		fatal(err)
	}
}

func validateCloudServeAuthConfig() error {
	token := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN"))
	adminToken := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_ADMIN"))
	insecureNoAuth := envBool("ENGRAM_CLOUD_INSECURE_NO_AUTH")
	allowlist := normalizeAllowedProjects(cloud.ConfigFromEnv().AllowedProjects)
	jwtSecretEnv := strings.TrimSpace(os.Getenv("ENGRAM_JWT_SECRET"))
	if insecureNoAuth && token != "" {
		return fmt.Errorf("conflicting cloud auth configuration: ENGRAM_CLOUD_INSECURE_NO_AUTH=1 cannot be used together with ENGRAM_CLOUD_TOKEN")
	}
	if token != "" && len(allowlist) > 0 {
		if jwtSecretEnv == "" {
			return fmt.Errorf("authenticated cloud serve requires explicit ENGRAM_JWT_SECRET (non-default); refusing implicit default secret")
		}
		if cloud.IsDefaultJWTSecret(jwtSecretEnv) {
			return fmt.Errorf("authenticated cloud serve requires a non-default ENGRAM_JWT_SECRET; refusing development default")
		}
		return nil
	}
	if insecureNoAuth {
		if len(allowlist) == 0 {
			return fmt.Errorf("cloud project allowlist is required even in insecure mode: set ENGRAM_CLOUD_ALLOWED_PROJECTS to one or more project names")
		}
		if adminToken != "" {
			return fmt.Errorf("ENGRAM_CLOUD_ADMIN is not supported when ENGRAM_CLOUD_INSECURE_NO_AUTH=1; remove ENGRAM_CLOUD_ADMIN or enable authenticated mode")
		}
		fmt.Fprintln(os.Stderr, "warning: ENGRAM_CLOUD_INSECURE_NO_AUTH=1 disables cloud API authentication; do not use in production")
		return nil
	}
	if token == "" {
		return fmt.Errorf("cloud auth token is required: set ENGRAM_CLOUD_TOKEN (or ENGRAM_CLOUD_INSECURE_NO_AUTH=1 for local insecure development)")
	}
	return fmt.Errorf("cloud project allowlist is required: set ENGRAM_CLOUD_ALLOWED_PROJECTS to one or more project names")
}

func normalizeAllowedProjects(projects []string) []string {
	normalized := make([]string, 0, len(projects))
	seen := make(map[string]struct{})
	for _, project := range projects {
		name, _ := store.NormalizeProject(project)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

// writeColorToMap performs a read-modify-write of the classrules graph_colors block.
// When isGame is true, name updates graph_colors.games; otherwise graph_colors.departments.
// Clones maps to avoid mutating the live in-memory config; initialises nil maps on first use.
func writeColorToMap(cl *classrules.ClassrulesLoader, path, name, color string, isGame bool) error {
	cfg := cl.Current()
	var gameColors, deptColors map[string]string
	if cfg != nil {
		if cfg.GraphColors.Games != nil {
			gameColors = make(map[string]string, len(cfg.GraphColors.Games))
			for k, v := range cfg.GraphColors.Games {
				gameColors[k] = v
			}
		}
		if cfg.GraphColors.Departments != nil {
			deptColors = make(map[string]string, len(cfg.GraphColors.Departments))
			for k, v := range cfg.GraphColors.Departments {
				deptColors[k] = v
			}
		}
	}
	if gameColors == nil {
		gameColors = make(map[string]string)
	}
	if deptColors == nil {
		deptColors = make(map[string]string)
	}
	if isGame {
		gameColors[name] = color
	} else {
		deptColors[name] = color
	}
	return classrules.WriteColors(path, gameColors, deptColors, func() {
		if err := cl.Reload(); err != nil {
			log.Printf("[engram-cloud] classrules.Reload after color write: %v (retaining last-good)", err)
		}
	})
}

// writeGameEntry performs a combined atomic write of the games list and game-color
// map to the classrules YAML. Both fields are patched in a single write so rename
// operations never leave the list and color map inconsistent.
// Delegates to classrules.WriteGameEntry which validates and writes atomically.
func writeGameEntry(cl *classrules.ClassrulesLoader, path string, newGames []string, newGameColors map[string]string) error {
	bridge := &classrulesBridge{cl: cl}
	return classrules.WriteGameEntry(path, bridge, newGames, newGameColors)
}

// writeGameEntryAllowEmpty is like writeGameEntry but permits an empty games list
// (used by the delete handler — deleting the last game empties the list).
// Delegates to classrules.WriteGameEntryAllowEmpty which writes both the games list
// and color map atomically without the non-empty constraint.
func writeGameEntryAllowEmpty(cl *classrules.ClassrulesLoader, path string, newGames []string, newGameColors map[string]string) error {
	bridge := &classrulesBridge{cl: cl}
	return classrules.WriteGameEntryAllowEmpty(path, bridge, newGames, newGameColors)
}

// classrulesBridge adapts *classrules.ClassrulesLoader to the classrules.Reloader interface
// so WriteGameEntry can call Reload after a successful atomic write.
type classrulesBridge struct {
	cl *classrules.ClassrulesLoader
}

func (b *classrulesBridge) Reload() error {
	if err := b.cl.Reload(); err != nil {
		log.Printf("[engram-cloud] classrules.Reload after game entry write: %v (retaining last-good)", err)
		return err
	}
	return nil
}

// writeDeptEntry converts dashboard.DeptEntry slice to classrules.Department slice and
// performs a combined atomic write of the departments list and dept-color map to the
// classrules YAML. Both fields are patched in a single write so rename operations never
// leave the list and color map inconsistent.
// Delegates to classrules.WriteDeptEntry which validates and writes atomically.
func writeDeptEntry(cl *classrules.ClassrulesLoader, path string, newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error {
	bridge := &classrulesBridge{cl: cl}
	// Convert dashboard.DeptEntry → classrules.Department.
	depts := make([]classrules.Department, 0, len(newDepts))
	for _, d := range newDepts {
		depts = append(depts, classrules.Department{
			Name:    d.Name,
			Aliases: d.Aliases,
		})
	}
	return classrules.WriteDeptEntry(path, bridge, depts, newDeptColors)
}

func cloudConfigPath(cfg store.Config) string {
	return filepath.Join(cfg.DataDir, "cloud.json")
}

func loadCloudConfig(cfg store.Config) (*cloudConfig, error) {
	path := cloudConfigPath(cfg)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cc cloudConfig
	if err := json.Unmarshal(b, &cc); err != nil {
		return nil, err
	}
	return &cc, nil
}

func saveCloudConfig(cfg store.Config, cc *cloudConfig) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cloudConfigPath(cfg), b, 0o644)
}
