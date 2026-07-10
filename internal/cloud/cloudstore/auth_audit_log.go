package cloudstore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// ─── Outcome Constants ────────────────────────────────────────────────────────

// OutcomeAllowed is the outcome value for authentication events that were accepted.
const OutcomeAllowed = "allowed"

// OutcomeDenied is the outcome value for authentication events that were rejected.
const OutcomeDenied = "denied"

// ─── Source Constants ─────────────────────────────────────────────────────────

// SourceOAuth is the source value for events that went through the OAuth2-proxy
// dashboard flow (autoLoginFromHeader).
const SourceOAuth = "oauth"

// SourceJWT is the source value for events that presented a JWT credential.
const SourceJWT = "jwt"

// SourceLegacy is the source value for events that used the legacy
// ENGRAM_CLOUD_TOKEN emergency bypass path.
const SourceLegacy = "legacy"

// ─── Reason Code Constants ────────────────────────────────────────────────────

// ReasonUnknownEmail is used when an email is not found in the provisioned users.
const ReasonUnknownEmail = "unknown_email"

// ReasonInvalidJWT is used when a JWT fails signature verification or is expired.
const ReasonInvalidJWT = "invalid_jwt"

// ReasonRemovedUser is used when the user account has been removed.
const ReasonRemovedUser = "removed_user"

// ReasonAccountOffboarding is used when the user account is in offboarding state.
const ReasonAccountOffboarding = "account_offboarding"

// ReasonMissingCredential is used when no credential was presented at all.
const ReasonMissingCredential = "missing_credential"

// ReasonInvalidDomain is used when the email domain is not in the allowed list.
const ReasonInvalidDomain = "invalid_domain"

// ReasonBypassAdminMissing is used when the legacy bypass token is presented but
// no sole-admin account exists to accept it.
const ReasonBypassAdminMissing = "bypass_admin_missing"

// ─── Types ────────────────────────────────────────────────────────────────────

// AuthAuditEntry is the read-side struct returned from ListAuthAuditEntriesPaginated.
// Fixed columns only; no metadata or free-form fields.
type AuthAuditEntry struct {
	ID         int64
	OccurredAt string // RFC3339 UTC
	Email      string
	Outcome    string // OutcomeAllowed | OutcomeDenied
	Source     string // SourceOAuth | SourceJWT | SourceLegacy
	ReasonCode string // empty string when NULL (outcome=allowed)
}

// ─── CloudStore Methods ───────────────────────────────────────────────────────

// InsertAuthAuditEntry synchronously inserts one auth audit log row.
// reasonCode may be empty — it is stored as NULL when empty (required for
// outcome=allowed rows per spec).
// Returns an error; the caller decides whether to log and swallow or propagate.
func (cs *CloudStore) InsertAuthAuditEntry(ctx context.Context, email, outcome, source, reasonCode string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: InsertAuthAuditEntry: not initialized")
	}
	var rc *string
	if r := strings.TrimSpace(reasonCode); r != "" {
		rc = &r
	}
	_, err := cs.db.ExecContext(ctx, `
		INSERT INTO cloud_auth_audit_log (email, outcome, source, reason_code)
		VALUES ($1, $2, $3, $4)`,
		strings.TrimSpace(email),
		outcome,
		source,
		rc,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: InsertAuthAuditEntry: %w", err)
	}
	return nil
}

// ListAuthAuditEntriesPaginated returns a page of auth audit rows sorted
// occurred_at DESC, plus the total row count.
// page is 1-indexed; pageSize is the number of rows per page.
func (cs *CloudStore) ListAuthAuditEntriesPaginated(ctx context.Context, page, pageSize int) ([]AuthAuditEntry, int, error) {
	if cs == nil || cs.db == nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuthAuditEntriesPaginated: not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 500 {
		pageSize = 500
	}
	offset := (int64(page) - 1) * int64(pageSize)

	var total int
	if err := cs.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cloud_auth_audit_log`,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuthAuditEntriesPaginated count: %w", err)
	}

	dbRows, err := cs.db.QueryContext(ctx, `
		SELECT id, occurred_at, email, outcome, source, COALESCE(reason_code, '')
		FROM cloud_auth_audit_log
		ORDER BY occurred_at DESC
		LIMIT $1 OFFSET $2`,
		pageSize, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuthAuditEntriesPaginated query: %w", err)
	}
	defer dbRows.Close()

	var result []AuthAuditEntry
	for dbRows.Next() {
		var row AuthAuditEntry
		var occurredAt time.Time
		if err := dbRows.Scan(
			&row.ID,
			&occurredAt,
			&row.Email,
			&row.Outcome,
			&row.Source,
			&row.ReasonCode,
		); err != nil {
			return nil, 0, fmt.Errorf("cloudstore: ListAuthAuditEntriesPaginated scan: %w", err)
		}
		row.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		result = append(result, row)
	}
	if err := dbRows.Err(); err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuthAuditEntriesPaginated iterate: %w", err)
	}
	if result == nil {
		result = []AuthAuditEntry{}
	}
	return result, total, nil
}

// PruneAuthAuditBefore deletes all rows from cloud_auth_audit_log whose
// occurred_at is strictly before cutoff. Returns the number of deleted rows.
// This MUST NOT touch cloud_sync_audit_log or any other table.
func (cs *CloudStore) PruneAuthAuditBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if cs == nil || cs.db == nil {
		return 0, fmt.Errorf("cloudstore: PruneAuthAuditBefore: not initialized")
	}
	result, err := cs.db.ExecContext(ctx,
		`DELETE FROM cloud_auth_audit_log WHERE occurred_at < $1`,
		cutoff.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("cloudstore: PruneAuthAuditBefore: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cloudstore: PruneAuthAuditBefore: rows affected: %w", err)
	}
	return n, nil
}

// RecordAuthEvent is the fire-and-forget wrapper used by auth instrumentation
// (PR2). It spawns a goroutine and calls InsertAuthAuditEntry; DB failures are
// logged at WARN and swallowed — they MUST NOT propagate to the auth path.
// email is captured by value before the goroutine starts (no request-scope capture).
//
// ctx is checked ONLY before spawning the goroutine: if the context is already
// cancelled or has expired (e.g. server shutdown in progress), the write is
// suppressed entirely. The goroutine itself uses context.Background() for the
// actual insert because the request context will be cancelled by the time the
// goroutine runs.
func (cs *CloudStore) RecordAuthEvent(ctx context.Context, email, outcome, source, reasonCode string) {
	// Suppress write if the context signals shutdown or cancellation before we spawn.
	if ctx != nil && ctx.Err() != nil {
		return
	}

	// Capture all values before spawning to avoid capturing a mutable request context.
	emailCopy := email
	outcomeCopy := outcome
	sourceCopy := source
	reasonCodeCopy := reasonCode

	go func() {
		if err := cs.InsertAuthAuditEntry(context.Background(), emailCopy, outcomeCopy, sourceCopy, reasonCodeCopy); err != nil {
			log.Printf("WARN cloudstore: RecordAuthEvent: %v", err)
		}
	}()
}
