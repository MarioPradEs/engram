package cloudserver

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
	engramproject "github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/Gentleman-Programming/engram/internal/version"
)

type Option func(*CloudServer)

type ChunkStore interface {
	ReadManifest(ctx context.Context, project string) (*engramsync.Manifest, error)
	WriteChunk(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte) error
	ReadChunk(ctx context.Context, project, chunkID string) ([]byte, error)
	KnownSessionIDs(ctx context.Context, project string) (map[string]struct{}, error)
}

type Authenticator interface {
	// Authorize validates the request and returns an enriched *http.Request whose
	// context carries the resolved principal. The returned request MUST be used for
	// all downstream calls so that AuthorizeProject, Attribution, and EnrolledProjects
	// can read the principal from the context (concurrency-safe shared instance).
	Authorize(r *http.Request) (*http.Request, error)
}

type ProjectAuthorizer interface {
	// AuthorizeProject returns nil when the project is allowed for the caller
	// identified by the principal in ctx (placed there by Authorize). ctx is
	// required so implementations can read request-scoped principal state.
	AuthorizeProject(ctx context.Context, project string) error
}

type dashboardSessionCodec interface {
	MintDashboardSession(bearerToken string) (string, error)
	ParseDashboardSession(sessionToken string) (string, error)
}

type staticStatusProvider struct{ status dashboard.SyncStatus }

func (s staticStatusProvider) Status() dashboard.SyncStatus { return s.status }

type CloudServer struct {
	store            ChunkStore
	auth             Authenticator
	projectAuth      ProjectAuthorizer
	dashboardAdmin   string
	port             int
	host             string
	serverVersion    string // set by WithServerVersion; passed to the dashboard for display
	maxPushBodyBytes int64
	mux              *http.ServeMux
	syncStatus       dashboard.SyncStatusProvider
	listenAndServe   func(addr string, handler http.Handler) error
	// /auth endpoint fields (set by WithAuthEndpoint option).
	authLoader    AuthUserLoader // user directory for /auth principal resolution
	authJWTSecret string         // ENGRAM_JWT_SECRET for minting JWTs in /auth
	jwtTTL        time.Duration  // configurable JWT lifetime; 0 means auth.DefaultJWTTTL
	// userReloadFn is called by admin member-management write handlers to reload
	// the in-memory user directory after a successful users.yaml write. (D4)
	userReloadFn func() error
	// usersFilePath is the absolute path to users.yaml inside the container. Used by
	// admin member-management handlers for atomic write + git commit (D4).
	usersFilePath string
	// classrulesReloadFn is called by admin games write handlers to reload the
	// cloud server's in-memory classrules Config after a successful write (D6).
	classrulesReloadFn func() error
	// classrulesCurrentGamesFn returns the current in-memory games list from the
	// ClassrulesLoader. Used by admin games UI to display the live list. (D6)
	classrulesCurrentGamesFn func() []string
	// classrulesFilePath is the absolute path to classification-rules.yaml inside the
	// container. Used by admin games handlers for atomic write + local git commit (D6).
	classrulesFilePath string
	// classrulesCurrentColorsFn returns the current (gameColors, deptColors) maps from
	// the in-memory ClassrulesLoader. Used by the admin games color section. (Slice B)
	classrulesCurrentColorsFn func() (map[string]string, map[string]string)
	// classrulesWriteColorFn persists a color for a game slug in graph_colors.games.
	// Performs read-modify-write on the classrules YAML, calling reload after success. (Slice B)
	classrulesWriteColorFn func(name, color string) error
	// classrulesWriteDeptColorFn persists a color for a dept key in graph_colors.departments.
	// Performs read-modify-write on the classrules YAML, calling reload after success. (Block A)
	classrulesWriteDeptColorFn func(name, color string) error
	// classrulesSaveGameFn is the combined save closure used by POST /dashboard/admin/games/save.
	// It receives the full new games list and game-color map and writes them atomically.
	classrulesSaveGameFn func(newGames []string, newGameColors map[string]string) error
	// classrulesDeleteGameFn is the combined delete closure used by POST /dashboard/admin/games/delete.
	// It receives the full new games list and game-color map (with the deleted entry removed) and writes atomically.
	classrulesDeleteGameFn func(newGames []string, newGameColors map[string]string) error
	// classrulesCurrentDeptsFn returns the current canonical department names from the
	// ClassrulesLoader (cfg.Departments[].Name). Used by the admin departments UI.
	classrulesCurrentDeptsFn func() []string
	// classrulesCurrentDeptEntriesFn returns the full department entries (name + aliases)
	// from the ClassrulesLoader. Used by save/delete handlers to preserve aliases.
	classrulesCurrentDeptEntriesFn func() []dashboard.DeptEntry
	// classrulesSaveDeptFn is the combined save closure used by POST /dashboard/admin/departments/save.
	// It receives the full new departments list and dept-color map and writes them atomically.
	classrulesSaveDeptFn func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error
	// classrulesDeleteDeptFn is the combined delete closure used by POST /dashboard/admin/departments/delete.
	// It receives the remaining departments list and dept-color map (deleted entry already removed).
	classrulesDeleteDeptFn func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error
	// classrulesListRulesFn returns the current free-form rules text from the in-memory
	// ClassrulesLoader. Used by GET /dashboard/admin/rules to pre-fill the textarea.
	classrulesListRulesFn func() string
	// classrulesSaveRulesFn persists updated rules text via classrules.WriteRules.
	// Called by POST /dashboard/admin/rules with the submitted rules form field.
	classrulesSaveRulesFn func(rules string) error
}

