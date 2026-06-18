package cloudstore

// deletion_requests.go — per-observation deletion-request store methods (D5).
//
// Guiding principle (spec §guiding-principle):
// By default, nothing is deleted. Memory evolves through CORRECTIONS (observation
// revisions via revision_count / mem_update). Hard deletion is the RARE EXCEPTION
// reserved for content that must actually disappear because it is sensitive or
// erroneous — not the normal knowledge-maintenance mechanism.
//
// Hard-delete implementation:
//   Accept inserts a cloud_mutations row with op=delete + hard_delete=true for the
//   targeted sync_id, then calls invalidateDashboardReadModel. On the next read-model
//   rebuild, applyDashboardMutation processes the delete mutation and removes the
//   observation from the in-memory map — exactly the same path as the sync client's
//   own delete/hard-delete operation. Purely additive: no DROP or ALTER on any
//   existing table.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrRequestAlreadyDecided is returned when an admin attempts to accept or reject
// a deletion request that has already been decided (status != "pending").
var ErrRequestAlreadyDecided = errors.New("cloudstore: deletion request already decided")

// ErrDeletionRequestConflict is returned when a member tries to submit a pending
// deletion request for a sync_id that already has one in "pending" status.
var ErrDeletionRequestConflict = errors.New("cloudstore: deletion request already pending for this observation")

// ErrDeletionRequestNotFound is returned when GetDeletionRequest cannot find a row.
var ErrDeletionRequestNotFound = errors.New("cloudstore: deletion request not found")

// DeletionRequest is the input type for CreateDeletionRequest.
type DeletionRequest struct {
	TargetSyncID   string // sync_id of the observation to delete
	TargetChunk    string // optional chunk reference
	RequesterEmail string // verified email of the requesting member
	Reason         string // optional member-supplied reason
}

// StoredDeletionRequest is the row returned from the database.
type StoredDeletionRequest struct {
	ID             int64
	TargetSyncID   string
	TargetChunk    string
	RequesterEmail string
	Reason         string
	Status         string     // pending | accepted | rejected
	DecidedBy      string     // email of the deciding admin; empty when pending
	RequestedAt    time.Time
	DecidedAt      *time.Time // nil when pending
}

