package dashboard

//go:generate go tool templ generate

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/a-h/templ"
)

type SyncStatus struct {
	Phase                string
	ReasonCode           string
	ReasonMessage        string
	UpgradeStage         string
	UpgradeReasonCode    string
	UpgradeReasonMessage string
}

type SyncStatusProvider interface {
	Status() SyncStatus
}

type staticSyncStatusProvider struct {
	status SyncStatus
}

func (s staticSyncStatusProvider) Status() SyncStatus { return s.status }

type MountConfig struct {
	RequireSession      func(r *http.Request) error
	ValidateLoginToken  func(token string) error
	CreateSessionCookie func(w http.ResponseWriter, r *http.Request, token string) error
	ClearSessionCookie  func(w http.ResponseWriter, r *http.Request)
	IsAdmin             func(r *http.Request) bool
	GetDisplayName      func(r *http.Request) string
	// GetUserEmail returns the verified email address from the JWT email claim.
	// Returns "" when no JWT session is present (e.g. static admin token).
	// The email is NEVER derived from a client-supplied request param — it comes
	// from the verified JWT decoded by the session codec (D2: server-derived identity).
	GetUserEmail func(r *http.Request) string
	// AutoLoginFromHeader resolves the proxy-injected identity into a bearer JWT.
	// Returns ("", nil)  → no header present (render token-paste fallback).
	// Returns ("", err)  → header present but principal denied (403, no cookie).
	// Returns (jwt, nil) → mint succeeded; handleLoginPage wraps it via CreateSessionCookie.
	// nil means auto-login is disabled (token-paste only).
	AutoLoginFromHeader func(r *http.Request) (string, error)
	// BrainURL is the base URL of the Engram Brain service rendered in the Graph tab iframe.
	// When empty, the Graph tab shows a "Graph coming soon" placeholder. Set from ENGRAM_BRAIN_URL env. (D3)
	BrainURL string
	// ListProvisionedUsers returns the current snapshot of provisioned users from users.yaml.
	// Used by admin member-management routes to source the member list (D4).
	// nil when users.yaml is not configured (e.g. legacy bearer-token deployments).
	ListProvisionedUsers func() []ProvisionedUser
	// UserReload triggers an in-process reload of the user directory after an atomic write to users.yaml.
	// Non-fatal on failure — the write has already succeeded; reload errors are logged by the handler.
	UserReload func() error
	// UsersFilePath is the absolute path to users.yaml inside the container.
	// Required for atomic write + local git commit in admin member-management handlers (D4).
	UsersFilePath string
	// ListGames returns the current games vocabulary from the cloud's in-memory
	// ClassrulesLoader. Used by the admin games-editing UI to display the current list.
	// Returns nil when ENGRAM_CLASSIFICATION_RULES is not configured. (D6)
	ListGames func() []string
	// ClassrulesReload triggers an in-process reload of the cloud's classrules Config
	// after a successful atomic write to classification-rules.yaml. Non-fatal on failure. (D6)
	ClassrulesReload func() error
	// ClassrulesFilePath is the absolute path to classification-rules.yaml inside the
	// container. Required for atomic write + local git commit in admin games handlers (D6).
	ClassrulesFilePath string
	Store              DashboardStore
	MaxLoginBodyBytes   int64
	StatusProvider      SyncStatusProvider
	// WriteGameColor is called when an admin saves a game color via
	// POST /dashboard/admin/games/{name}/color. It receives the game slug and
	// validated hex color string. When nil, the route returns 501 Not Implemented.
	WriteGameColor func(game, color string) error
	// WriteDeptColor is called when an admin saves a department color via
	// POST /dashboard/admin/departments/{name}/color. It receives the dept key and
	// validated hex color string. When nil, the route returns 501 Not Implemented.
	WriteDeptColor func(dept, color string) error
	// ListColors returns the current game-color map and department-color map from
	// the in-memory classrules config. Used by handleAdminGames and handleAdminDepartments
	// to populate color pickers. When nil (classrules not configured), both maps are nil.
	ListColors func() (gameColors map[string]string, deptColors map[string]string)
	// SaveGame is called by POST /dashboard/admin/games/save.
	// It receives the complete new games list (with rename/add already applied) and
	// the complete new game-color map so both are written atomically.
	// When nil, the route returns 501 Not Implemented.
	SaveGame func(newGames []string, newGameColors map[string]string) error
	// DeleteGame is called by POST /dashboard/admin/games/delete.
	// It receives the complete new games list (with the deleted game removed) and
	// the complete new game-color map (with the deleted game's color removed).
	// When nil, the route returns 501 Not Implemented.
	DeleteGame func(newGames []string, newGameColors map[string]string) error
	// ListDepartmentsCanonical returns the current department names from the canonical
	// classification-rules.yaml departments list (not users.yaml). Used by the admin
	// departments editable table GET handler.
	// When nil (classrules not configured), returns nil.
	ListDepartmentsCanonical func() []string
	// ListDeptEntriesCanonical returns the full department entries (name + aliases)
	// from the canonical classification-rules.yaml departments list. Used by save/delete
	// handlers to preserve aliases through rename operations.
	// When nil, the handlers fall back to names-only (aliases lost on rename).
	ListDeptEntriesCanonical func() []DeptEntry
	// SaveDept is called by POST /dashboard/admin/departments/save.
	// It receives the complete new departments list (with rename/add already applied) and
	// the complete new dept-color map so both are written atomically.
	// When nil, the route returns 501 Not Implemented.
	SaveDept func(newDepts []DeptEntry, newDeptColors map[string]string) error
	// DeleteDept is called by POST /dashboard/admin/departments/delete.
	// It receives the complete new departments list (with the deleted dept removed) and
	// the complete new dept-color map (with the deleted dept's color removed).
	// When nil, the route returns 501 Not Implemented.
	DeleteDept func(newDepts []DeptEntry, newDeptColors map[string]string) error
	// ListRules returns the current free-form classification rules text from the
	// in-memory ClassrulesLoader. Used by GET /dashboard/admin/rules to pre-fill
	// the textarea with the current value. Returns "" when classrules is not configured.
	ListRules func() string
	// SaveRules persists updated free-form rules text via classrules.WriteRules.
	// Called by POST /dashboard/admin/rules with the submitted rules form field.
	// When nil, the route returns 501 Not Implemented.
	SaveRules func(rules string) error
}

// DeptEntry is the dashboard-layer representation of a canonical department entry.
// Name is the canonical department name. Aliases preserves the original aliases
// from classification-rules.yaml so they survive a rename or color-only update.
type DeptEntry struct {
	Name    string
	Aliases []string
}

