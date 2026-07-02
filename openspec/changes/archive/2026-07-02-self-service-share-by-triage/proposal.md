# Proposal: Self-Service Share-by-Triage

## Intent

Today the fork forces one shared project (`ENGRAM_PROJECT=viva`), so every memory leaves the user's machine regardless of intent, admins must mediate enrollment, and non-git folders save with `project=''` which **always** bypasses the sync enrollment gate (BUG #955) — poisoning sync batches and blocking QA user Sergio. We want **private by default + user-controlled sharing**: a user works in any folder, everything stays PRIVATE, and only when the user themselves clicks "share with Viva" in triage does that folder sync (auto-tagged by juego/project/department). The share decision MUST be the user's so personal notes never leak.

## Scope

### In Scope
- **Piece 3 (foundation)**: `ENGRAM_DEFAULT_PROJECT` env fallback used only when `detectProject(cwd)==""`; default `"personal"` (private). Fix BUG #955 so `project=''` mutations stop bypassing the enrollment gate.
- **One-time migration**: existing `project=''` orphan mutations → reassigned to `"personal"` (private), not discarded.
- **Piece 1 (triage)**: `POST /project/{name}/share` action = client `EnrollProject(cwd)` + server self-service enroll + `default_scope=shared`, atomic with explicit success/failure feedback.
- **Unshare**: triage lets the user remove a folder from shared (stops future sync).
- **Piece 2 (server)**: `POST /user/enrolled-projects` JWT-authed self-service endpoint that mutates the AUTHENTICATED user's OWN `enrolled` in users.yaml (reuse `writePrincipalsAndReload` minus admin gate; add a write mutex).

### Out of Scope (Non-Goals)
- Redesigning juego/project/department tagging (already automatic).
- Changing the cloud auth/JWT model.
- A full per-observation reassignment UI beyond what the orphan migration needs.
- Removing `ENGRAM_PROJECT` (kept as explicit override; onboarding just stops forcing it).

## Capabilities

### New Capabilities
- `self-service-share`: triage share/unshare action; client+server enrollment; atomic apply with feedback.
- `private-by-default`: `ENGRAM_DEFAULT_PROJECT` fallback + `project=''` enrollment-gate fix.

### Modified Capabilities
- `sync-enrollment-gate`: `project=''` no longer auto-syncs; gate requires real enrollment.

## Approach

Per-folder projects via cwd detection. `ENGRAM_PROJECT` stays an explicit override (non-per-folder deployments); `ENGRAM_DEFAULT_PROJECT` (default `"personal"`, private) is the fallback when cwd resolves to "". Build order: **Piece 3 → Piece 1 → Piece 2**. Sharing `"personal"` mixes genuinely-personal notes, so the spec MUST define selective sharing (per-observation vs whole-project) — flagged as a design point.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/store/store.go` ~L3958 | Modified | Fix `sm.project=''` gate bypass + migration of orphans to `"personal"` |
| `cmd/engram/main.go` (L812, L875) | Modified | `ENGRAM_DEFAULT_PROJECT` fallback when cwd detect == "" |
| `internal/triage/server.go` + `handlers.go` | New/Modified | `POST /project/{name}/share` + unshare; `EnrollmentStore` seam |
| `cmd/engram/triage.go` | Modified | Wire enrollment store, cloud config, JWT token at startup |
| `internal/cloud/cloudserver/cloudserver.go` | New | `POST /user/enrolled-projects` self-service endpoint |
| `internal/cloud/dashboard/admin_members_handler.go` | Reused | `writePrincipalsAndReload` pattern minus admin gate + mutex |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Concurrent users.yaml write race | Med | Server-side mutex around read-modify-write-reload |
| JWT absent/expired at triage time | Med | Atomic share fails cleanly with "not logged in"; no half-applied state |
| `project=''` orphan migration | Med | One-time, idempotent migration to `"personal"`; private so no leak |
| Server git-commit noise per enroll | Low | Accept for v1; flag for future batching |

## Rollback Plan

Revert the three pieces independently (build order is reverse-safe). Migration is idempotent and lands orphans in the private `"personal"` project, so re-running is safe and never leaks. `ENGRAM_PROJECT` override remains intact, so existing Viva deployments keep working if the change is reverted.

## Dependencies

- Triage needs `credentials.json` JWT + cloud config available at startup (new wiring).
- Strict TDD active: `go test ./...`; run `make templ` before tests touching triage/dashboard.

## Success Criteria

- [ ] Non-git folder memories save to `"personal"` and do NOT sync (private by default).
- [ ] `project=''` no longer bypasses the enrollment gate; orphans migrated to `"personal"`.
- [ ] Triage share enrolls client + server + sets `shared` atomically, or fails with clear feedback.
- [ ] Triage unshare stops future sync for a folder.
- [ ] Self-service server endpoint mutates only the caller's own `enrolled`.
- [ ] BUG #955 resolved → QA user Sergio unblocked (orphan poisoning root removed).

## First Slice Boundary

Smallest shippable increment: **Piece 3 + orphan migration** — fixes BUG #955 and unblocks Sergio with no UX dependency. Then **Piece 1 + Piece 2** ship the share/unshare UX.
