# Live User Directory Reload Specification

## Change metadata

- Change: configurable-jwt-ttl-live-revocation
- Capability: live-user-directory-reload
- Kind: ADDITIVE (new filesystem watcher capability for user directory)
- REQ range: REQ-005 through REQ-007

## Purpose

The `YAMLLoader` gains a `Watch(ctx context.Context)` method backed by `fsnotify` that monitors the `users.yaml` file and triggers automatic `Reload()` when the file changes on disk, including via atomic rename operations (used by the admin dashboard). This enables live user revocation and enrollment updates without requiring a SIGHUP signal or server restart.

---

## ADDED Requirements

### REQ-005: Watch Method on YAMLLoader with Parent Directory Monitoring

The `YAMLLoader` type MUST expose a `Watch(ctx context.Context) error` method that watches the **parent directory** of the configured YAML path and automatically calls `Reload()` when file-system events occur on the target YAML file. Watch MUST be triggered by Create, Write, and Rename events whose base name matches the configured filename. Watch MUST NOT call `Reload()` for events on unrelated files in the parent directory. Watch MUST block until `ctx` is cancelled, then return `ctx.Err()`.

**Scenarios**:

- **Direct file write triggers reload**: GIVEN `Watch(ctx)` is running for a loader on `dir/users.yaml`, WHEN a new `users.yaml` is written via `os.WriteFile` with updated content, THEN `Reload()` is invoked and subsequent `Lookup` calls reflect the updated directory state.
- **Atomic rename triggers reload**: GIVEN `Watch(ctx)` is running, WHEN a temporary file is atomically renamed to `dir/users.yaml` via `os.Rename` (the admin dashboard's write strategy), THEN `Reload()` is invoked and the new directory is reflected in subsequent `Lookup` calls.
- **Unrelated file events do not trigger reload**: GIVEN `Watch(ctx)` is running for `dir/users.yaml`, WHEN an unrelated file like `dir/other.yaml` is written to or modified, THEN `Reload()` is NOT called and the loader state remains unchanged.
- **Context cancellation stops watching**: GIVEN `Watch(ctx)` is running, WHEN `ctx` is cancelled, THEN `Watch` returns cleanly (within 1 second) without resource leaks or goroutine hangs.

---

### REQ-006: Revoked User Is Denied After Watch-Triggered Reload

After a `Watch`-triggered `Reload()` call, the `HeaderAuthenticator` MUST deny requests from users whose `status` has been changed to `removed` or who have been deleted entirely from `users.yaml`. No server restart or manual SIGHUP signal is required; live revocation takes effect immediately.

**Scenarios**:

- **Removed user is denied on next request**: GIVEN a user `carol@vivastudios.com` is present with `status: active` at server start, WHEN `users.yaml` is updated to set `carol`'s `status: removed` and `Watch` triggers `Reload()`, THEN the next request from `carol` is denied by `HeaderAuthenticator.Authenticate` with an authorization error.
- **Deleted user is denied on next request**: GIVEN a user is enrolled and active, WHEN the user is removed entirely from `users.yaml` and `Watch` triggers `Reload()`, THEN the next request from that user is denied.
- **Remaining users continue to work**: GIVEN a directory with users alice and bob, WHEN carol is removed from the directory and reloaded, THEN alice and bob's requests continue to be processed normally.

---

### REQ-007: Watch Wired at Server Start

The `Watch(ctx)` method MUST be started as a goroutine in `defaultCloudRuntime.Start()`, running in parallel with the existing SIGHUP signal handler. The SIGHUP path MUST remain active as a belt-and-suspenders fallback for manual reload triggers. Both mechanisms MUST coexist without conflict.

**Scenarios**:

- **Watcher active without SIGHUP**: GIVEN `defaultCloudRuntime.Start()` was called and no SIGHUP has been sent, WHEN `users.yaml` is modified on disk, THEN `Reload()` is called automatically within the debounce window (approximately 100ms).
- **SIGHUP still works as fallback**: GIVEN the watcher is running, WHEN a SIGHUP signal is sent to the process, THEN the SIGHUP handler still triggers a `Reload()` as before.
- **Graceful shutdown cancels watcher**: GIVEN the watcher goroutine is running, WHEN `defaultCloudRuntime.Stop()` or equivalent shutdown is called, THEN the watcher goroutine exits cleanly and does not leak resources.

---

## Test Seam Summary

| REQ | Test name(s) |
|-----|-------------|
| REQ-005 | `TestWatcherTriggersReload_DirectWrite`, `TestWatcherTriggersReload_AtomicRename`, `TestWatcherIgnoresUnrelated` |
| REQ-006 | `TestHeaderAuth_RevokedUserDeniedAfterReload` |
| REQ-007 | Code inspection: `defaultCloudRuntime.Start()` starts Watch goroutine after SIGHUP goroutine; context management in cloud.go |