const defaultHost = "127.0.0.1"
const defaultMaxPushBodyBytes int64 = 8 * 1024 * 1024
const maxDashboardLoginBodyBytes int64 = 16 * 1024
const dashboardSessionCookieName = "engram_dashboard_token"

var ErrDashboardSessionCodecRequired = errors.New("dashboard session codec is required for dashboard auth")

func WithSyncStatusProvider(provider dashboard.SyncStatusProvider) Option {
	return func(s *CloudServer) {
		s.syncStatus = provider
	}
}

func WithHost(host string) Option {
	return func(s *CloudServer) {
		s.host = strings.TrimSpace(host)
	}
}

// WithServerVersion sets the server binary version displayed in the dashboard
// version indicator. Pass the same ldflags-injected main.version value used by
// the client. Satisfies: REQ-VID-04, ADR-4.
func WithServerVersion(v string) Option {
	return func(s *CloudServer) {
		s.serverVersion = strings.TrimSpace(v)
	}
}

func WithProjectAuthorizer(authorizer ProjectAuthorizer) Option {
	return func(s *CloudServer) {
		s.projectAuth = authorizer
	}
}

func WithDashboardAdminToken(adminToken string) Option {
	return func(s *CloudServer) {
		s.dashboardAdmin = strings.TrimSpace(adminToken)
	}
}

func WithMaxPushBodyBytes(limit int64) Option {
	return func(s *CloudServer) {
		if limit > 0 {
			s.maxPushBodyBytes = limit
		}
	}
}

// WithUserDirectoryReload registers a callback that the admin member-management
// handlers call after a successful atomic write to users.yaml. The callback
// should invoke YAMLLoader.Reload() so the in-memory directory is updated
// without a process restart (D4: in-process reload via existing reload hook).
func WithUserDirectoryReload(fn func() error) Option {
	return func(s *CloudServer) {
		s.userReloadFn = fn
	}
}

// WithUsersFilePath sets the absolute path to users.yaml used by admin member-management
// handlers for atomic write and local git commit (D4). In production this is sourced
// from ENGRAM_USERS_FILE; tests inject it directly.
func WithUsersFilePath(path string) Option {
	return func(s *CloudServer) {
		s.usersFilePath = path
	}
}

// WithClassrulesReload registers a callback that admin games write handlers call after a
// successful atomic write to classification-rules.yaml. The callback should invoke
// ClassrulesLoader.Reload() so the cloud server's in-memory Config is updated without a
// process restart (D6: in-process reload via onSIGHUP fan-out callback).
func WithClassrulesReload(fn func() error) Option {
	return func(s *CloudServer) {
		s.classrulesReloadFn = fn
	}
}

// WithClassrulesFilePath sets the absolute path to classification-rules.yaml used by
// admin games handlers for atomic write and local git commit (D6). In production this
// is sourced from ENGRAM_CLASSIFICATION_RULES; tests inject it directly.
func WithClassrulesFilePath(path string) Option {
	return func(s *CloudServer) {
		s.classrulesFilePath = path
	}
}

// WithClassrulesCurrentGames registers a closure that returns the current in-memory
// games vocabulary from the ClassrulesLoader. The admin games UI calls this to
// display the live list. nil means classrules is not configured. (D6)
func WithClassrulesCurrentGames(fn func() []string) Option {
	return func(s *CloudServer) {
		s.classrulesCurrentGamesFn = fn
	}
}

// WithClassrulesCurrentColors registers a closure that returns the current game and
// department color maps from the in-memory ClassrulesLoader. The admin color-map
// editor calls this on every GET /dashboard/admin/games request. (Slice B)
func WithClassrulesCurrentColors(fn func() (map[string]string, map[string]string)) Option {
	return func(s *CloudServer) {
		s.classrulesCurrentColorsFn = fn
	}
}

// WithClassrulesWriteColor registers the color-save closure used by
// POST /dashboard/admin/games/{name}/color. The closure receives a game slug
// plus a validated hex color and must persist via classrules.WriteColors
// (read-modify-write on graph_colors.games). (Slice B)
func WithClassrulesWriteColor(fn func(name, color string) error) Option {
	return func(s *CloudServer) {
		s.classrulesWriteColorFn = fn
	}
}

// WithClassrulesWriteDeptColor registers the color-save closure used by
// POST /dashboard/admin/departments/{name}/color. The closure receives a dept key
// plus a validated hex color and must persist via classrules.WriteColors
// (read-modify-write on graph_colors.departments). (Block A)
func WithClassrulesWriteDeptColor(fn func(name, color string) error) Option {
	return func(s *CloudServer) {
		s.classrulesWriteDeptColorFn = fn
	}
}

// WithSaveGame registers the combined save closure used by
// POST /dashboard/admin/games/save. The closure receives the full updated games
// list and game-color map and must write both atomically via classrules.WriteGameEntry.
func WithSaveGame(fn func(newGames []string, newGameColors map[string]string) error) Option {
	return func(s *CloudServer) {
		s.classrulesSaveGameFn = fn
	}
}

