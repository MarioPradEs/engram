# Live User Directory Reload Specification (D3)

## Purpose

`YAMLLoader` gains a `Watch(ctx context.Context)` method backed by fsnotify that calls
`Reload()` automatically when `users.yaml` changes on disk — including via atomic rename
(the admin dashboard's write strategy). No SIGHUP required for live revocation.

## Requirements

### Requirement: Watch Method on YAMLLoader

`YAMLLoader` MUST expose `Watch(ctx context.Context) error` that watches the **parent
directory** of the configured YAML path and calls `Reload()` on file-system events
whose base name matches the configured filename. `Watch` MUST block until `ctx` is
cancelled, then return. It MUST NOT call `Reload()` for events on unrelated files.

#### Scenario: Direct file write triggers reload

- GIVEN `Watch(ctx)` is running for a loader on `dir/users.yaml`
- WHEN a new `users.yaml` is written via `os.WriteFile`
- THEN `Reload()` is invoked and `Lookup` returns the updated directory state

#### Scenario: Atomic rename triggers reload

- GIVEN `Watch(ctx)` is running
- WHEN a temp file is atomically renamed to `dir/users.yaml` via `os.Rename`
- THEN `Reload()` is invoked and the new directory is reflected in `Lookup`

#### Scenario: Unrelated file events do not trigger reload

- GIVEN `Watch(ctx)` is running for `dir/users.yaml`
- WHEN `dir/other.yaml` is written
- THEN `Reload()` is NOT called

### Requirement: Revoked User Is Denied After Watch-Triggered Reload

After a watch-triggered `Reload()`, `HeaderAuthenticator` MUST deny requests from
users whose `status` is `removed` or who have been deleted from `users.yaml`. No
server restart is required.

#### Scenario: Removed user is denied on next sync

- GIVEN a user `carol@vivastudios.com` is present with `status: active` at server start
- WHEN `users.yaml` is updated to set `carol`'s `status: removed` and `Watch` triggers `Reload()`
- THEN `HeaderAuthenticator.Authenticate` returns an authorization error for `carol`'s request

### Requirement: Watch Wired at Server Start

The system MUST start `Watch(ctx)` as a goroutine in `defaultCloudRuntime.Start()`,
alongside the existing SIGHUP goroutine. The SIGHUP path MUST remain active as a
belt-and-suspenders fallback.

#### Scenario: Watcher active without SIGHUP

- GIVEN `defaultCloudRuntime.Start()` was called and no SIGHUP has been sent
- WHEN `users.yaml` is modified on disk
- THEN `Reload()` is called automatically (no manual signal required)

## Test File Targets

| Test | File | Type |
|------|------|------|
| Watch direct-write | `internal/cloud/users/loader_test.go` | New test |
| Watch atomic-rename | `internal/cloud/users/loader_test.go` | New test |
| Watch ignores unrelated file | `internal/cloud/users/loader_test.go` | New test |
| Revoked user → 403 after reload | `internal/cloud/auth/header_auth_test.go` | New test |

Note: `TestReloadUpdatesDirectory` and `TestReloadInvalidRetainsLastGood` already exist
in `loader_test.go` and cover the `Reload()` contract. New Watch tests build on top.