// CreateDeletionRequest inserts a new pending deletion request.
// Returns ErrDeletionRequestConflict when a pending request already exists for
// the same target_sync_id (enforced by the uq_cdr_pending partial unique index).
func (cs *CloudStore) CreateDeletionRequest(ctx context.Context, req DeletionRequest) (int64, error) {
	if cs == nil || cs.db == nil {
		return 0, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		INSERT INTO cloud_deletion_requests
			(target_sync_id, target_chunk, requester_email, reason)
		VALUES ($1, $2, $3, $4)
		RETURNING id`
	var id int64
	err := cs.db.QueryRowContext(ctx, q,
		strings.TrimSpace(req.TargetSyncID),
		strings.TrimSpace(req.TargetChunk),
		strings.ToLower(strings.TrimSpace(req.RequesterEmail)),
		strings.TrimSpace(req.Reason),
	).Scan(&id)
	if err != nil {
		// Partial unique index violation on (target_sync_id) WHERE status='pending'
		// manifests as a unique_violation (pg error code 23505).
		if isDuplicateKeyError(err) {
			return 0, fmt.Errorf("%w: sync_id=%q", ErrDeletionRequestConflict, req.TargetSyncID)
		}
		return 0, fmt.Errorf("cloudstore: create deletion request: %w", err)
	}
	return id, nil
}

// GetDeletionRequest returns a single deletion request row by id.
func (cs *CloudStore) GetDeletionRequest(ctx context.Context, id int64) (StoredDeletionRequest, error) {
	if cs == nil || cs.db == nil {
		return StoredDeletionRequest{}, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT id, target_sync_id, COALESCE(target_chunk,''), requester_email,
		       COALESCE(reason,''), status, COALESCE(decided_by,''), requested_at, decided_at
		FROM cloud_deletion_requests
		WHERE id = $1`
	var row StoredDeletionRequest
	err := cs.db.QueryRowContext(ctx, q, id).Scan(
		&row.ID, &row.TargetSyncID, &row.TargetChunk,
		&row.RequesterEmail, &row.Reason, &row.Status,
		&row.DecidedBy, &row.RequestedAt, &row.DecidedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredDeletionRequest{}, fmt.Errorf("%w: id=%d", ErrDeletionRequestNotFound, id)
	}
	if err != nil {
		return StoredDeletionRequest{}, fmt.Errorf("cloudstore: get deletion request: %w", err)
	}
	return row, nil
}

// ListPendingDeletionRequests returns all rows with status='pending', ordered by requested_at ASC.
func (cs *CloudStore) ListPendingDeletionRequests(ctx context.Context) ([]StoredDeletionRequest, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT id, target_sync_id, COALESCE(target_chunk,''), requester_email,
		       COALESCE(reason,''), status, COALESCE(decided_by,''), requested_at, decided_at
		FROM cloud_deletion_requests
		WHERE status = 'pending'
		ORDER BY requested_at ASC`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list pending deletion requests: %w", err)
	}
	defer rows.Close()
	var out []StoredDeletionRequest
	for rows.Next() {
		var row StoredDeletionRequest
		if err := rows.Scan(
			&row.ID, &row.TargetSyncID, &row.TargetChunk,
			&row.RequesterEmail, &row.Reason, &row.Status,
			&row.DecidedBy, &row.RequestedAt, &row.DecidedAt,
		); err != nil {
			return nil, fmt.Errorf("cloudstore: scan pending deletion request: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate pending deletion requests: %w", err)
	}
	return out, nil
}

// ListDeletionRequestsForRequester returns all rows where requester_email = email.
// Used by the member-facing decision notice (spec §notification).
func (cs *CloudStore) ListDeletionRequestsForRequester(ctx context.Context, email string) ([]StoredDeletionRequest, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT id, target_sync_id, COALESCE(target_chunk,''), requester_email,
		       COALESCE(reason,''), status, COALESCE(decided_by,''), requested_at, decided_at
		FROM cloud_deletion_requests
		WHERE requester_email = $1
		ORDER BY requested_at DESC`
	rows, err := cs.db.QueryContext(ctx, q, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list deletion requests for requester: %w", err)
	}
	defer rows.Close()
	var out []StoredDeletionRequest
	for rows.Next() {
		var row StoredDeletionRequest
		if err := rows.Scan(
			&row.ID, &row.TargetSyncID, &row.TargetChunk,
			&row.RequesterEmail, &row.Reason, &row.Status,
			&row.DecidedBy, &row.RequestedAt, &row.DecidedAt,
		); err != nil {
			return nil, fmt.Errorf("cloudstore: scan deletion request for requester: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate deletion requests for requester: %w", err)
	}
	return out, nil
}

// AcceptDeletionRequest hard-deletes the targeted observation and marks the request accepted.
//
// Hard-delete mechanism: insert a cloud_mutations row with op=delete + hard_delete=true
// for the targeted sync_id, then invalidate the dashboard read model so the next rebuild
// applies the delete mutation via applyDashboardMutation (dashboard_queries.go, delete path).
// If the observation is already absent (0 mutation rows inserted or already deleted),
// the accept is still recorded and no error is returned (spec §graceful).
//
// Returns ErrRequestAlreadyDecided when status != "pending".
func (cs *CloudStore) AcceptDeletionRequest(ctx context.Context, id int64, adminEmail string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}

	// Load the request to get target_sync_id and verify pending state.
	row, err := cs.GetDeletionRequest(ctx, id)
	if err != nil {
		return err
	}
	if row.Status != "pending" {
		return fmt.Errorf("%w: id=%d status=%q", ErrRequestAlreadyDecided, id, row.Status)
	}

	// Begin transaction: hard-delete mutation + status update atomically.
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: accept deletion request: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Resolve the observation's real project so the hard-delete mutation is keyed
	// correctly. applyDashboardMutation keys observations by (project, syncID); if
	// project is empty the delete never matches and the observation stays visible.
	//
	// C2 fix: look up the observation in the current read model to get its project.
	// We use admin scope (IsAdmin=true) so we see all projects. If the observation is
	// already absent (already deleted or never existed) we proceed gracefully — the
	// Accept is still recorded and the mutation is a no-op on rebuild.
	resolvedProject := ""
	adminScope := &ReadScope{IsAdmin: true}
	if existingObs, lookupErr := cs.GetObservationBySyncID(adminScope, strings.TrimSpace(row.TargetSyncID)); lookupErr == nil {
		resolvedProject = strings.TrimSpace(existingObs.Project)
	}
	// If the observation is already gone (lookupErr != nil), resolvedProject stays "".
	// The mutation will still be inserted and will be a no-op on rebuild — matching
	// the spec §graceful: "observation already absent → accept is still recorded".

	// Insert the hard-delete mutation. This is the same mutation format the sync client
	// uses: entity=observation, op=delete, payload contains sync_id + hard_delete=true.
	// On the next read-model rebuild, applyDashboardMutation will delete(observations, key).
	hardDeletePayload := fmt.Sprintf(`{"sync_id":%q,"deleted":true,"hard_delete":true,"project":%q}`,
		strings.TrimSpace(row.TargetSyncID), resolvedProject)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO cloud_mutations (project, entity, entity_key, op, payload, user_email)
		VALUES ($1, 'observation', $2, 'delete', $3::jsonb, $4)`,
		resolvedProject,
		strings.TrimSpace(row.TargetSyncID),
		hardDeletePayload,
		strings.ToLower(strings.TrimSpace(adminEmail)),
	)
	if err != nil {
		return fmt.Errorf("cloudstore: accept deletion request: insert hard-delete mutation: %w", err)
	}

	// Mark request accepted.
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		UPDATE cloud_deletion_requests
		SET status = 'accepted', decided_by = $1, decided_at = $2
		WHERE id = $3 AND status = 'pending'`,
		strings.ToLower(strings.TrimSpace(adminEmail)), now, id,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: accept deletion request: update status: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: accept deletion request: commit: %w", err)
	}

	// Invalidate the read model so the hard-delete mutation is picked up on the next load.
	cs.invalidateDashboardReadModel()
	return nil
}