// WithDeleteGame registers the combined delete closure used by
// POST /dashboard/admin/games/delete. The closure receives the remaining games
// list and game-color map (deleted entry already removed) and writes atomically.
func WithDeleteGame(fn func(newGames []string, newGameColors map[string]string) error) Option {
	return func(s *CloudServer) {
		s.classrulesDeleteGameFn = fn
	}
}

// WithClassrulesCurrentDepts registers a closure that returns the current canonical
// department names from the ClassrulesLoader (cfg.Departments[].Name). The admin
// departments UI calls this on every GET /dashboard/admin/departments request.
func WithClassrulesCurrentDepts(fn func() []string) Option {
	return func(s *CloudServer) {
		s.classrulesCurrentDeptsFn = fn
	}
}

// WithClassrulesCurrentDeptEntries registers a closure that returns the full department
// entries (name + aliases) from the ClassrulesLoader. Save/delete handlers use this to
// preserve aliases through rename operations.
func WithClassrulesCurrentDeptEntries(fn func() []dashboard.DeptEntry) Option {
	return func(s *CloudServer) {
		s.classrulesCurrentDeptEntriesFn = fn
	}
}

// WithSaveDept registers the combined save closure used by
// POST /dashboard/admin/departments/save. The closure receives the full updated
// departments list and dept-color map and must write both atomically.
func WithSaveDept(fn func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error) Option {
	return func(s *CloudServer) {
		s.classrulesSaveDeptFn = fn
	}
}

// WithDeleteDept registers the combined delete closure used by
// POST /dashboard/admin/departments/delete. The closure receives the remaining
// departments list and dept-color map (deleted entry already removed) and writes atomically.
func WithDeleteDept(fn func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error) Option {
	return func(s *CloudServer) {
		s.classrulesDeleteDeptFn = fn
	}
}

// WithClassrulesListRules registers a closure that returns the current free-form
// classification rules text from the ClassrulesLoader. The admin rules editor
// calls this on every GET /dashboard/admin/rules request to pre-fill the textarea.
func WithClassrulesListRules(fn func() string) Option {
	return func(s *CloudServer) {
		s.classrulesListRulesFn = fn
	}
}

// WithClassrulesSaveRules registers the rules-save closure used by
// POST /dashboard/admin/rules. The closure receives the new rules text and
// must persist it via classrules.WriteRules (atomic write + reload).
func WithClassrulesSaveRules(fn func(rules string) error) Option {
	return func(s *CloudServer) {
		s.classrulesSaveRulesFn = fn
	}
}

func New(store ChunkStore, authSvc Authenticator, port int, opts ...Option) *CloudServer {
	s := &CloudServer{
		store:            store,
		auth:             authSvc,
		port:             port,
		host:             defaultHost,
		maxPushBodyBytes: defaultMaxPushBodyBytes,
		syncStatus: staticStatusProvider{status: dashboard.SyncStatus{
			Phase:         "degraded",
			ReasonCode:    constants.ReasonTransportFailed,
			ReasonMessage: "sync status provider is unavailable",
		}},
		listenAndServe: http.ListenAndServe,
	}
	if projectAuthorizer, ok := authSvc.(ProjectAuthorizer); ok {
		s.projectAuth = projectAuthorizer
	}
	for _, opt := range opts {
		opt(s)
	}
	s.routes()
	return s
}

func (s *CloudServer) Start() error {
	host := strings.TrimSpace(s.host)
	if host == "" {
		host = defaultHost
	}
	addr := fmt.Sprintf("%s:%d", host, s.port)
	log.Printf("[engram-cloud] listening on %s", addr)
	return s.listenAndServe(addr, s.Handler())
}

func (s *CloudServer) Handler() http.Handler {
	if s.mux == nil {
		s.routes()
	}
	return s.mux
}

func (s *CloudServer) pushBodyLimit() int64 {
	if s.maxPushBodyBytes > 0 {
		return s.maxPushBodyBytes
	}
	return defaultMaxPushBodyBytes
}

