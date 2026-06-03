package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// chunkGateBDrop records a personal-scope observation entry that was dropped
// during chunk materialization (Gate B, defense-in-depth mirror of InsertMutationBatch).
type chunkGateBDrop struct {
	project   string
	entityKey string
}

// applyChunkAttributionAndGateB processes a slice of materialized MutationEntry
// values from a chunk push, applying the same security layer as InsertMutationBatch:
//
//  1. Gate B (personal-drop): when attr.UserEmail is non-empty, observation entries
//     with scope=personal are silently dropped. The drop is recorded in the returned
//     drops slice so the caller can write audit log entries.
//
//  2. Attribution stamping: for kept observation entries, user_email / user_name /
//     department / user_deleted are merged into the JSONB payload using
//     stampAttributionIntoEntry (same helper used by InsertMutationBatch).
//
// When attr is zero (UserEmail == ""), Gate B is not applied and no stamping occurs —
// this preserves the pre-auth legacy behaviour for unauthenticated chunk pushes.
//
// Non-observation entries (session, prompt, relation) pass through unchanged.
func applyChunkAttributionAndGateB(entries []MutationEntry, attr Attribution) (kept []MutationEntry, drops []chunkGateBDrop) {
	if attr.UserEmail == "" {
		// Zero attribution → bypass Gate B and stamping.
		return entries, nil
	}

	kept = make([]MutationEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Entity) != store.SyncEntityObservation {
			// Non-observation entities pass through as-is.
			kept = append(kept, entry)
			continue
		}

		scope := extractScopeFromPayload(entry.Payload)
		if scope == "personal" {
			drops = append(drops, chunkGateBDrop{
				project:   strings.TrimSpace(entry.Project),
				entityKey: strings.TrimSpace(entry.EntityKey),
			})
			continue
		}

		// Stamp attribution into the JSONB payload (same helper as InsertMutationBatch).
		entry = stampAttributionIntoEntry(entry, attr)
		kept = append(kept, entry)
	}
	return kept, drops
}