type DashboardStore interface {
	// Existing methods (from cloud-dashboard-parity).
	ListProjects(query string) ([]cloudstore.DashboardProjectRow, error)
	ProjectDetail(project string) (cloudstore.DashboardProjectDetail, error)
	ListContributors(query string) ([]cloudstore.DashboardContributorRow, error)
	ListRecentSessions(project string, query string, limit int) ([]cloudstore.DashboardSessionRow, error)
	ListRecentObservations(project string, query string, limit int) ([]cloudstore.DashboardObservationRow, error)
	ListRecentPrompts(project string, query string, limit int) ([]cloudstore.DashboardPromptRow, error)
	AdminOverview() (cloudstore.DashboardAdminOverview, error)
	// ScopedMemberOverview returns stats tailored to the caller's ReadScope.
	// Admin → team-wide overview; member → own obs/sessions/prompts counts; missing identity → error.
	ScopedMemberOverview(scope *cloudstore.ReadScope) (cloudstore.DashboardAdminOverview, error)

	// Paginated list methods (from cloud-dashboard-visual-parity).
	// All paginated + detail methods accept a *cloudstore.ReadScope as the first parameter (D2).
	ListProjectsPaginated(query string, limit, offset int) ([]cloudstore.DashboardProjectRow, int, error)
	ListRecentObservationsPaginated(scope *cloudstore.ReadScope, project, query, obsType string, limit, offset int) ([]cloudstore.DashboardObservationRow, int, error)
	ListRecentSessionsPaginated(scope *cloudstore.ReadScope, project, query string, limit, offset int) ([]cloudstore.DashboardSessionRow, int, error)
	ListRecentPromptsPaginated(scope *cloudstore.ReadScope, project, query string, limit, offset int) ([]cloudstore.DashboardPromptRow, int, error)
	ListContributorsPaginated(scope *cloudstore.ReadScope, query string, limit, offset int) ([]cloudstore.DashboardContributorRow, int, error)

	// Detail methods.
	GetSessionDetail(scope *cloudstore.ReadScope, project, sessionID string) (cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error)
	GetObservationDetail(scope *cloudstore.ReadScope, project, sessionID, syncID string) (cloudstore.DashboardObservationRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, error)
	GetPromptDetail(scope *cloudstore.ReadScope, project, sessionID, syncID string) (cloudstore.DashboardPromptRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardPromptRow, error)

	// GetObservationBySyncID looks up an observation by syncID alone (no project or
	// sessionID required). Used by handleRequestRemoval to verify ownership before
	// submitting a deletion request. scope enforces visibility: admin sees all,
	// member sees only own (ErrDashboardObservationNotFound if not owner or not found).
	GetObservationBySyncID(scope *cloudstore.ReadScope, syncID string) (cloudstore.DashboardObservationRow, error)

	// SystemHealth.
	SystemHealth() (cloudstore.DashboardSystemHealth, error)

	// Sync control methods.
	ListProjectSyncControls() ([]cloudstore.ProjectSyncControl, error)
	GetProjectSyncControl(project string) (*cloudstore.ProjectSyncControl, error)
	SetProjectSyncEnabled(project string, enabled bool, updatedBy, reason string) error
	IsProjectSyncEnabled(project string) (bool, error)

	// Batch 6: Connected navigation methods.
	GetContributorDetail(name string) (cloudstore.DashboardContributorRow, []cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error)
	ListDistinctTypes() ([]string, error)

	// Audit log (REQ-409).
	ListAuditEntriesPaginated(ctx context.Context, filter cloudstore.AuditFilter, limit, offset int) ([]cloudstore.DashboardAuditRow, int, error)

	// D5: Per-observation deletion requests.
	// Members submit requests; admins review, accept, or reject them.
	// Accept hard-deletes exactly the targeted observation via the mutation journal.
	CreateDeletionRequest(ctx context.Context, req cloudstore.DeletionRequest) (int64, error)
	GetDeletionRequest(ctx context.Context, id int64) (cloudstore.StoredDeletionRequest, error)
	ListPendingDeletionRequests(ctx context.Context) ([]cloudstore.StoredDeletionRequest, error)
	ListDeletionRequestsForRequester(ctx context.Context, email string) ([]cloudstore.StoredDeletionRequest, error)
	AcceptDeletionRequest(ctx context.Context, id int64, adminEmail string) error
	RejectDeletionRequest(ctx context.Context, id int64, adminEmail string) error
	PendingDeletionRequestCount(ctx context.Context) (int, error)
}

type handlers struct {
	cfg MountConfig
}

// overlayFS serves a file from primary first, falling back to fallback on any
// error. Powers the live-editable dashboard static override
// (ENGRAM_DASHBOARD_STATIC_DIR) so styles.css can be tweaked without a rebuild.
type overlayFS struct {
	primary  http.FileSystem
	fallback http.FileSystem
}

func (o overlayFS) Open(name string) (http.File, error) {
	if f, err := o.primary.Open(name); err == nil {
		return f, nil
	}
	return o.fallback.Open(name)
}