func (s *CloudServer) routes() {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /health", s.handleHealth)
	var dashboardStore dashboard.DashboardStore
	if store, ok := s.store.(dashboard.DashboardStore); ok {
		dashboardStore = store
	}
	validateLoginToken := func(token string) error {
		token = strings.TrimSpace(token)
		if token == "" {
			return fmt.Errorf("bearer token is required")
		}
		if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken != "" && hmac.Equal([]byte(token), []byte(adminToken)) {
			return nil
		}
		if s.auth == nil {
			return nil
		}
		req, _ := http.NewRequest(http.MethodGet, "/dashboard/login", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		_, err := s.auth.Authorize(req)
		return err
	}
	createSessionCookie := func(w http.ResponseWriter, r *http.Request, token string) error {
		sessionToken, err := s.dashboardSessionToken(token)
		if err != nil {
			return err
		}
		http.SetCookie(w, &http.Cookie{
			Name:     dashboardSessionCookieName,
			Value:    sessionToken,
			Path:     "/dashboard",
			HttpOnly: true,
			Secure:   dashboardCookieSecure(r),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int((8 * time.Hour).Seconds()),
		})
		return nil
	}
	// AutoLoginFromHeader closure: resolves the oauth2-proxy-injected identity into a bearer JWT.
	// Only wired when authLoader is set (the OAuth/header deployment path).
	// Returns ("", nil)  → header absent → token-paste fallback.
	// Returns ("", err)  → principal denied → 403 in handleLoginPage.
	// Returns (jwt, nil) → identity minted; handleLoginPage wraps it via CreateSessionCookie.
	var autoLoginFromHeader func(r *http.Request) (string, error)
	if s.authLoader != nil {
		autoLoginFromHeader = func(r *http.Request) (string, error) {
			email := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Email")))
			if email == "" {
				return "", nil // header absent → token-paste fallback
			}
			p, ok := s.authLoader.Lookup(email)
			if !ok || strings.EqualFold(p.Status, "removed") {
				return "", fmt.Errorf("auto-login denied for %q", email)
			}
			// Reuse the /auth endpoint's JWT minting path (auth_endpoint.go:100-106)
			// so there is exactly one identity-minting code path in the server.
			return auth.MintJWT(s.authJWTSecret, auth.JWTClaims{
				Sub:        p.Email,
				Email:      p.Email,
				Name:       p.Name,
				Department: p.Department,
				Role:       p.Role,
			}, time.Now().UTC(), s.jwtTTL)
		}
	}
	if s.auth == nil {
		validateLoginToken = nil
		createSessionCookie = nil
		autoLoginFromHeader = nil
	}
	dashboard.Mount(s.mux, dashboard.MountConfig{
		RequireSession:      s.authorizeDashboardRequest,
		ValidateLoginToken:  validateLoginToken,
		CreateSessionCookie: createSessionCookie,
		ClearSessionCookie: func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{
				Name:     dashboardSessionCookieName,
				Value:    "",
				Path:     "/dashboard",
				HttpOnly: true,
				Secure:   dashboardCookieSecure(r),
				SameSite: http.SameSiteLaxMode,
				MaxAge:   -1,
			})
		},
		AutoLoginFromHeader: autoLoginFromHeader,
		IsAdmin: func(r *http.Request) bool {
			return s.isDashboardAdmin(r)
		},
		// GetDisplayName: returns the JWT Name claim when the session carries a
		// verifiable JWT; falls back to "OPERATOR" for static admin token sessions.
		GetDisplayName: func(r *http.Request) string {
			if claims, ok := s.dashboardPrincipal(r); ok && strings.TrimSpace(claims.Name) != "" {
				return claims.Name
			}
			return "OPERATOR"
		},
		// GetUserEmail: returns the verified JWT email claim. Returns "" for static
		// admin token sessions (no JWT = no per-user scoping = treated as admin).
		// The email is derived from the verified JWT, NEVER from a request parameter.
		GetUserEmail: func(r *http.Request) string {
			if claims, ok := s.dashboardPrincipal(r); ok {
				return strings.ToLower(strings.TrimSpace(claims.Email))
			}
			return ""
		},
		// ServerVersion: cloud binary version for the dashboard indicator.
		// Set via WithServerVersion (wired from main.version in cloud.go). (REQ-VID-04)
		ServerVersion: s.serverVersion,
		// LatestVersion: TTL-cached GitHub latest-version fetcher.
		// Set lazily so a cold start does not block dashboard load. (REQ-VID-03)
		LatestVersion: version.LatestCached,
		// BrainURL: set from ENGRAM_BRAIN_URL env var. When non-empty, the Graph tab renders
		// an iframe pointing to the Brain service. When empty, a placeholder is shown. (D3)
		BrainURL: os.Getenv("ENGRAM_BRAIN_URL"),
		// D6: wire classrules fields for admin games-editing handlers.
		ListGames:          s.listGamesFunc(),
		ClassrulesReload:   s.classrulesReloadFn,
		ClassrulesFilePath: s.resolveClassrulesFilePath(),
		// Slice B: ListColors returns the current game and department color maps from the
		// in-memory classrules config. Nil when classrules is not configured.
		ListColors: s.listColorsFunc(),
		// Block A: WriteGameColor writes graph_colors.games[name] only.
		// Separated from dept colors (no longer uses isDeptKey disambiguation).
		WriteGameColor: s.writeGameColorFunc(),
		// Block A: WriteDeptColor writes graph_colors.departments[name] only.
		// Called by the new POST /dashboard/admin/departments/{name}/color route.
		WriteDeptColor: s.writeDeptColorFunc(),
		// Save/Delete: combined game list + color writes (POST /dashboard/admin/games/save and /delete).
		SaveGame:   s.saveGameFunc(),
		DeleteGame: s.deleteGameFunc(),
		// Departments editable table: canonical list + save/delete closures.
		ListDepartmentsCanonical: s.listDeptsFunc(),
		ListDeptEntriesCanonical: s.listDeptEntriesFunc(),
		SaveDept:                 s.saveDeptFunc(),
		DeleteDept:               s.deleteDeptFunc(),
		// Rules editor: ListRules returns the current cfg.Rules text; SaveRules persists
		// updates atomically via classrules.WriteRules + loader.Reload.
		ListRules: s.listRulesFunc(),
		SaveRules: s.saveRulesFunc(),
		// D4: ListProvisionedUsers — closure that sources the admin member list from
		// users.yaml (YAMLLoader.List) rather than cloud_chunks contributors. Only
		// wired when the authLoader implements the listableUserDirectory interface
		// (i.e. when ENGRAM_USERS_FILE is set and a YAMLLoader was constructed).
		ListProvisionedUsers: s.listProvisionedUsersFunc(),
		// D4: UserReload — invoke YAMLLoader.Reload after a successful write to users.yaml.
		UserReload: s.userReloadFn,
		// D4: UsersFilePath — absolute path to users.yaml inside the container.
		// Set via WithUsersFilePath option (default: ENGRAM_USERS_FILE env var).
		UsersFilePath:     s.resolveUsersFilePath(),
		Store:             dashboardStore,
		MaxLoginBodyBytes: maxDashboardLoginBodyBytes,
		StatusProvider:    s.syncStatus,
	})
	s.mux.HandleFunc("GET /sync/pull", s.withAuth(s.handlePullManifest))
	s.mux.HandleFunc("GET /sync/pull/{chunkID}", s.withAuth(s.handlePullChunk))
	s.mux.HandleFunc("POST /sync/push", s.withAuth(s.handlePushChunk))
	s.mux.HandleFunc("POST /sync/mutations/push", s.withAuth(s.handleMutationPush))
	s.mux.HandleFunc("GET /sync/mutations/pull", s.withAuth(s.handleMutationPull))
	// Self-service project enrollment: POST to enroll, DELETE to unenroll.
	// Auth enforced by withAuth; caller identity derived from Bearer JWT.
	s.mux.HandleFunc("POST /user/enrolled-projects", s.withAuth(s.handleSelfEnrollProject))
	s.mux.HandleFunc("DELETE /user/enrolled-projects", s.withAuth(s.handleSelfUnenrollProject))
	// /classrules/games: returns the canonical games vocabulary for per-session
	// sync by member CLIs. Bearer JWT auth (same token members use for sync).
	s.mux.HandleFunc("GET /classrules/games", s.withAuth(s.handleClassrulesGames))
	// /api/brain/identity: echoes the X-Forwarded-Email header injected by oauth2-proxy.
	// No withAuth wrapper — reachable by any authenticated viewer (same-origin brain iframe JS).
	// Must be registered OUTSIDE /brain/* so the dashboard brain static prefix does not shadow it.
	s.mux.HandleFunc("GET /api/brain/identity", s.handleBrainIdentity)
	// /auth endpoint: mint engram JWT from oauth2-proxy X-Forwarded-Email.
	// Only registered when WithAuthEndpoint option is applied (authLoader set).
	if s.authLoader != nil {
		s.registerAuthEndpoint()
	}
}