// insertMaterializedMutationsWithAttribution inserts a slice of already-filtered,
// already-stamped MutationEntry values into cloud_mutations within the given
// transaction. For observation entries with a non-zero attr, the denorm columns
// (user_email, user_name, department, user_deleted) are set alongside the payload.
//
// This is the attribution-aware replacement for insertMaterializedMutations. The
// original insertMaterializedMutations is preserved for callsites that do not have
// attribution context (legacy paths, backfill, etc.).
func insertMaterializedMutationsWithAttribution(ctx context.Context, tx *sql.Tx, entries []MutationEntry, attr Attribution) error {
	for _, entry := range entries {
		payload := entry.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		project := strings.TrimSpace(entry.Project)
		entity := strings.TrimSpace(entry.Entity)
		entityKey := strings.TrimSpace(entry.EntityKey)
		op := strings.TrimSpace(entry.Op)

		if entity == store.SyncEntityObservation && attr.UserEmail != "" {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO cloud_mutations
					(project, entity, entity_key, op, payload,
					 user_email, user_name, department, user_deleted)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				project, entity, entityKey, op, payload,
				attr.UserEmail, attr.UserName, attr.Department, attr.UserDeleted,
			)
			if err != nil {
				return fmt.Errorf("cloudstore: insert materialized chunk mutation (attr) %s/%s/%s: %w",
					project, entity, entityKey, err)
			}
		} else {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO cloud_mutations (project, entity, entity_key, op, payload)
				VALUES ($1, $2, $3, $4, $5)`,
				project, entity, entityKey, op, payload,
			)
			if err != nil {
				return fmt.Errorf("cloudstore: insert materialized chunk mutation %s/%s/%s: %w",
					project, entity, entityKey, err)
			}
		}
	}
	return nil
}

// WriteChunkWithAttribution is the attribution-aware variant of WriteChunk.
// It applies the same security layer as InsertMutationBatch to the chunk-push path:
//
//   - Gate B (personal-drop): observation entries with scope=personal are silently
//     dropped when attr.UserEmail is non-empty. A cloud_sync_audit_log row with
//     outcome "rejected_personal_scope" is written for each dropped entry.
//   - Attribution stamping: kept observation entries have user_email / user_name /
//     department / user_deleted stamped into the JSONB payload AND into the denorm
//     columns of cloud_mutations.
//
// When attr is zero (UserEmail == ""), behaviour is identical to WriteChunk —
// no Gate B, no stamping. This preserves backward compatibility for unauthenticated
// or non-OAuth server deployments.
//
// The existing WriteChunk method is preserved and unchanged; it delegates to this
// method with a zero Attribution so all call sites remain valid.
func (cs *CloudStore) WriteChunkWithAttribution(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte, attr Attribution) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloudstore: project is required")
	}
	if strings.TrimSpace(chunkID) == "" {
		return fmt.Errorf("cloudstore: chunk id is required")
	}
	expectedChunkID := chunkIDFromPayload(payload)
	if chunkID != expectedChunkID {
		return fmt.Errorf("cloudstore: chunk id does not match payload hash (expected %s)", expectedChunkID)
	}
	originCreatedAt, err := parseClientCreatedAt(clientCreatedAt)
	if err != nil {
		return err
	}

	// Idempotency check: if the chunk already exists with the same payload, skip insert.
	var existingPayload []byte
	err = cs.db.QueryRowContext(ctx, `SELECT payload::text FROM cloud_chunks WHERE project_name = $1 AND chunk_id = $2`, project, chunkID).Scan(&existingPayload)
	if err == nil {
		normalizedIncoming := normalizeJSON(payload)
		normalizedExisting := normalizeJSON(existingPayload)
		if string(normalizedIncoming) != string(normalizedExisting) {
			return fmt.Errorf("%w: existing chunk %q has different payload", ErrChunkConflict, chunkID)
		}
		_ = cs.indexChunkSessions(ctx, project, payload)
		cs.invalidateDashboardReadModel()
		return nil
	}
	if err != nil && !isNoRows(err) {
		return fmt.Errorf("cloudstore: read existing chunk: %w", err)
	}

	chunk, err := parseChunkData(payload)
	if err != nil {
		return fmt.Errorf("cloudstore: parse chunk for materialization: %w", err)
	}

	// Materialize raw entries from the chunk (no Gate B or stamping yet).
	rawMutations, err := materializedChunkMutations(project, chunk)
	if err != nil {
		return err
	}

	// Apply Gate B + attribution stamping to produce the filtered/stamped set.
	mutations, drops := applyChunkAttributionAndGateB(rawMutations, attr)

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin write chunk tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	counts := summarizeChunk(payload)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO cloud_chunks (project_name, chunk_id, created_by, client_created_at, payload, sessions_count, observations_count, prompts_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		project, strings.TrimSpace(chunkID), strings.TrimSpace(createdBy), originCreatedAt, payload, counts.sessions, counts.observations, counts.prompts)
	if err != nil {
		if isUniqueViolation(err) {
			conflictErr := cs.resolveChunkConflict(ctx, project, chunkID, payload)
			if conflictErr != nil {
				return conflictErr
			}
			cs.invalidateDashboardReadModel()
			return nil
		}
		return fmt.Errorf("cloudstore: write chunk: %w", err)
	}
	if err := cs.indexChunkSessionsWith(ctx, tx, project, payload); err != nil {
		return err
	}
	if err := insertMaterializedMutationsWithAttribution(ctx, tx, mutations, attr); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit write chunk: %w", err)
	}
	tx = nil
	cs.invalidateDashboardReadModel()

	// Write audit log entries for Gate B drops (after commit — best-effort, non-fatal).
	// Mirror InsertMutationBatch's audit pattern.
	contributor := strings.TrimSpace(attr.UserEmail)
	if contributor == "" {
		contributor = "unknown"
	}
	for _, d := range drops {
		_ = cs.InsertAuditEntry(ctx, AuditEntry{
			Contributor: contributor,
			Project:     d.project,
			Action:      AuditActionChunkPush,
			Outcome:     AuditOutcomeRejectedPersonalScope,
			EntryCount:  1,
			ReasonCode:  "gate_b_personal_scope",
			Metadata:    map[string]any{"entity_key": d.entityKey},
		})
	}

	return nil
}

// isNoRows returns true if err is sql.ErrNoRows.
// Extracted as a helper to avoid importing errors in this file.
func isNoRows(err error) bool {
	return err == sql.ErrNoRows
}
