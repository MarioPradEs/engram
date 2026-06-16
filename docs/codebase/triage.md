[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Dashboard](dashboard.md) | [Next: Integrations →](integrations.md)

# Local Triage UI

**`engram triage` is a loopback-only browser UI for reviewing and reclassifying observation scope. It runs exclusively on your machine and requires no authentication — the same trust model as `engram serve`.**

## Quick start

```bash
# Start the triage UI (opens browser automatically)
engram triage

# Suppress browser auto-open (headless / CI)
ENGRAM_TRIAGE_NO_BROWSER=1 engram triage

# Override port (default 7438)
ENGRAM_TRIAGE_PORT=9000 engram triage
```

The server listens on `127.0.0.1:7438` and routes traffic through its own `http.ServeMux` — completely independent of the main Engram API at `:7437`.

## Architecture

```text
engram triage
      │
      ▼
cmd/engram/triage.go
  cmdTriage() — store open, port resolution, SIGINT shutdown
      │
      ▼
internal/triage/server.go
  Server — own ServeMux, loopback-only bind, browser opener seam
      │
      ├── GET /                 index: all projects + stats
      ├── GET /project/{name}   per-project observation list
      ├── POST /observations/{id}/scope   per-item scope toggle
      ├── POST /project/{name}/set-scope   bidirectional bulk set-scope (confirm gate)
      ├── POST /project/{name}/classify   set project default_scope (cwd only)
      └── GET /triage/static/   embedded pico.min.css + htmx.min.js + triage.css
            │
            ▼
      internal/triage/handlers.go
        TriageStore / MutableTriageStore — narrow interfaces over *store.Store
            │
            ▼
      internal/triage/*.templ → *_templ.go (server-rendered, htmx partials)
```

## Key files

| File | Responsibility |
|---|---|
| `cmd/engram/triage.go` | CLI entry point — store open, port resolution, SIGINT shutdown. |
| `internal/triage/server.go` | `Server` struct, constructors, route registration, `StoreAdapter`. |
| `internal/triage/handlers.go` | `TriageStore`/`MutableTriageStore` interfaces + all route handlers. |
| `internal/triage/defaultscope.go` | `ResolveDefaultScope` — reads `default_scope` from config.json for the cwd project. |
| `internal/triage/scope.go` | `ToInternalScope`/`UIScopeOf` — maps between UI vocab (shared/personal) and store tiers. |
| `internal/triage/configwrite.go` | `WriteProjectDefaultScope` — atomic temp+rename write to config.json. |
| `internal/triage/layout.templ` | Top-level HTML layout (pico + htmx + triage.css). |
| `internal/triage/projects.templ` | Index page: project cards with stats. |
| `internal/triage/projectlist.templ` | Per-project list: per-row toggle forms, bidirectional bulk set-scope, classify controls. |
| `internal/triage/embed.go` | Embedded static assets via `//go:embed`. |
| `internal/triage/static/` | `triage.css`, `pico.min.css`, `htmx.min.js`. |

## Trust model

Loopback-only, no authentication. Access is controlled by network position: only processes on `127.0.0.1` can reach the server. This is the same model as `engram serve` (`:7437`).

**Do not expose the triage port externally.** There is no auth layer to add because the design assumption is your-machine-only.

### CSRF mitigation

State-changing POST endpoints (`/observations/{id}/scope`, `/project/{name}/set-scope`, `/project/{name}/classify`) are wrapped with an **Origin-check middleware**. If a request carries an `Origin` header that is not one of the trusted loopback origins (`http://127.0.0.1:7438`, `http://localhost:7438`), the server responds `403 Forbidden`.

This prevents a malicious web page from making cross-origin requests to the triage server on behalf of the local user. Requests without an `Origin` header (curl, direct browser navigation, same-origin form submissions) pass through unchanged.

## Scope model (UI ↔ store)

`engram triage` uses a two-value vocabulary in the UI and in config.json:

| UI label | config.json value | Internal store tier |
|---|---|---|
| Shared | `"shared"` | `"team"` |
| Personal | `"personal"` | `"personal"` |

The mapping is done in `internal/triage/scope.go`. The store and cloud layers never see the UI vocabulary — they operate on internal tiers (`team`, `personal`).

Observations with scope values outside these two (legacy `"needs-triage"`, `"project"`, etc.) surface as **Needs triage** in the UI so they appear in the review queue.

## Scope enforcement — Gate B (server-side)

`engram triage` toggles scope on the local SQLite store. When observations are pushed to the cloud, the cloud-side `InsertMutationBatch` in `internal/cloud/cloudstore` drops any observation with `scope=personal` before writing it to Postgres. This is **Gate B** and is the primary privacy boundary.

**There is no client-side gate (Gate A) in v1.** The toggle sets scope in local SQLite immediately; the cloud drop happens at push time on the server. Gate A (a predicate in the autosync path) is scoped as a future change and was intentionally excluded from v1 to avoid diverging the sync path unnecessarily.

## cwd-only default scope (Option A)

The **Classify** button in the triage UI writes `default_scope` to `config.json` in the detected git-root of the **current working directory only**. This is Option A, the narrowest boundary:

- Toggle and bulk-share work for ALL projects in the local store.
- Setting a project default (classify) is only available for the project whose root matches the cwd at `engram triage` start time.
- Other projects can have their default set the next time `engram triage` is run from their directory.

## Templ generation

The triage HTML components are generated from `.templ` source files using the `templ` code generator:

```bash
# Regenerate triage templ components (from repo root)
make templ
```

The `Makefile` target runs `go tool templ generate` for both `internal/cloud/dashboard/...` and `internal/triage/...`. Generated `*_templ.go` files are committed alongside their `.templ` sources — this is the established convention in the fork (see `internal/cloud/dashboard/` for precedent).

## Triage change checklist

- [ ] Handlers stay in `internal/triage/handlers.go`.
- [ ] Scope mapping stays in `internal/triage/scope.go` (pure, no store import).
- [ ] Config writes use `WriteProjectDefaultScope` (atomic temp+rename).
- [ ] `StoreAdapter` proxies to `*store.Store` — do not re-implement store logic in handlers.
- [ ] Templ components are regenerated (`make templ`) and committed when `.templ` files change.
- [ ] Golden files in `internal/triage/testdata/golden/` are updated when HTML output changes.
- [ ] The triage server does NOT share routes, middleware, or store state with `internal/server`.
- [ ] Any new mutation endpoint must enforce the cwd boundary if it writes config.json.

---

[← Previous: Dashboard](dashboard.md) | [Next: Integrations →](integrations.md)