// ClientVersionRecorder is a structural interface implemented by stores that can
// persist the last-seen X-Engram-Client-Version per contributor. It is checked
// via structural assertion in withAuth so the ChunkStore interface stays stable.
// Satisfies: ADR-3 (optional store capability, not a hard interface extension).
type ClientVersionRecorder interface {
	RecordClientVersion(ctx context.Context, contributor, version string) error
}

func (s *CloudServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil {
			enriched, err := s.auth.Authorize(r)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			r = enriched
		}
		// Best-effort: record the client version after auth succeeds.
		// Fire-and-forget so version telemetry never blocks or fails a sync request.
		// Satisfies: REQ-CVR-05, REQ-CVR-06, ADR-2, ADR-3.
		if clientVer := strings.TrimSpace(r.Header.Get("X-Engram-Client-Version")); clientVer != "" {
			if cvr, ok := s.store.(ClientVersionRecorder); ok {
				var contributorEmail string
				if ap, ok := s.auth.(interface {
					Attribution(ctx context.Context) cloudstore.Attribution
				}); ok {
					contributorEmail = ap.Attribution(r.Context()).UserEmail
				}
				if contributorEmail != "" {
					go func() {
						_ = cvr.RecordClientVersion(r.Context(), contributorEmail, clientVer)
					}()
				}
			}
		}
		next(w, r)
	}
}

func (s *CloudServer) withAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil {
			enriched, err := s.auth.Authorize(r)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			r = enriched
		}
		next.ServeHTTP(w, r)
	})
}

// structuredAuthError is satisfied by typed auth errors that carry HTTP status
// and structured error codes (e.g. auth.AuthError). Using an interface avoids
// an import cycle: cloudserver does not import auth; auth defines its error type
// independently and cloudserver checks the interface via errors.As.
type structuredAuthError interface {
	error
	HTTPStatus() int
	ErrorCode() string
	ErrorMessage() string
}

// writeAuthError writes the appropriate HTTP response for an auth error.
// If err satisfies structuredAuthError, the typed status and JSON body are used.
// A plain (untyped) error results in HTTP 401 (backward-compatible default).
func writeAuthError(w http.ResponseWriter, err error) {
	var se structuredAuthError
	if errors.As(err, &se) {
		jsonResponse(w, se.HTTPStatus(), map[string]any{
			"error":   se.ErrorCode(),
			"message": se.ErrorMessage(),
		})
		return
	}
	http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
}

