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

	_, err := cs.db.ExecContext(ctx, `
		INSERT INTO cloud_client_versions (contributor, last_client_version, last_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (contributor) DO UPDATE
			SET last_client_version = EXCLUDED.last_client_version,
			    last_seen_at        = NOW()`,
		contributor, version,
	)
	if err != nil {
		return err
	}

	cs.invalidateDashboardReadModel()
	return nil
}