func Mount(mux *http.ServeMux, cfg MountConfig) {
	h := &handlers{cfg: cfg}

	staticSub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		log.Fatalf("dashboard: failed to create static sub FS: %v", err)
	}
	// Live-editable override: when ENGRAM_DASHBOARD_STATIC_DIR points at a directory,
	// files found there are served first; anything missing falls back to the embed.
	var staticFS http.FileSystem = http.FS(staticSub)
	if dir := strings.TrimSpace(os.Getenv("ENGRAM_DASHBOARD_STATIC_DIR")); dir != "" {
		staticFS = overlayFS{primary: http.Dir(dir), fallback: staticFS}
	}
	mux.Handle("GET /dashboard/static/", http.StripPrefix("/dashboard/static/", http.FileServer(staticFS)))

	mux.HandleFunc("GET /dashboard/health", h.handleHealth)
	mux.HandleFunc("GET /dashboard/login", h.handleLoginPage)
	mux.HandleFunc("POST /dashboard/login", h.handleLoginSubmit)
	mux.HandleFunc("POST /dashboard/logout", h.handleLogout)
	mux.HandleFunc("GET /dashboard/sync-status", h.handleSyncStatus)

	mux.HandleFunc("GET /dashboard", h.requireSession(h.handleDashboardHome))
	mux.HandleFunc("GET /dashboard/", h.requireSession(h.handleDashboardHome))
	// S6: Brain tab — iframe to Brain when ENGRAM_BRAIN_URL is set; placeholder otherwise.
	// /dashboard/graph is kept as a permanent redirect to avoid broken bookmarks.
	mux.HandleFunc("GET /dashboard/brain", h.requireSession(h.handleBrain))
	mux.HandleFunc("GET /dashboard/graph", h.requireSession(h.handleGraphRedirect))
	mux.HandleFunc("GET /dashboard/stats", h.requireSession(h.handleDashboardStats))
	mux.HandleFunc("GET /dashboard/activity", h.requireSession(h.handleDashboardActivity))
	mux.HandleFunc("GET /dashboard/browser", h.requireSession(h.handleBrowser))
	mux.HandleFunc("GET /dashboard/browser/observations", h.requireSession(h.handleBrowserObservations))
	mux.HandleFunc("GET /dashboard/browser/sessions", h.requireSession(h.handleBrowserSessions))
	mux.HandleFunc("GET /dashboard/browser/sessions/{sessionID}", h.requireSession(h.handleBrowserSessionDetail))
	mux.HandleFunc("GET /dashboard/browser/prompts", h.requireSession(h.handleBrowserPrompts))
	mux.HandleFunc("GET /dashboard/projects", h.requireSession(h.handleProjects))
	mux.HandleFunc("GET /dashboard/projects/{project}", h.requireSession(h.handleProjectDetail))
	mux.HandleFunc("GET /dashboard/contributors", h.requireSession(h.handleContributors))
	mux.HandleFunc("GET /dashboard/contributors/list", h.requireSession(h.handleContributorsList))
	mux.HandleFunc("GET /dashboard/contributors/{contributor}", h.requireSession(h.handleContributorDetail))
	mux.HandleFunc("GET /dashboard/admin", h.requireSession(h.handleAdmin))
	mux.HandleFunc("GET /dashboard/admin/projects", h.requireSession(h.handleAdminProjectControls))
	// R4-10: /dashboard/admin/contributors was a dead route (duplicate of /dashboard/contributors
	// behind an extra admin gate). Removed to avoid confusion.

	// 11 new routes — visual parity + composite-ID detail pages (REQ-106, Design Decision 3).
	mux.HandleFunc("GET /dashboard/projects/list", h.requireSession(h.handleProjectsList))
	mux.HandleFunc("GET /dashboard/projects/{name}/observations", h.requireSession(h.handleProjectObservationsPartial))
	mux.HandleFunc("GET /dashboard/projects/{name}/sessions", h.requireSession(h.handleProjectSessionsPartial))
	mux.HandleFunc("GET /dashboard/projects/{name}/prompts", h.requireSession(h.handleProjectPromptsPartial))
	mux.HandleFunc("GET /dashboard/admin/users", h.requireSession(h.handleAdminUsers))
	mux.HandleFunc("GET /dashboard/admin/users/list", h.requireSession(h.handleAdminUsersList))
	// Users management write actions — delegate to the same member handlers (D4 write path).
	// These are the canonical write endpoints surfaced by the Users sub-nav page.
	mux.HandleFunc("POST /dashboard/admin/users/add", h.requireSession(h.handleAdminMembersAdd))
	mux.HandleFunc("POST /dashboard/admin/users/edit", h.requireSession(h.handleAdminMembersEdit))
	mux.HandleFunc("POST /dashboard/admin/users/deactivate", h.requireSession(h.handleAdminMembersDeactivate))
	mux.HandleFunc("POST /dashboard/admin/users/delete", h.requireSession(h.handleAdminUsersDelete))
	mux.HandleFunc("GET /dashboard/admin/health", h.requireSession(h.handleAdminHealth))
	mux.HandleFunc("POST /dashboard/admin/projects/{name}/sync", h.requireSession(h.handleAdminSyncTogglePost))
	mux.HandleFunc("GET /dashboard/admin/projects/{name}/sync/form", h.requireSession(h.handleAdminSyncToggleForm))
	mux.HandleFunc("GET /dashboard/sessions/{project}/{sessionID}", h.requireSession(h.handleSessionDetail))
	mux.HandleFunc("GET /dashboard/observations/{project}/{sessionID}/{syncID}", h.requireSession(h.handleObservationDetail))
	mux.HandleFunc("GET /dashboard/prompts/{project}/{sessionID}/{syncID}", h.requireSession(h.handlePromptDetail))

	// Audit log routes — admin-gated (REQ-408, REQ-409).
	mux.HandleFunc("GET /dashboard/admin/audit-log", h.requireSession(h.handleAdminAuditLog))
	mux.HandleFunc("GET /dashboard/admin/audit-log/list", h.requireSession(h.handleAdminAuditLogList))

	// D4: Admin member-management routes — admin-gated.
	// Source: users.yaml (ListProvisionedUsers), not cloud_chunks contributors.
	mux.HandleFunc("GET /dashboard/admin/members", h.requireSession(h.handleAdminMembers))
	mux.HandleFunc("POST /dashboard/admin/members/add", h.requireSession(h.handleAdminMembersAdd))
	mux.HandleFunc("POST /dashboard/admin/members/edit", h.requireSession(h.handleAdminMembersEdit))
	mux.HandleFunc("POST /dashboard/admin/members/deactivate", h.requireSession(h.handleAdminMembersDeactivate))

	// D5: Per-observation deletion-request routes.
	// Member route: POST request-removal (member-only — admin gets 403).
	// Admin routes: review page (GET) + accept/reject actions (POST, admin-only).
	// Guiding principle: hard deletion is the RARE EXCEPTION — not the normal path.
	mux.HandleFunc("POST /dashboard/browser/observations/{syncID}/request-removal", h.requireSession(h.handleRequestRemoval))
	mux.HandleFunc("GET /dashboard/admin/deletion-requests", h.requireSession(h.handleAdminDeletionRequests))
	mux.HandleFunc("POST /dashboard/admin/deletion-requests/accept", h.requireSession(h.handleAdminDeletionRequestAccept))
	mux.HandleFunc("POST /dashboard/admin/deletion-requests/reject", h.requireSession(h.handleAdminDeletionRequestReject))

	// D6: Admin games-editing routes — admin-gated.
	// GET  /dashboard/admin/games         → show editable games table (one row per game).
	// POST /dashboard/admin/games/save    → save/rename/add a game row (name + color, atomic).
	// POST /dashboard/admin/games/delete  → delete a game row (removes list entry + color).
	// POST /dashboard/admin/games         → legacy textarea bulk-update (kept for backwards compat).
	mux.HandleFunc("GET /dashboard/admin/games", h.requireSession(h.handleAdminGames))
	mux.HandleFunc("POST /dashboard/admin/games/save", h.requireSession(h.handleAdminGameSavePost))
	mux.HandleFunc("POST /dashboard/admin/games/delete", h.requireSession(h.handleAdminGameDeletePost))
	mux.HandleFunc("POST /dashboard/admin/games", h.requireSession(h.handleAdminGamesPost))

	// Block A: per-game color route (kept for backwards compat; save handler supersedes it).
	// POST /dashboard/admin/games/{name}/color       → write graph_colors.games[name]
	// POST /dashboard/admin/departments/{name}/color → write graph_colors.departments[name] (legacy, kept for compat)
	mux.HandleFunc("POST /dashboard/admin/games/{name}/color", h.requireSession(h.handleAdminGameColorPost))
	mux.HandleFunc("POST /dashboard/admin/departments/{name}/color", h.requireSession(h.handleAdminDeptColorPost))

	// Departments editable table routes (mirrors games routes).
	// GET  /dashboard/admin/departments         → show editable departments table.
	// POST /dashboard/admin/departments/save    → save/rename/add a dept row (name + color, atomic).
	// POST /dashboard/admin/departments/delete  → delete a dept row (removes list entry + color).
	mux.HandleFunc("GET /dashboard/admin/departments", h.requireSession(h.handleAdminDepartments))
	mux.HandleFunc("POST /dashboard/admin/departments/save", h.requireSession(h.handleAdminDeptSavePost))
	mux.HandleFunc("POST /dashboard/admin/departments/delete", h.requireSession(h.handleAdminDeptDeletePost))

	// Rules editor — admin-gated.
	// GET  /dashboard/admin/rules → show textarea pre-filled with cfg.Rules markdown.
	// POST /dashboard/admin/rules → save new rules text and redirect 303 with flash.
	mux.HandleFunc("GET /dashboard/admin/rules", h.requireSession(h.handleAdminRules))
	mux.HandleFunc("POST /dashboard/admin/rules", h.requireSession(h.handleAdminRulesPost))
}

func Handler() http.Handler {
	return HandlerWithStatus(staticSyncStatusProvider{status: SyncStatus{Phase: "idle"}})
}

func HandlerWithStatus(provider SyncStatusProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		status := provider.Status()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderSyncStatusPage(status)))
	})
	return mux
}

func renderSyncStatusPage(status SyncStatus) string {
	code := status.ReasonCode
	message := status.ReasonMessage
	headline := reasonHeadline(status.ReasonCode)
	phase := status.Phase
	phase = html.EscapeString(phase)
	headline = html.EscapeString(headline)
	code = html.EscapeString(code)
	message = html.EscapeString(message)

	return fmt.Sprintf(`<html>
<head><title>Engram Cloud Dashboard</title></head>
<body>
  <main>
    <h1>Engram Cloud Dashboard</h1>
    <p>phase: %s</p>
    <section>
      <h2>%s</h2>
      <p>reason_code: %s</p>
      <p>reason_message: %s</p>
      <p>upgrade_stage: %s</p>
      <p>upgrade_reason_code: %s</p>
      <p>upgrade_reason_message: %s</p>
    </section>
  </main>
</body>
</html>`, phase, headline, code, message, html.EscapeString(status.UpgradeStage), html.EscapeString(status.UpgradeReasonCode), html.EscapeString(status.UpgradeReasonMessage))
}

func reasonHeadline(code string) string {
	switch code {
	case constants.ReasonBlockedUnenrolled:
		return "Blocked — project unenrolled"
	case constants.ReasonPaused:
		return "Paused"
	case constants.ReasonAuthRequired:
		return "Authentication required"
	case constants.ReasonTransportFailed:
		return "Transport failure"
	default:
		if code == "" {
			return "Healthy"
		}
		return "Sync issue"
	}
}

func (h *handlers) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","subsystem":"dashboard"}`))
}

// renderComponent renders a templ component to the HTTP response.
func renderComponent(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("dashboard: templ render error: %v", err)
	}
}

// renderComponentStatus renders a templ component with a specific HTTP status code.
func renderComponentStatus(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("dashboard: templ render error: %v", err)
	}
}

func (h *handlers) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	next := sanitizeDashboardNext(r.URL.Query().Get("next"))
	// Existing valid-session bypass: if the caller already has a valid session, redirect.
	if h.cfg.RequireSession != nil {
		if err := h.cfg.RequireSession(r); err == nil {
			http.Redirect(w, r, h.dashboardPostLoginPathFor(next), http.StatusSeeOther)
			return
		}
	}
	// Auto-login from proxy-injected identity header (D2).
	// AutoLoginFromHeader is nil when not wired (e.g. token-paste-only deployments).
	if h.cfg.AutoLoginFromHeader != nil {
		jwt, err := h.cfg.AutoLoginFromHeader(r)
		if err != nil {
			// Header present but principal denied (removed, not provisioned).
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if jwt != "" {
			// Mint session cookie via the existing closure and redirect.
			if h.cfg.CreateSessionCookie != nil {
				if err := h.cfg.CreateSessionCookie(w, r, jwt); err != nil {
					http.Error(w, "unable to create dashboard session", http.StatusInternalServerError)
					return
				}
			}
			http.Redirect(w, r, h.dashboardPostLoginPathFor(next), http.StatusSeeOther)
			return
		}
		// jwt == "" → header absent → fall through to token-paste fallback form.
	}
	renderComponent(w, r, LoginPage("", next))
}