func (s *CloudServer) authorizeDashboardRequest(r *http.Request) error {
	if s.auth == nil {
		return nil
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return err
	}
	bearerToken, err := s.dashboardBearerToken(cookie.Value)
	if err != nil {
		return err
	}
	if strings.TrimSpace(bearerToken) == "" {
		return fmt.Errorf("dashboard session token is empty")
	}
	if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken != "" && hmac.Equal([]byte(bearerToken), []byte(adminToken)) {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	_, err = s.auth.Authorize(req)
	return err
}

func (s *CloudServer) dashboardSessionToken(bearerToken string) (string, error) {
	if codec, ok := s.auth.(dashboardSessionCodec); ok {
		return codec.MintDashboardSession(bearerToken)
	}
	return "", ErrDashboardSessionCodecRequired
}

func (s *CloudServer) dashboardBearerToken(sessionToken string) (string, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return "", fmt.Errorf("dashboard session token is empty")
	}
	if codec, ok := s.auth.(dashboardSessionCodec); ok {
		return codec.ParseDashboardSession(sessionToken)
	}
	return "", ErrDashboardSessionCodecRequired
}

func dashboardCookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	forwardedProto := r.Header.Get("X-Forwarded-Proto")
	for _, proto := range strings.Split(forwardedProto, ",") {
		if strings.EqualFold(strings.TrimSpace(proto), "https") {
			return true
		}
	}
	return false
}

func (s *CloudServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "service": "engram-cloud"})
}

// handleClassrulesGames handles GET /classrules/games.
// Returns the canonical games vocabulary as {"games": [...]} so member CLI
// instances can sync the list via `engram games sync` on session start.
// Auth: Bearer JWT (same token members use for sync endpoints).
// When classrulesCurrentGamesFn is nil or returns nil, responds with {"games": []}
// — this is not an error; classrules simply are not configured on this server.
func (s *CloudServer) handleClassrulesGames(w http.ResponseWriter, _ *http.Request) {
	games := []string{}
	if s.classrulesCurrentGamesFn != nil {
		if got := s.classrulesCurrentGamesFn(); got != nil {
			games = got
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"games": games})
}

// dashboardPrincipal decodes the request's session cookie and returns the JWT
// claims embedded in it. Returns (zero, false) when no valid session cookie is
// present or the session does not contain a verifiable JWT (e.g. static admin
// token session). This is a per-call decode — no context enrichment because
// RequireSession returns error (not *http.Request), so enrichment would be dropped.
func (s *CloudServer) dashboardPrincipal(r *http.Request) (auth.JWTClaims, bool) {
	if s.auth == nil {
		return auth.JWTClaims{}, false
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return auth.JWTClaims{}, false
	}
	bearer, err := s.dashboardBearerToken(cookie.Value)
	if err != nil || strings.TrimSpace(bearer) == "" {
		return auth.JWTClaims{}, false
	}
	claims, err := auth.VerifyJWT(s.authJWTSecret, bearer, time.Now().UTC())
	if err != nil {
		return auth.JWTClaims{}, false
	}
	return claims, true
}

func (s *CloudServer) isDashboardAdmin(r *http.Request) bool {
	if s.auth == nil {
		return false
	}
	// Static token path (first, backward compat): ENGRAM_CLOUD_TOKEN via cookie.
	adminToken := strings.TrimSpace(s.dashboardAdmin)
	if adminToken != "" {
		cookie, err := r.Cookie(dashboardSessionCookieName)
		if err == nil {
			token, err := s.dashboardBearerToken(cookie.Value)
			if err == nil && hmac.Equal([]byte(token), []byte(adminToken)) {
				return true
			}
		}
	}
	// JWT path: role:admin in verified JWT claims.
	if claims, ok := s.dashboardPrincipal(r); ok {
		if strings.EqualFold(strings.TrimSpace(claims.Role), "admin") {
			return true
		}
	}
	return false
}