// RejectDeletionRequest marks a pending request as rejected without touching the observation.
// Returns ErrRequestAlreadyDecided when status != "pending".
func (cs *CloudStore) RejectDeletionRequest(ctx context.Context, id int64, adminEmail string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}

	now := time.Now().UTC()
	result, err := cs.db.ExecContext(ctx, `
		UPDATE cloud_deletion_requests
		SET status = 'rejected', decided_by = $1, decided_at = $2
		WHERE id = $3 AND status = 'pending'`,
		strings.ToLower(strings.TrimSpace(adminEmail)), now, id,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: reject deletion request: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		// Either not found or already decided.
		_, lookupErr := cs.GetDeletionRequest(ctx, id)
		if errors.Is(lookupErr, ErrDeletionRequestNotFound) {
			return lookupErr
		}
		return fmt.Errorf("%w: id=%d", ErrRequestAlreadyDecided, id)
	}
	return nil
}

// PendingDeletionRequestCount returns the count of rows with status='pending'.
// Used for the admin pending-badge notification (spec §notification).
func (cs *CloudStore) PendingDeletionRequestCount(ctx context.Context) (int, error) {
	if cs == nil || cs.db == nil {
		return 0, fmt.Errorf("cloudstore: not initialized")
	}
	var count int
	err := cs.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cloud_deletion_requests WHERE status = 'pending'`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("cloudstore: pending deletion request count: %w", err)
	}
	return count, nil
}

// isDuplicateKeyError returns true when err is a PostgreSQL unique_violation (23505).
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	// pgconn.PgError implements the error interface and has Code "23505" for unique_violation.
	// We check by string to avoid importing jackc/pgx/v5/pgconn in this file.
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key")
}