func (h *handlers) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if h.cfg.MaxLoginBodyBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxLoginBodyBytes)
	}
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, fmt.Sprintf("login payload too large (max %d bytes)", h.cfg.MaxLoginBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid form payload", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.PostForm.Get("token"))
	next := sanitizeDashboardNext(r.PostForm.Get("next"))
	if next == "" {
		next = sanitizeDashboardNext(r.URL.Query().Get("next"))
	}
	if h.cfg.RequireSession != nil {
		if err := h.cfg.RequireSession(r); err == nil {
			http.Redirect(w, r, h.dashboardPostLoginPathFor(next), http.StatusSeeOther)
			return
		}
	}
	if token == "" {
		renderComponent(w, r, LoginPage("token is required", next))
		return
	}
	if h.cfg.ValidateLoginToken != nil {
		if err := h.cfg.ValidateLoginToken(token); err != nil {
			renderComponent(w, r, LoginPage("invalid token", next))
			return
		}
	}
	if h.cfg.CreateSessionCookie != nil {
		if err := h.cfg.CreateSessionCookie(w, r, token); err != nil {
			http.Error(w, "unable to create dashboard session", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, h.dashboardPostLoginPathFor(next), http.StatusSeeOther)
}

func (h *handlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ClearSessionCookie != nil {
		h.cfg.ClearSessionCookie(w, r)
	}
	// Clearing only the engram session is not enough: oauth2-proxy keeps its own
	// Google session and re-injects X-Forwarded-Email on the next request, which
	// AutoLoginFromHeader would immediately turn back into a session. Redirect to
	// the proxy's sign-out so its cookie is cleared too — otherwise logout no-ops.
	http.Redirect(w, r, "/oauth2/sign_out?rd=%2Fdashboard%2Flogin", http.StatusSeeOther)
}

// handleSyncStatus renders the live cloud-sync status pill for the current user.
// It reflects how recently the caller last pushed a chunk (their own data), so a
// stale/expired CLI token surfaces as a visible warning instead of a permanently
// green "CLOUD ACTIVE" label. Loaded via htmx from the header.
func (h *handlers) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	email := ""
	if h.cfg.GetUserEmail != nil {
		email = h.cfg.GetUserEmail(r)
	}
	lastAt := ""
	if h.cfg.Store != nil && email != "" {
		if contribs, err := h.cfg.Store.ListContributors(""); err == nil {
			for _, c := range contribs {
				if strings.EqualFold(c.CreatedBy, email) {
					lastAt = c.LastChunkAt
					break
				}
			}
		}
	}
	cls, label, title := syncStatusFor(lastAt)
	renderComponent(w, r, SyncStatusPill(cls, label, title))
}

// syncStatusFor maps the caller's last chunk-push time to a pill (class, label,
// tooltip). The dashboard can't directly observe an expired CLI JWT (that auth is
// separate from the browser's oauth2-proxy session), but it can show sync recency
// so a silently-dead sync becomes obvious.
func syncStatusFor(lastChunkAt string) (cssClass, label, title string) {
	s := strings.TrimSpace(lastChunkAt)
	if s == "" {
		return "sync-off", "NOT SYNCING", "No cloud sync recorded for your account. Run `engram login` in your terminal to authenticate and sync."
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05.999999-07", s)
	}
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05", s)
	}
	if err != nil {
		return "sync-off", "SYNC UNKNOWN", "Could not determine your last sync time."
	}
	age := time.Since(t)
	rel := humanizeAge(age)
	switch {
	case age < 6*time.Hour:
		return "sync-active", "CLOUD ACTIVE", "Synced " + rel + "."
	case age < 48*time.Hour:
		return "sync-stale", "SYNC STALE · " + rel, "Last synced " + rel + ". If you've worked since, run `engram login` to re-authenticate."
	default:
		return "sync-off", "NOT SYNCING · " + rel, "Last synced " + rel + ". Run `engram login` in your terminal to re-authenticate and resume cloud sync."
	}
}

func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (h *handlers) handleDashboardHome(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if isHTMXRequest(r) {
		renderComponent(w, r, DashboardHome(p.DisplayName()))
		return
	}
	renderComponent(w, r, Layout("Dashboard", p.DisplayName(), "dashboard", p.IsAdmin(), DashboardHome(p.DisplayName())))
}

// handleBrain serves the Brain tab (S6, formerly handleGraph/D3).
// When MountConfig.BrainURL is non-empty and starts with http:// or https://,
// renders an iframe pointing to the Brain service filling the viewport below the nav bar.
// Any other value (including javascript: or data: URIs) is treated as unset and falls back
// to the placeholder. When BrainURL is empty, shows a "Graph coming soon" placeholder.
func (h *handlers) handleBrain(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	brainURL := strings.TrimSpace(h.cfg.BrainURL)
	// S3: only embed URLs with a safe absolute scheme; reject anything else.
	if brainURL != "" && !strings.HasPrefix(brainURL, "http://") && !strings.HasPrefix(brainURL, "https://") {
		brainURL = ""
	}
	var body string
	if brainURL != "" {
		escapedURL := html.EscapeString(brainURL)
		// S6: iframe fills the full viewport height minus the nav bar (~56px) so the
		// knowledge graph is the prominent main view with no wasted whitespace below it.
		body = fmt.Sprintf(`<section class="brain-stage"><header class="brain-bar"><span class="brain-title">Knowledge Graph</span></header><iframe src="%s" class="brain-frame" title="Knowledge Graph"></iframe></section>`, escapedURL)
	} else {
		body = `<section class="frame-section"><p class="section-kicker">BRAIN</p><h2>Knowledge Graph</h2><p>Graph coming soon. Set <code>ENGRAM_BRAIN_URL</code> to enable the interactive graph.</p></section>`
	}
	if isHTMXRequest(r) {
		renderHTML(w, body)
		return
	}
	renderComponent(w, r, Layout("Brain", p.DisplayName(), "brain", p.IsAdmin(), templ.Raw(body)))
}

// handleGraphRedirect redirects the legacy /dashboard/graph route to /dashboard/brain (S6).
// Permanent redirect (301) so browsers and crawlers update cached links.
func (h *handlers) handleGraphRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dashboard/brain", http.StatusMovedPermanently)
}

func (h *handlers) handleDashboardStats(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	overview := cloudstore.DashboardAdminOverview{}
	if h.cfg.Store != nil {
		// D2: when GetUserEmail is configured (JWT session mode), route via ScopedMemberOverview
		// which enforces per-user scope. Admin → team-wide; member → own counts; missing identity → 403.
		// When GetUserEmail is not configured (static-token/legacy bypass mode), fall back to
		// AdminOverview (pre-scoping behaviour) so existing non-JWT tests remain valid.
		if h.cfg.GetUserEmail != nil {
			scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
			loaded, err := h.cfg.Store.ScopedMemberOverview(scope)
			if err != nil {
				if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				h.renderStoreError(w, r, "dashboard", "Stats", err)
				return
			}
			overview = loaded
		} else {
			loaded, err := h.cfg.Store.AdminOverview()
			if err != nil {
				h.renderStoreError(w, r, "dashboard", "Stats", err)
				return
			}
			overview = loaded
		}
	}
	// R5-1: Build the stats body as raw HTML then wrap in templ Layout so the
	// status-ribbon, shell-footer, and "CLOUD ACTIVE" pill are always present on
	// full-page navigation (non-HTMX). Previously renderPageOrHTMX called the
	// string-based renderLayout which lacked those elements.
	//
	// D2: admin (or legacy bypass) sees Projects/Contributors/Chunks (team-wide); member
	// (JWT-authenticated, non-admin) sees Observations/Sessions/Prompts (own counts).
	var body string
	if p.IsAdmin() || h.cfg.GetUserEmail == nil {
		body = fmt.Sprintf(`<section class="frame-section"><p class="section-kicker">STATS</p><h2>Cloud Stats</h2><div class="metric-strip"><a href="/dashboard/projects" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Projects</span></a><a href="/dashboard/contributors" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Contributors</span></a><a href="/dashboard/browser" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Chunks</span></a></div></section>`, overview.Projects, overview.Contributors, overview.Chunks)
	} else {
		body = fmt.Sprintf(`<section class="frame-section"><p class="section-kicker">STATS</p><h2>My Activity</h2><div class="metric-strip"><a href="/dashboard/browser/observations" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Observations</span></a><a href="/dashboard/browser/sessions" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Sessions</span></a><a href="/dashboard/browser/prompts" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Prompts</span></a></div></section>`, overview.Observations, overview.Sessions, overview.Prompts)
	}
	if isHTMXRequest(r) {
		renderHTML(w, body)
		return
	}
	renderComponent(w, r, Layout("Stats", p.DisplayName(), "dashboard", p.IsAdmin(), templ.Raw(body)))
}