func (s *CloudServer) handlePullManifest(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromRequest(w, r)
	if !ok {
		return
	}
	if !s.authorizeProjectScope(w, r, project) {
		return
	}
	manifest, err := s.store.ReadManifest(r.Context(), project)
	if err != nil {
		http.Error(w, fmt.Sprintf("read manifest: %v", err), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, manifest)
}

func (s *CloudServer) handlePullChunk(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromRequest(w, r)
	if !ok {
		return
	}
	if !s.authorizeProjectScope(w, r, project) {
		return
	}
	chunkID := strings.TrimSpace(r.PathValue("chunkID"))
	if chunkID == "" {
		http.Error(w, "chunkID is required", http.StatusBadRequest)
		return
	}
	chunk, err := s.store.ReadChunk(r.Context(), project, chunkID)
	if err != nil {
		if errors.Is(err, cloudstore.ErrChunkNotFound) {
			http.Error(w, fmt.Sprintf("read chunk: %v", err), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("read chunk: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(chunk)
}

func (s *CloudServer) handlePushChunk(w http.ResponseWriter, r *http.Request) {
	maxPushBodyBytes := s.pushBodyLimit()
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)
	var req struct {
		ChunkID         string          `json:"chunk_id"`
		CreatedBy       string          `json:"created_by"`
		ClientCreatedAt string          `json:"client_created_at"`
		Project         string          `json:"project"`
		Data            json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeActionableError(w, http.StatusRequestEntityTooLarge, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadTooLarge, fmt.Sprintf("push payload too large (max %d bytes)", maxPushBodyBytes))
			return
		}
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	if len(req.Data) == 0 {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "data is required")
		return
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = strings.TrimSpace(r.URL.Query().Get("project"))
	}
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return
	}
	if !s.authorizeProjectScope(w, r, project) {
		return
	}

	// Push-path pause guard: check project sync control before accepting the chunk.
	// Uses a structural interface assertion so the ChunkStore interface is NOT extended.
	// Satisfies REQ-109 / Design Decision 5.
	if storeForControls, ok := s.store.(interface {
		IsProjectSyncEnabled(project string) (bool, error)
	}); ok {
		enabled, err := storeForControls.IsProjectSyncEnabled(project)
		if err != nil {
			writeActionableError(w, http.StatusInternalServerError,
				constants.UpgradeErrorClassBlocked,
				constants.UpgradeErrorCodeInternal,
				fmt.Sprintf("check project control: %v", err))
			return
		}
		if !enabled {
			// REQ-405: emit audit entry for chunk-push pause-rejection before writing 409.
			// Structural type assertion — ChunkStore is NOT extended.
			contributor := strings.TrimSpace(req.CreatedBy)
			if contributor == "" {
				contributor = "unknown"
			}
			if auditor, ok := s.store.(interface {
				InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error
			}); ok {
				if aerr := auditor.InsertAuditEntry(r.Context(), cloudstore.AuditEntry{
					Contributor: contributor,
					Project:     project,
					Action:      cloudstore.AuditActionChunkPush,
					Outcome:     cloudstore.AuditOutcomeRejectedProjectPaused,
					ReasonCode:  "sync-paused",
				}); aerr != nil {
					log.Printf("cloudserver: audit insert failed (chunk push): %v", aerr)
				}
			} else {
				log.Printf("cloudserver: store (%T) does not implement InsertAuditEntry; audit skipped", s.store)
			}
			// JW4: include project envelope fields in 409 response, consistent
			// with the mutation push 409 envelope (REQ-414 parity for chunk path).
			jsonResponse(w, http.StatusConflict, map[string]any{
				"error_class":    strings.TrimSpace(constants.UpgradeErrorClassPolicy),
				"error_code":     "sync-paused",
				"error":          fmt.Sprintf("sync is paused for project %q", project),
				"project":        project,
				"project_source": engramproject.SourceRequestBody,
				"project_path":   "",
			})
			return
		}
	}

	normalizedData, err := coerceChunkProject(req.Data, project)
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	chunk, err := validateImportableChunkPayload(normalizedData)
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	knownSessionIDs, err := s.store.KnownSessionIDs(r.Context(), project)
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("validate push payload: %v", err))
		return
	}
	if err := validateChunkSessionReferences(chunk, knownSessionIDs); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}

	computedChunkID := chunkIDFromPayload(normalizedData)
	providedChunkID := strings.TrimSpace(req.ChunkID)
	if providedChunkID != "" && providedChunkID != computedChunkID {
		log.Printf("cloudserver: chunk_id mismatch for project %q: client=%q server=%q; accepting server-canonicalized payload", project, providedChunkID, computedChunkID)
	}
	clientCreatedAt := strings.TrimSpace(req.ClientCreatedAt)
	if clientCreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, clientCreatedAt); err != nil {
			writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "client_created_at must be RFC3339")
			return
		}
	}

	// Resolve server-side attribution from the authenticated identity (if any).
	// Uses the same AttributionProvider pattern as handleMutationPush so that
	// Gate B (personal-drop) and attribution stamping are applied on the chunk path.
	// ctx carries the principal placed there by withAuth → Authorize.
	var attr cloudstore.Attribution
	if ap, ok := s.auth.(interface {
		Attribution(ctx context.Context) cloudstore.Attribution
	}); ok {
		attr = ap.Attribution(r.Context())
	}

	// Use WriteChunkWithAttribution when the store implements it (structural assertion
	// keeps the ChunkStore interface stable — same pattern as the pause guard above).
	// Falls back to WriteChunk (zero Attribution = no Gate B, no stamping) for stores
	// that do not yet implement the method (e.g. test fakes).
	type chunkStoreWithAttribution interface {
		WriteChunkWithAttribution(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte, attr cloudstore.Attribution) error
	}
	var writeErr error
	if storeWithAttr, ok := s.store.(chunkStoreWithAttribution); ok {
		writeErr = storeWithAttr.WriteChunkWithAttribution(r.Context(), project, computedChunkID, req.CreatedBy, clientCreatedAt, normalizedData, attr)
	} else {
		log.Printf("cloudserver: store (%T) does not implement WriteChunkWithAttribution; chunk written WITHOUT Gate B or attribution stamping", s.store)
		writeErr = s.store.WriteChunk(r.Context(), project, computedChunkID, req.CreatedBy, clientCreatedAt, normalizedData)
	}
	if writeErr != nil {
		if errors.Is(writeErr, cloudstore.ErrChunkConflict) {
			writeActionableError(w, http.StatusConflict, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodeChunkConflict, fmt.Sprintf("write chunk: %v", writeErr))
			return
		}
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("write chunk: %v", writeErr))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "chunk_id": computedChunkID})
}

func chunkIDFromPayload(payload []byte) string {
	return chunkcodec.ChunkID(payload)
}

func projectFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return "", false
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return "", false
	}
	return project, true
}

