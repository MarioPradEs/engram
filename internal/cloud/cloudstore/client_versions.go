package cloudstore

import (
	"context"
	"strings"
)

// RecordClientVersion persists the last-seen X-Engram-Client-Version value for
// the given contributor. It is an upsert: subsequent calls update the stored
// version and bump last_seen_at. No-ops silently on empty contributor or empty
// version strings.
//
// The read-model cache is only invalidated when the stored version actually
// changes. Sending the same version (common on every sync request) no longer
// thrashes the cache.
//
// Recording is best-effort telemetry and MUST NOT fail sync operations. Callers
// are expected to log and discard any returned error.
//
// Satisfies: REQ-CVR-05, REQ-CVR-06, REQ-CVR-09.
func (cs *CloudStore) RecordClientVersion(ctx context.Context, contributor, version string) error {
	contributor = strings.TrimSpace(contributor)
	version = strings.TrimSpace(version)
	if contributor == "" || version == "" {
		return nil
	}

	// The RETURNING clause emits a row only when an INSERT happens or when the
	// ON CONFLICT branch actually changes the stored value. If the stored
	// version is identical the WHERE clause suppresses the update, no row is
	// returned, and we skip the invalidation.
	rows, err := cs.db.QueryContext(ctx, `
		INSERT INTO cloud_client_versions (contributor, last_client_version, last_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (contributor) DO UPDATE
			SET last_client_version = EXCLUDED.last_client_version,
			    last_seen_at        = NOW()
		WHERE cloud_client_versions.last_client_version IS DISTINCT FROM EXCLUDED.last_client_version
		RETURNING contributor`,
		contributor, version,
	)
	if err != nil {
		return err
	}
	changed := rows.Next()
	_ = rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}

	if changed {
		cs.invalidateDashboardReadModel()
	}
	return nil
}