func (h *handlers) handleDashboardActivity(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows := make([]cloudstore.DashboardObservationRow, 0)
	if h.cfg.Store != nil {
		// D2: when GetUserEmail is configured (JWT session mode), use the scoped paginated
		// method which enforces per-user scope. Missing identity → 403.
		// When GetUserEmail is nil (static-token/legacy bypass), fall back to the unscoped
		// ListRecentObservations so existing non-JWT tests remain valid.
		if h.cfg.GetUserEmail != nil {
			scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
			loaded, _, err := h.cfg.Store.ListRecentObservationsPaginated(scope, project, query, "", 25, 0)
			if err != nil {
				if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				h.renderStoreError(w, r, "dashboard", "Activity", err)
				return
			}
			rows = loaded
		} else {
			loaded, err := h.cfg.Store.ListRecentObservations(project, query, 25)
			if err != nil {
				h.renderStoreError(w, r, "dashboard", "Activity", err)
				return
			}
			rows = loaded
		}
	}
	b := strings.Builder{}
	b.WriteString(`<section class="frame-section"><p class="section-kicker">ACTIVITY</p><h2>Recent Observation Activity</h2>`)
	if len(rows) == 0 {
		b.WriteString(`<div class="empty-state"><h3>No Activity</h3><p>No recent observations are available.</p></div>`)
	} else {
		b.WriteString(`<table class="data-table"><thead><tr><th>Project</th><th>Type</th><th>Title</th><th>Session</th><th>Created</th></tr></thead><tbody>`)
		for _, row := range rows {
			b.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td><td><a href="%s">%s</a></td><td>%s</td></tr>`, html.EscapeString(row.Project), html.EscapeString(row.Type), html.EscapeString(row.Title), safeQuery("/dashboard/browser/sessions/"+url.PathEscape(row.SessionID), preserveQuery(r.URL.RawQuery, "project", row.Project)), html.EscapeString(row.SessionID), html.EscapeString(row.CreatedAt)))
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(`</section>`)
	// R5-1: Use templ Layout for non-HTMX so status-ribbon and shell-footer are present.
	if isHTMXRequest(r) {
		renderHTML(w, b.String())
		return
	}
	renderComponent(w, r, Layout("Activity", p.DisplayName(), "dashboard", p.IsAdmin(), templ.Raw(b.String())))
}

func (h *handlers) handleBrowser(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	obsType := strings.TrimSpace(r.URL.Query().Get("type"))
	var projectNames []string
	var obsTypes []string
	if h.cfg.Store != nil {
		if projs, err := h.cfg.Store.ListProjects(""); err == nil {
			for _, pr := range projs {
				projectNames = append(projectNames, pr.Project)
			}
		}
		// Batch 6: source type pills from store (degrade gracefully on error).
		if types, err := h.cfg.Store.ListDistinctTypes(); err == nil {
			obsTypes = types
		}
	}
	// DR-05: load decided deletion requests for the member notice (non-admin only).
	// Only shown on the full-page render; HTMX partial (handleBrowserObservations) is unchanged.
	var decisions []cloudstore.StoredDeletionRequest
	if !p.IsAdmin() && h.cfg.Store != nil {
		if all, err := h.cfg.Store.ListDeletionRequestsForRequester(r.Context(), p.Email()); err == nil {
			for _, d := range all {
				if d.Status == "accepted" || d.Status == "rejected" {
					decisions = append(decisions, d)
				}
			}
		}
	}
	component := BrowserPage(projectNames, obsTypes, project, query, obsType, decisions)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), component))
}

func (h *handlers) handleBrowserObservations(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	obsType := strings.TrimSpace(r.URL.Query().Get("type"))
	// Browser tab scope — admins see ALL data; non-admins see only their own.
	// Mario reversed the original S6 decision on 2026-07-07: admins now have
	// full visibility in the Browser tab, matching their access in Contributors.
	// Non-admins are unaffected (IsAdmin: false filters to own email via applyReadScope).
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	// R2-1: parse page/pageSize without pre-clamping (total not known yet).
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardObservationRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListRecentObservationsPaginated(scope, project, query, obsType, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
				renderComponentStatus(w, r, http.StatusForbidden, ObservationsPartial(nil, Pagination{}))
				return
			}
			h.renderStoreError(w, r, "browser", "Observations", err)
			return
		}
	}
	// R2-1: re-clamp page to real totalPages; re-fetch if the requested page was beyond the end.
	// R3-7: on re-fetch error, log and keep previous rows rather than returning an error page.
	// R4-9: when re-fetch fails and rows are empty, attempt one additional fetch at page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListRecentObservationsPaginated(scope, project, query, obsType, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch observations page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListRecentObservationsPaginated(scope, project, query, obsType, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback observations page 1: %v", fallbackErr)
				}
			}
		}
	}
	partial := ObservationsPartial(rows, pg)
	if isHTMXRequest(r) {
		renderComponent(w, r, partial)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), BrowserPage(nil, nil, project, query, obsType, nil)))
}

func (h *handlers) handleBrowserSessions(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// Browser tab scope — admins see ALL data; non-admins see only their own.
	// S6 decision reversed on 2026-07-07 (see handleBrowserObservations comment).
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	// R2-1: parse page/pageSize without pre-clamping.
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardSessionRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListRecentSessionsPaginated(scope, project, query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
				renderComponentStatus(w, r, http.StatusForbidden, SessionsPartial(nil, Pagination{}))
				return
			}
			h.renderStoreError(w, r, "browser", "Sessions", err)
			return
		}
	}
	// R2-1: re-clamp and re-fetch if needed.
	// R3-7: on re-fetch error, log and keep previous rows (graceful degradation).
	// R4-9: when re-fetch fails and rows are empty, attempt one additional fetch at page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListRecentSessionsPaginated(scope, project, query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch sessions page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListRecentSessionsPaginated(scope, project, query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback sessions page 1: %v", fallbackErr)
				}
			}
		}
	}
	partial := SessionsPartial(rows, pg)
	if isHTMXRequest(r) {
		renderComponent(w, r, partial)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), BrowserPage(nil, nil, project, query, "", nil)))
}

func (h *handlers) handleBrowserPrompts(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// Browser tab scope — admins see ALL data; non-admins see only their own.
	// S6 decision reversed on 2026-07-07 (see handleBrowserObservations comment).
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	// R2-1: parse page/pageSize without pre-clamping.
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardPromptRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListRecentPromptsPaginated(scope, project, query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
				renderComponentStatus(w, r, http.StatusForbidden, PromptsPartial(nil, Pagination{}))
				return
			}
			h.renderStoreError(w, r, "browser", "Prompts", err)
			return
		}
	}
	// R2-1: re-clamp and re-fetch if needed.
	// R3-7: on re-fetch error, log and keep previous rows (graceful degradation).
	// R4-9: when re-fetch fails and rows are empty, attempt one additional fetch at page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListRecentPromptsPaginated(scope, project, query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch prompts page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListRecentPromptsPaginated(scope, project, query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback prompts page 1: %v", fallbackErr)
				}
			}
		}
	}
	partial := PromptsPartial(rows, pg)
	if isHTMXRequest(r) {
		renderComponent(w, r, partial)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), BrowserPage(nil, nil, project, query, "", nil)))
}

// handleBrowserSessionDetail handles GET /dashboard/browser/sessions/{sessionID}.
// R4-5: migrated to use principalFromRequest and renderComponentStatus for empty state.
// R5-6: use r.Clone to avoid mutating shared request state when delegating to handleBrowserSessions.
func (h *handlers) handleBrowserSessionDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if sessionID == "" {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Session Detail", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Session Not Found", "No dashboard data exists for that session identifier.")))
		return
	}
	// Clone the request before mutating URL so the original request is not modified.
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = preserveQuery(r.URL.RawQuery, "q", sessionID)
	h.handleBrowserSessions(w, r2)
}

func (h *handlers) handleProjects(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	component := ProjectsPage(query)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Projects", p.DisplayName(), "projects", p.IsAdmin(), component))
}

func (h *handlers) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	if project == "" {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Project Detail", p.DisplayName(), "projects", p.IsAdmin(), EmptyState("Project Not Found", "No replicated dashboard data exists for that project.")))
		return
	}
	var stats *cloudstore.DashboardProjectRow
	var ctrl *cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		detail, err := h.cfg.Store.ProjectDetail(project)
		if err != nil {
			h.renderStoreError(w, r, "projects", "Project detail", err)
			return
		}
		statsRow := detail.Stats
		stats = &statsRow
		// Degrade gracefully: if sync control lookup fails, render without pause audit.
		if c, err := h.cfg.Store.GetProjectSyncControl(project); err == nil {
			ctrl = c
		}
	}
	component := ProjectDetailPage(project, stats, ctrl)
	renderComponent(w, r, Layout("Project Detail", p.DisplayName(), "projects", p.IsAdmin(), component))
}

func (h *handlers) handleContributors(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	// S6: Contributors tab is admin-only — it shows the global (all-contributors) view.
	// Non-admin users get 403; they use the Browser tab to see their own data.
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// R6-1: serve only the shell; the list is loaded via HTMX from /dashboard/contributors/list.
	// This mirrors the ProjectsPage pattern — no store call at the shell level.
	component := ContributorsPage(query)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Contributors", p.DisplayName(), "contributors", p.IsAdmin(), component))
}

// handleContributorsList handles GET /dashboard/contributors/list.
// R5-2: always returns ContributorsListPartial (no full page wrapper) so HTMX
// pagination targets can swap just the content div.
// R6-2: on store error, always renders a fragment (no Layout wrapper) — partial-only contract.
// S6: admin-gated; non-admins receive 403. Uses global scope (IsAdmin: true) so all
// contributors are visible — this is the Contributors tab, not the scoped Browser.
func (h *handlers) handleContributorsList(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	// S6: admin-only endpoint.
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// S6: Contributors always uses global scope regardless of the caller's actual role,
	// since only admins can reach this endpoint and they need the full picture.
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: true}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardContributorRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListContributorsPaginated(scope, query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			log.Printf("dashboard: contributors list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Dashboard data is temporarily unavailable."))
			return
		}
	}
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListContributorsPaginated(scope, query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch contributors list page %d: %v", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListContributorsPaginated(scope, query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback contributors list page 1: %v", fallbackErr)
				}
			}
		}
	}
	renderComponent(w, r, ContributorsListPartial(rows, pg))
}

func (h *handlers) handleContributorDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	contributor := strings.TrimSpace(r.PathValue("contributor"))
	if contributor == "" {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Contributor Detail", p.DisplayName(), "contributors", p.IsAdmin(), EmptyState("Contributor Not Found", "No dashboard data exists for that contributor.")))
		return
	}
	if h.cfg.Store == nil {
		renderComponent(w, r, Layout("Contributor Detail", p.DisplayName(), "contributors", p.IsAdmin(), ContributorDetailPage(nil, nil, nil, nil)))
		return
	}
	row, sessions, observations, prompts, err := h.cfg.Store.GetContributorDetail(contributor)
	if err != nil {
		h.renderStoreError(w, r, "contributors", "Contributor detail", err)
		return
	}
	component := ContributorDetailPage(&row, sessions, observations, prompts)
	renderComponent(w, r, Layout("Contributor Detail", p.DisplayName(), "contributors", p.IsAdmin(), component))
}

// pendingDeletionCount returns the count of pending deletion requests.
// Returns 0 on nil store, nil-count method, or any error so admin pages
// degrade gracefully rather than failing entirely over a badge count.
func (h *handlers) pendingDeletionCount(ctx context.Context) int {
	if h.cfg.Store == nil {
		return 0
	}
	count, err := h.cfg.Store.PendingDeletionRequestCount(ctx)
	if err != nil {
		return 0
	}
	return count
}

func (h *handlers) handleAdmin(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var health *cloudstore.DashboardSystemHealth
	var controls []cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		if sh, err := h.cfg.Store.SystemHealth(); err == nil {
			health = &sh
		}
		if ctrls, err := h.cfg.Store.ListProjectSyncControls(); err == nil {
			controls = ctrls
		}
	}
	pendingCount := h.pendingDeletionCount(r.Context())
	component := AdminPage(health, controls, pendingCount)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Admin", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminProjectControls handles GET /dashboard/admin/projects.
// Batch 6: renders AdminProjectsPage templ with sync controls, replacing the
// previous delegation to handleProjects which had no toggle UI.
func (h *handlers) handleAdminProjectControls(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var controls []cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		// Degrade gracefully: empty controls if store fails.
		if ctrls, err := h.cfg.Store.ListProjectSyncControls(); err == nil {
			controls = ctrls
		}
	}
	pendingCount := h.pendingDeletionCount(r.Context())
	component := AdminProjectsPage(controls, pendingCount)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Admin Projects", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// ─── 11 New Handler Implementations (visual parity batch) ────────────────────

// handleProjectsList handles GET /dashboard/projects/list (HTMX partial).
// Batch 6: passes sync controls map so Paused badge renders correctly.
// R4-3: uses parsePaginationRaw + reclampPagination so >50 projects are reachable.
func (h *handlers) handleProjectsList(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// R4-3: parse raw page/pageSize (no pre-clamp) before first store call.
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardProjectRow, 0)
	total := 0
	var controlsMap map[string]cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListProjectsPaginated(query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			// R6-2: partial-only endpoint — always render fragment, never full Layout (even non-HTMX).
			log.Printf("dashboard: projects list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Dashboard data is temporarily unavailable."))
			return
		}
		// Degrade gracefully: if controls fail, render without badges.
		if ctrls, err := h.cfg.Store.ListProjectSyncControls(); err == nil {
			controlsMap = controlsByProject(ctrls)
		}
	}
	// R4-3: re-clamp to real total; re-fetch if requested page was beyond last page.
	// R5-3: add tier-3 fallback — if clamped re-fetch fails AND rows are empty, attempt page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListProjectsPaginated(query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch projects list page %d: %v (using first-page rows)", pg.Page, err)
			// R5-3: tier-3 fallback to page 1 when re-fetch fails and rows are empty.
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListProjectsPaginated(query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback projects list page 1: %v", fallbackErr)
				}
			}
		}
	}
	renderComponent(w, r, ProjectsListPartial(rows, controlsMap, pg))
}

// handleProjectObservationsPartial handles GET /dashboard/projects/{name}/observations.
func (h *handlers) handleProjectObservationsPartial(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	name := strings.TrimSpace(r.PathValue("name"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	obsType := strings.TrimSpace(r.URL.Query().Get("type"))
	rows := make([]cloudstore.DashboardObservationRow, 0)
	if h.cfg.Store != nil {
		var err error
		rows, _, err = h.cfg.Store.ListRecentObservationsPaginated(scope, name, query, obsType, 50, 0)
		if err != nil {
			if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
				renderComponentStatus(w, r, http.StatusForbidden, ObservationsPartial(nil, Pagination{}))
				return
			}
			h.renderStoreError(w, r, "projects", "Project observations", err)
			return
		}
	}
	renderComponent(w, r, ObservationsPartial(rows, Pagination{}))
}

// handleProjectSessionsPartial handles GET /dashboard/projects/{name}/sessions.
func (h *handlers) handleProjectSessionsPartial(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	name := strings.TrimSpace(r.PathValue("name"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows := make([]cloudstore.DashboardSessionRow, 0)
	if h.cfg.Store != nil {
		var err error
		rows, _, err = h.cfg.Store.ListRecentSessionsPaginated(scope, name, query, 50, 0)
		if err != nil {
			if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
				renderComponentStatus(w, r, http.StatusForbidden, SessionsPartial(nil, Pagination{}))
				return
			}
			h.renderStoreError(w, r, "projects", "Project sessions", err)
			return
		}
	}
	renderComponent(w, r, SessionsPartial(rows, Pagination{}))
}

// handleProjectPromptsPartial handles GET /dashboard/projects/{name}/prompts.
func (h *handlers) handleProjectPromptsPartial(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	name := strings.TrimSpace(r.PathValue("name"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows := make([]cloudstore.DashboardPromptRow, 0)
	if h.cfg.Store != nil {
		var err error
		rows, _, err = h.cfg.Store.ListRecentPromptsPaginated(scope, name, query, 50, 0)
		if err != nil {
			if errors.Is(err, cloudstore.ErrDashboardIdentityRequired) {
				renderComponentStatus(w, r, http.StatusForbidden, PromptsPartial(nil, Pagination{}))
				return
			}
			h.renderStoreError(w, r, "projects", "Project prompts", err)
			return
		}
	}
	renderComponent(w, r, PromptsPartial(rows, Pagination{}))
}

// handleAdminUsers handles GET /dashboard/admin/users.
// Renders the unified Users management page: provisioned-user list + add form +
// per-row role/status edit + activate/deactivate controls. Sources users from
// ListProvisionedUsers (users.yaml) and department names from ListDepartmentsCanonical.
func (h *handlers) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	members := h.provisionedUsersList()

	// Source department names from the canonical classrules list when available;
	// fall back to nil so the templ renders sensible hardcoded defaults.
	var departments []string
	if h.cfg.ListDepartmentsCanonical != nil {
		departments = h.cfg.ListDepartmentsCanonical()
	}

	// Flash messages are passed via query param after a redirect.
	flash := r.URL.Query().Get("flash")
	flashErr := r.URL.Query().Get("error") == "1"

	pendingCount := h.pendingDeletionCount(r.Context())
	component := AdminUsersManagementPage(members, departments, flash, flashErr, strings.ToLower(strings.TrimSpace(p.Email())), pendingCount)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Admin Users", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminUsersList handles GET /dashboard/admin/users/list.
// R5-2: always returns AdminUsersListPartial (partial only, no full shell wrapper).
// R6-2: on store error, always renders a fragment (no Layout wrapper) — partial-only contract.
// Admin-gated.
func (h *handlers) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Admin users list: admin scope, no per-member filtering.
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardContributorRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListContributorsPaginated(scope, "", pageSize, (reqPage-1)*pageSize)
		if err != nil {
			log.Printf("dashboard: admin users list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Dashboard data is temporarily unavailable."))
			return
		}
	}
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListContributorsPaginated(scope, "", pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch admin users list page %d: %v", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListContributorsPaginated(scope, "", pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback admin users list page 1: %v", fallbackErr)
				}
			}
		}
	}
	renderComponent(w, r, AdminUsersListPartial(rows, pg))
}

// handleAdminHealth handles GET /dashboard/admin/health.
func (h *handlers) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var health *cloudstore.DashboardSystemHealth
	if h.cfg.Store != nil {
		if sh, err := h.cfg.Store.SystemHealth(); err == nil {
			health = &sh
		}
	}
	pendingCount := h.pendingDeletionCount(r.Context())
	component := AdminHealthPage(health, pendingCount)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Admin Health", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminSyncTogglePost handles POST /dashboard/admin/projects/{name}/sync.
// Admin-gated. Sets sync enabled/disabled for the project. Satisfies REQ-112, AD-6.
func (h *handlers) handleAdminSyncTogglePost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	enabledRaw := strings.TrimSpace(r.FormValue("enabled"))
	if enabledRaw != "true" && enabledRaw != "false" {
		http.Error(w, "invalid value for enabled: must be 'true' or 'false'", http.StatusBadRequest)
		return
	}
	enabled := enabledRaw == "true"
	reason := strings.TrimSpace(r.FormValue("reason"))
	if h.cfg.Store != nil {
		if err := h.cfg.Store.SetProjectSyncEnabled(name, enabled, p.DisplayName(), reason); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
	}
	redirectURL := "/dashboard/admin/projects"
	// R2-3: For HTMX requests, return 200 + HX-Redirect only.
	// http.Redirect writes a 303 regardless; HTMX intercepts 303 and follows natively,
	// making HX-Redirect irrelevant. For plain browser forms, keep the 303.
	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// handleAdminSyncToggleForm handles GET /dashboard/admin/projects/{name}/sync/form.
func (h *handlers) handleAdminSyncToggleForm(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	ctrl := cloudstore.ProjectSyncControl{Project: name, SyncEnabled: true}
	if h.cfg.Store != nil {
		if c, err := h.cfg.Store.GetProjectSyncControl(name); err == nil && c != nil {
			ctrl = *c
		}
	}
	renderComponent(w, r, AdminSyncToggleFormPartial(ctrl))
}

// handleSessionDetail handles GET /dashboard/sessions/{project}/{sessionID}.
// Satisfies REQ-106, Design Decision 3, Design Decision 5.
func (h *handlers) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if project == "" || sessionID == "" || len(sessionID) > 128 {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Session", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Session Not Found", "Invalid session identifier.")))
		return
	}
	// D2: build per-request read scope for session detail.
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	var sess *cloudstore.DashboardSessionRow
	var obs []cloudstore.DashboardObservationRow
	var prompts []cloudstore.DashboardPromptRow
	if h.cfg.Store != nil {
		s, o, pr, err := h.cfg.Store.GetSessionDetail(scope, project, sessionID)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Session detail", err)
			return
		}
		sess = &s
		obs = o
		prompts = pr
	}
	component := SessionDetailPage(sess, obs, prompts)
	renderComponent(w, r, Layout("Session Detail", p.DisplayName(), "browser", p.IsAdmin(), component))
}

// handleObservationDetail handles GET /dashboard/observations/{project}/{sessionID}/{syncID}.
func (h *handlers) handleObservationDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	syncID := strings.TrimSpace(r.PathValue("syncID"))
	if project == "" || sessionID == "" || syncID == "" || len(syncID) > 128 {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Observation", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Observation Not Found", "Invalid observation identifier.")))
		return
	}
	// D2: build per-request read scope for observation detail.
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	var obs *cloudstore.DashboardObservationRow
	var sess *cloudstore.DashboardSessionRow
	var related []cloudstore.DashboardObservationRow
	if h.cfg.Store != nil {
		o, s, rel, err := h.cfg.Store.GetObservationDetail(scope, project, sessionID, syncID)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Observation detail", err)
			return
		}
		obs = &o
		sess = &s
		related = rel
	}
	// DR-01: compute owner-guard and pending-state booleans.
	// canRequestRemoval: non-admin whose email matches the observation's user_email.
	// removalPending: the owner already has a pending deletion request for this syncID.
	var canRequestRemoval, removalPending bool
	if obs != nil && !p.IsAdmin() {
		canRequestRemoval = strings.EqualFold(strings.TrimSpace(p.Email()), strings.TrimSpace(obs.UserEmail))
	}
	if canRequestRemoval && h.cfg.Store != nil {
		if reqs, err := h.cfg.Store.ListDeletionRequestsForRequester(r.Context(), p.Email()); err == nil {
			for _, req := range reqs {
				if req.TargetSyncID == syncID && req.Status == "pending" {
					removalPending = true
					break
				}
			}
		}
	}
	component := ObservationDetailPage(obs, sess, related, canRequestRemoval, removalPending)
	renderComponent(w, r, Layout("Observation Detail", p.DisplayName(), "browser", p.IsAdmin(), component))
}

// handlePromptDetail handles GET /dashboard/prompts/{project}/{sessionID}/{syncID}.
func (h *handlers) handlePromptDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	syncID := strings.TrimSpace(r.PathValue("syncID"))
	if project == "" || sessionID == "" || syncID == "" || len(syncID) > 128 {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Prompt", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Prompt Not Found", "Invalid prompt identifier.")))
		return
	}
	// D2: build per-request read scope for prompt detail.
	scope := &cloudstore.ReadScope{Email: p.Email(), IsAdmin: p.IsAdmin()}
	var prompt *cloudstore.DashboardPromptRow
	var sess *cloudstore.DashboardSessionRow
	var related []cloudstore.DashboardPromptRow
	if h.cfg.Store != nil {
		pr, s, rel, err := h.cfg.Store.GetPromptDetail(scope, project, sessionID, syncID)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Prompt detail", err)
			return
		}
		prompt = &pr
		sess = &s
		related = rel
	}
	component := PromptDetailPage(prompt, sess, related)
	renderComponent(w, r, Layout("Prompt Detail", p.DisplayName(), "browser", p.IsAdmin(), component))
}

// renderObservationsTable removed in Batch 6 REFACTOR — replaced by ObservationsPartial templ component.

func (h *handlers) renderStoreError(w http.ResponseWriter, r *http.Request, activeTab string, contextLabel string, err error) {
	status, headline, message := classifyStoreError(contextLabel, err)
	log.Printf("dashboard: %s store error: %v", strings.ToLower(strings.TrimSpace(contextLabel)), err)
	fragment := fmt.Sprintf(`<div class="empty-state" role="alert"><h3>%s</h3><p>%s</p></div>`, html.EscapeString(headline), html.EscapeString(message))
	if isHTMXRequest(r) {
		renderHTMLStatus(w, status, fragment)
		return
	}
	// R5-1: Use templ Layout for non-HTMX error pages so status-ribbon and shell-footer
	// are always present regardless of which handler generated the error.
	p := h.principalFromRequest(r)
	body := fmt.Sprintf(`<section class="frame-section"><p class="section-kicker">DEGRADED</p><h2>%s</h2>%s</section>`, html.EscapeString(contextLabel), fragment)
	renderComponentStatus(w, r, status, Layout(contextLabel, p.DisplayName(), activeTab, p.IsAdmin(), templ.Raw(body)))
}

func classifyStoreError(contextLabel string, err error) (int, string, string) {
	switch {
	case errors.Is(err, cloudstore.ErrDashboardProjectInvalid):
		return http.StatusNotFound, "Project not found", "No replicated dashboard data exists for that project."
	case errors.Is(err, cloudstore.ErrDashboardProjectForbidden):
		return http.StatusForbidden, "Project access denied", "You are not allowed to access that project scope."
	case errors.Is(err, cloudstore.ErrDashboardProjectNotFound):
		return http.StatusNotFound, "Project not found", "No replicated dashboard data exists for that project."
	// R4-7: Contributor not found must produce a contributor-specific message, not "Project not found".
	case errors.Is(err, cloudstore.ErrDashboardContributorNotFound):
		return http.StatusNotFound, "Contributor not found", "No contributor with that name has been seen in this cloud workspace."
	// R5-4: Session/observation/prompt not found — entity-specific error pages.
	case errors.Is(err, cloudstore.ErrDashboardSessionNotFound):
		return http.StatusNotFound, "Session not found", "No session with that identifier exists in the specified project."
	case errors.Is(err, cloudstore.ErrDashboardObservationNotFound):
		return http.StatusNotFound, "Observation not found", "No observation with that identifier exists in the specified session."
	case errors.Is(err, cloudstore.ErrDashboardPromptNotFound):
		return http.StatusNotFound, "Prompt not found", "No prompt with that identifier exists in the specified session."
	default:
		heading := strings.TrimSpace(contextLabel)
		if heading == "" {
			heading = "Dashboard data"
		}
		return http.StatusServiceUnavailable, heading + " unavailable", "Cloud dashboard data is temporarily unavailable."
	}
}

func isHTMXRequest(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("HX-Request")), "true")
}

// renderBrowserBody, renderProjectSessions, renderProjectObservations, renderProjectPrompts
// removed in Batch 6 REFACTOR — replaced by templ partials (BrowserPage, SessionsPartial,
// ObservationsPartial, PromptsPartial, ProjectDetailPage).
// renderPageOrHTMX removed in R5-1 REFACTOR — callers now use renderComponent(w, r, Layout(...)).

// renderLoginPage removed in Batch 6 REFACTOR — replaced by LoginPage templ component.
// renderLayout, shellNavLink removed in R5-1 REFACTOR — all handlers now use the templ Layout component
// which includes status-ribbon, shell-footer, and CLOUD ACTIVE pill.

func renderHTML(w http.ResponseWriter, body string) {
	renderHTMLStatus(w, http.StatusOK, body)
}

func renderHTMLStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// ─── Audit Log Handlers ───────────────────────────────────────────────────────

// handleAdminAuditLog handles GET /dashboard/admin/audit-log (shell, admin-gated).
// REQ-408: renders the AdminAuditLogPage templ component.
// JW2: filter is parsed and forwarded to the initial hx-get URL for deep-linking.
// JW6: invalid time formats yield 400.
func (h *handlers) handleAdminAuditLog(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	filter, filterErr := parseAuditFilter(r)
	if filterErr != "" {
		http.Error(w, filterErr, http.StatusBadRequest)
		return
	}
	pendingCount := h.pendingDeletionCount(r.Context())
	component := AdminAuditLogPage(p.DisplayName(), filter, pendingCount)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Audit Log", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminAuditLogList handles GET /dashboard/admin/audit-log/list (partial, admin-gated, HTMX).
// REQ-409: renders AdminAuditLogListPartial with filter and pagination from query params.
// JW6: invalid time format in from/to params yields 400 instead of silent drop.
// N7: partial-only endpoint — always renders fragment, never a full Layout wrapper
// (even for non-HTMX requests). Consistent with R6-2 partial-only contract.
func (h *handlers) handleAdminAuditLogList(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	filter, filterErr := parseAuditFilter(r)
	if filterErr != "" {
		http.Error(w, filterErr, http.StatusBadRequest)
		return
	}
	reqPage, pageSize := parsePaginationRaw(r)

	var rows []cloudstore.DashboardAuditRow
	var total int
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListAuditEntriesPaginated(r.Context(), filter, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			log.Printf("dashboard: audit log list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Audit log data is temporarily unavailable."))
			return
		}
	}

	// JW3: three-tier fallback pattern — consistent with other paginated handlers.
	// Tier 1: initial fetch (above). Tier 2: clamped re-fetch on page-out-of-range.
	// Tier 3: page-1 fallback when re-fetch fails and rows are empty.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListAuditEntriesPaginated(r.Context(), filter, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch audit log list page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListAuditEntriesPaginated(r.Context(), filter, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback audit log list page 1: %v", fallbackErr)
				}
			}
		}
	}

	renderComponent(w, r, AdminAuditLogListPartial(rows, pg, filter))
}

// parseAuditTime tries RFC3339 then date-only (2006-01-02) formats.
// Returns an error only when the value is non-empty and unparseable in either format.
// JW6: accepting date-only prevents confusing silent drops while still being lenient.
func parseAuditTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	// Try RFC3339 first (most specific).
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	// Fall back to date-only YYYY-MM-DD (midnight UTC).
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid_time_format: %q is not RFC3339 or YYYY-MM-DD", value)
}

// parseAuditFilter extracts AuditFilter fields from the request query params.
// Text filters are trimmed; time filters accept RFC3339 or date-only (YYYY-MM-DD). REQ-410.
// Returns the filter and an error string; on error, error is non-empty and the caller
// should return a 400 response. JW6 fix.
func parseAuditFilter(r *http.Request) (cloudstore.AuditFilter, string) {
	q := r.URL.Query()
	filter := cloudstore.AuditFilter{
		Contributor: strings.TrimSpace(q.Get("contributor")),
		Project:     strings.TrimSpace(q.Get("project")),
		Outcome:     strings.TrimSpace(q.Get("outcome")),
	}
	if from := strings.TrimSpace(q.Get("from")); from != "" {
		t, err := parseAuditTime(from)
		if err != nil {
			return cloudstore.AuditFilter{}, err.Error()
		}
		filter.OccurredAtFrom = t
	}
	if to := strings.TrimSpace(q.Get("to")); to != "" {
		t, err := parseAuditTime(to)
		if err != nil {
			return cloudstore.AuditFilter{}, err.Error()
		}
		filter.OccurredAtTo = t
	}
	return filter, ""
}