func (s *CloudServer) authorizeProjectScope(w http.ResponseWriter, r *http.Request, project string) bool {
	if s.projectAuth == nil {
		return true
	}
	if err := s.projectAuth.AuthorizeProject(r.Context(), project); err != nil {
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: project is not allowed")
		return false
	}
	return true
}

func writeActionableError(w http.ResponseWriter, status int, class, code, message string) {
	jsonResponse(w, status, map[string]any{
		"error_class": strings.TrimSpace(class),
		"error_code":  strings.TrimSpace(code),
		"error":       strings.TrimSpace(message),
	})
}

func coerceChunkProject(payload []byte, project string) ([]byte, error) {
	return chunkcodec.CanonicalizeForProject(payload, project)
}

func decodeSyncMutationPayload(payload string, dest any) error {
	return chunkcodec.DecodeSyncMutationPayload(payload, dest)
}

func validateImportableChunkPayload(payload []byte) (engramsync.ChunkData, error) {
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return engramsync.ChunkData{}, fmt.Errorf("chunk schema: %w", err)
	}
	if err := validateDirectChunkArrayEntries(chunk); err != nil {
		return engramsync.ChunkData{}, err
	}
	return chunk, nil

}

func validateDirectChunkArrayEntries(chunk engramsync.ChunkData) error {
	for i, session := range chunk.Sessions {
		if strings.TrimSpace(session.ID) == "" {
			return fmt.Errorf("sessions[%d].id is required", i)
		}
		if strings.TrimSpace(session.Directory) == "" {
			return fmt.Errorf("sessions[%d].directory is required", i)
		}
	}

	for i, observation := range chunk.Observations {
		if strings.TrimSpace(observation.SyncID) == "" {
			return fmt.Errorf("observations[%d].sync_id is required", i)
		}
		if strings.TrimSpace(observation.SessionID) == "" {
			return fmt.Errorf("observations[%d].session_id is required", i)
		}
		if strings.TrimSpace(observation.Type) == "" {
			return fmt.Errorf("observations[%d].type is required", i)
		}
		if strings.TrimSpace(observation.Title) == "" {
			return fmt.Errorf("observations[%d].title is required", i)
		}
		if strings.TrimSpace(observation.Content) == "" {
			return fmt.Errorf("observations[%d].content is required", i)
		}
		if strings.TrimSpace(observation.Scope) == "" {
			return fmt.Errorf("observations[%d].scope is required", i)
		}
	}

	for i, prompt := range chunk.Prompts {
		if strings.TrimSpace(prompt.SyncID) == "" {
			return fmt.Errorf("prompts[%d].sync_id is required", i)
		}
		if strings.TrimSpace(prompt.SessionID) == "" {
			return fmt.Errorf("prompts[%d].session_id is required", i)
		}
		if strings.TrimSpace(prompt.Content) == "" {
			return fmt.Errorf("prompts[%d].content is required", i)
		}
	}

	return nil
}

func validateChunkSessionReferences(chunk engramsync.ChunkData, knownSessionIDs map[string]struct{}) error {
	chunkSessionIDs := make(map[string]struct{}, len(chunk.Sessions))
	for i, session := range chunk.Sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			return fmt.Errorf("sessions[%d].id is required", i)
		}
		chunkSessionIDs[sessionID] = struct{}{}
	}
	for i, mutation := range chunk.Mutations {
		if mutation.Entity != store.SyncEntitySession || mutation.Op != store.SyncOpUpsert {
			continue
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return fmt.Errorf("mutations[%d] invalid payload: %w", i, err)
		}
		sessionID := strings.TrimSpace(body.ID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(mutation.EntityKey)
		}
		if sessionID == "" {
			return fmt.Errorf("mutations[%d].payload.id is required for session upsert", i)
		}
		chunkSessionIDs[sessionID] = struct{}{}
	}

	hasSession := func(sessionID string) bool {
		if _, ok := chunkSessionIDs[sessionID]; ok {
			return true
		}
		_, ok := knownSessionIDs[sessionID]
		return ok
	}

	for i, observation := range chunk.Observations {
		sessionID := strings.TrimSpace(observation.SessionID)
		if sessionID == "" {
			return fmt.Errorf("observations[%d].session_id is required", i)
		}
		if !hasSession(sessionID) {
			return fmt.Errorf("observations[%d] references missing session_id %q", i, sessionID)
		}
	}

	for i, prompt := range chunk.Prompts {
		sessionID := strings.TrimSpace(prompt.SessionID)
		if sessionID == "" {
			return fmt.Errorf("prompts[%d].session_id is required", i)
		}
		if !hasSession(sessionID) {
			return fmt.Errorf("prompts[%d] references missing session_id %q", i, sessionID)
		}
	}

	for i, mutation := range chunk.Mutations {
		if mutation.Entity != store.SyncEntityObservation && mutation.Entity != store.SyncEntityPrompt {
			continue
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := decodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return fmt.Errorf("mutations[%d] invalid payload: %w", i, err)
		}
		sessionID := strings.TrimSpace(body.SessionID)
		if mutation.Op == store.SyncOpUpsert && sessionID == "" {
			return fmt.Errorf("mutations[%d].payload.session_id is required for upsert", i)
		}
		if mutation.Op == store.SyncOpUpsert && !hasSession(sessionID) {
			return fmt.Errorf("mutations[%d] references missing session_id %q", i, sessionID)
		}
	}
	return nil
}

func jsonResponse(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
