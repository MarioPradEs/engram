package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type CloudStore struct {
	db                     *sql.DB
	dashboardAllowedScopes map[string]struct{}
	dashboardAllowedAll    bool
	dashboardReadModelMu   sync.RWMutex
	dashboardReadModel     dashboardReadModel
	dashboardReadModelOK   bool
	dashboardReadModelLoad func() (dashboardReadModel, error)
}

var ErrChunkNotFound = errors.New("cloudstore: chunk not found")
var ErrChunkConflict = errors.New("cloudstore: chunk id conflict")

func New(cfg cloud.Config) (*CloudStore, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, fmt.Errorf("cloudstore: database dsn is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: open postgres: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cloudstore: ping postgres: %w", err)
	}
	store := &CloudStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (cs *CloudStore) Close() error {
	if cs == nil || cs.db == nil {
		return nil
	}
	return cs.db.Close()
}

func (cs *CloudStore) SetDashboardAllowedProjects(projects []string) {
	if cs == nil {
		return
	}
	cs.dashboardAllowedAll = false
	cs.dashboardAllowedScopes = make(map[string]struct{})
	for _, project := range projects {
		project = strings.TrimSpace(project)
		if project == "*" {
			cs.dashboardAllowedAll = true
			cs.dashboardAllowedScopes = nil
			cs.invalidateDashboardReadModel()
			return
		}
		if project == "" {
			continue
		}
		cs.dashboardAllowedScopes[project] = struct{}{}
	}
	cs.invalidateDashboardReadModel()
}

type User struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
}

func (cs *CloudStore) CreateUser(username, email, _ string) (*User, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		INSERT INTO cloud_users (username, email, password_hash)
		VALUES ($1, $2, '')
		ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email
		RETURNING id::text, username, email, password_hash`
	var u User
	if err := cs.db.QueryRowContext(context.Background(), q, strings.TrimSpace(username), strings.TrimSpace(email)).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash); err != nil {
		return nil, fmt.Errorf("cloudstore: create user: %w", err)
	}
	return &u, nil
}

func (cs *CloudStore) GetUserByUsername(username string) (*User, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT id::text, username, email, password_hash FROM cloud_users WHERE username = $1`
	var u User
	err := cs.db.QueryRowContext(context.Background(), q, strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: lookup user by username: %w", err)
	}
	return &u, nil
}

func (cs *CloudStore) GetUserByEmail(email string) (*User, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT id::text, username, email, password_hash FROM cloud_users WHERE email = $1`
	var u User
	err := cs.db.QueryRowContext(context.Background(), q, strings.TrimSpace(email)).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: lookup user by email: %w", err)
	}
	return &u, nil
}

func (cs *CloudStore) ReadManifest(ctx context.Context, project string) (*engramsync.Manifest, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}
	rows, err := cs.db.QueryContext(ctx, `
		SELECT chunk_id, created_by, COALESCE(client_created_at, created_at) AS manifest_created_at, sessions_count, observations_count, prompts_count, created_at
		FROM cloud_chunks
		WHERE project_name = $1
		ORDER BY created_at ASC, chunk_id ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: query manifest: %w", err)
	}
	defer rows.Close()

	manifestRows := make([]manifestRow, 0)
	for rows.Next() {
		var row manifestRow
		if err := rows.Scan(&row.chunkID, &row.createdBy, &row.manifestTime, &row.sessions, &row.observations, &row.prompts, &row.serverCreated); err != nil {
			return nil, fmt.Errorf("cloudstore: scan manifest: %w", err)
		}
		manifestRows = append(manifestRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate manifest: %w", err)
	}
	return &engramsync.Manifest{Version: 1, Chunks: toManifestEntries(manifestRows)}, nil
}

type manifestRow struct {
	chunkID       string
	createdBy     string
	manifestTime  time.Time
	sessions      int
	observations  int
	prompts       int
	serverCreated time.Time
}

func toManifestEntries(rows []manifestRow) []engramsync.ChunkEntry {
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if !left.serverCreated.Equal(right.serverCreated) {
			return left.serverCreated.Before(right.serverCreated)
		}
		return left.chunkID < right.chunkID
	})
	entries := make([]engramsync.ChunkEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, engramsync.ChunkEntry{
			ID:        row.chunkID,
			CreatedBy: row.createdBy,
			CreatedAt: row.manifestTime.UTC().Format(time.RFC3339),
			Sessions:  row.sessions,
			Memories:  row.observations,
			Prompts:   row.prompts,
		})
	}
	return entries
}

func (cs *CloudStore) WriteChunk(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte) error {
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("cloudstore: read existing chunk: %w", err)
	}

	chunk, err := parseChunkData(payload)
	if err != nil {
		return fmt.Errorf("cloudstore: parse chunk for materialization: %w", err)
	}
	mutations, err := materializedChunkMutations(project, chunk)
	if err != nil {
		return err
	}

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
	if err := insertMaterializedMutations(ctx, tx, mutations); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit write chunk: %w", err)
	}
	tx = nil
	cs.invalidateDashboardReadModel()
	return nil
}

func (cs *CloudStore) invalidateDashboardReadModel() {
	if cs == nil {
		return
	}
	cs.dashboardReadModelMu.Lock()
	defer cs.dashboardReadModelMu.Unlock()
	cs.dashboardReadModel = dashboardReadModel{}
	cs.dashboardReadModelOK = false
}

func (cs *CloudStore) KnownSessionIDs(ctx context.Context, project string) (map[string]struct{}, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}
	rows, err := cs.db.QueryContext(ctx, `SELECT session_id FROM cloud_project_sessions WHERE project_name = $1`, project)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: query session index: %w", err)
	}
	defer rows.Close()

	known := make(map[string]struct{})
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, fmt.Errorf("cloudstore: scan session index: %w", err)
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		known[sessionID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate session index: %w", err)
	}
	return known, nil
}

func (cs *CloudStore) indexChunkSessions(ctx context.Context, project string, payload []byte) error {
	return cs.indexChunkSessionsWith(ctx, cs.db, project, payload)
}

type chunkSessionIndexer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (cs *CloudStore) indexChunkSessionsWith(ctx context.Context, execer chunkSessionIndexer, project string, payload []byte) error {
	sessionIDs := collectSessionIDsFromPayload(payload)
	if len(sessionIDs) == 0 {
		return nil
	}
	for sessionID := range sessionIDs {
		if _, err := execer.ExecContext(ctx,
			`INSERT INTO cloud_project_sessions (project_name, session_id) VALUES ($1, $2) ON CONFLICT (project_name, session_id) DO NOTHING`,
			project, sessionID,
		); err != nil {
			return fmt.Errorf("cloudstore: index session %q: %w", sessionID, err)
		}
	}
	return nil
}

func materializedChunkMutations(project string, chunk engramsync.ChunkData) ([]MutationEntry, error) {
	project = strings.TrimSpace(project)
	entries := make([]MutationEntry, 0, len(chunk.Sessions)+len(chunk.Observations)+len(chunk.Prompts))

	for i, session := range chunk.Sessions {
		entityKey := strings.TrimSpace(session.ID)
		if entityKey == "" {
			return nil, fmt.Errorf("cloudstore: materialize chunk: sessions[%d].id is required", i)
		}
		payload, err := json.Marshal(session)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: materialize chunk session %q: %w", entityKey, err)
		}
		entries = append(entries, MutationEntry{Project: project, Entity: store.SyncEntitySession, EntityKey: entityKey, Op: store.SyncOpUpsert, Payload: payload})
	}

	for i, observation := range chunk.Observations {
		entityKey := strings.TrimSpace(observation.SyncID)
		if entityKey == "" {
			return nil, fmt.Errorf("cloudstore: materialize chunk: observations[%d].sync_id is required", i)
		}
		payload, err := json.Marshal(observation)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: materialize chunk observation %q: %w", entityKey, err)
		}
		entries = append(entries, MutationEntry{Project: project, Entity: store.SyncEntityObservation, EntityKey: entityKey, Op: store.SyncOpUpsert, Payload: payload})
	}

	for i, prompt := range chunk.Prompts {
		entityKey := strings.TrimSpace(prompt.SyncID)
		if entityKey == "" {
			return nil, fmt.Errorf("cloudstore: materialize chunk: prompts[%d].sync_id is required", i)
		}
		payload, err := json.Marshal(prompt)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: materialize chunk prompt %q: %w", entityKey, err)
		}
		entries = append(entries, MutationEntry{Project: project, Entity: store.SyncEntityPrompt, EntityKey: entityKey, Op: store.SyncOpUpsert, Payload: payload})
	}

	return entries, nil
}

func insertMaterializedMutations(ctx context.Context, tx *sql.Tx, entries []MutationEntry) error {
	for _, entry := range entries {
		payload := entry.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cloud_mutations (project, entity, entity_key, op, payload)
			VALUES ($1, $2, $3, $4, $5)`,
			strings.TrimSpace(entry.Project), strings.TrimSpace(entry.Entity), strings.TrimSpace(entry.EntityKey), strings.TrimSpace(entry.Op), payload,
		)
		if err != nil {
			return fmt.Errorf("cloudstore: insert materialized chunk mutation %s/%s/%s: %w", entry.Project, entry.Entity, entry.EntityKey, err)
		}
	}
	return nil
}

func (cs *CloudStore) backfillProjectSessionsFromChunks(ctx context.Context) error {
	rows, err := cs.db.QueryContext(ctx, `SELECT project_name, payload FROM cloud_chunks`)
	if err != nil {
		return fmt.Errorf("cloudstore: backfill session index: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var project string
		var payload []byte
		if err := rows.Scan(&project, &payload); err != nil {
			return fmt.Errorf("cloudstore: backfill session index scan: %w", err)
		}
		if err := cs.indexChunkSessions(ctx, project, payload); err != nil {
			return fmt.Errorf("cloudstore: backfill session index row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cloudstore: backfill session index iterate: %w", err)
	}
	return nil
}

func collectSessionIDsFromPayload(payload []byte) map[string]struct{} {
	chunk, err := parseChunkData(payload)
	if err != nil {
		return map[string]struct{}{}
	}
	return collectSessionIDs(chunk)
}

func parseChunkData(payload []byte) (engramsync.ChunkData, error) {
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return engramsync.ChunkData{}, err
	}
	return chunk, nil
}

func collectSessionIDs(chunk engramsync.ChunkData) map[string]struct{} {
	sessionIDs := make(map[string]struct{})
	for _, session := range chunk.Sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID != "" {
			sessionIDs[sessionID] = struct{}{}
		}
	}
	for _, mutation := range chunk.Mutations {
		if mutation.Entity != "session" || mutation.Op == "delete" {
			continue
		}
		mutationPayload := strings.TrimSpace(mutation.Payload)
		if mutationPayload == "" {
			continue
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := chunkcodec.DecodeSyncMutationPayload(mutationPayload, &body); err != nil {
			continue
		}
		sessionID := strings.TrimSpace(body.ID)
		if sessionID != "" {
			sessionIDs[sessionID] = struct{}{}
		}
	}
	return sessionIDs
}

func (cs *CloudStore) resolveChunkConflict(ctx context.Context, project, chunkID string, payload []byte) error {
	var existingPayload []byte
	err := cs.db.QueryRowContext(ctx, `SELECT payload::text FROM cloud_chunks WHERE project_name = $1 AND chunk_id = $2`, project, chunkID).Scan(&existingPayload)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: existing chunk %q was concurrently inserted", ErrChunkConflict, chunkID)
	}
	if err != nil {
		return fmt.Errorf("cloudstore: resolve chunk conflict: %w", err)
	}
	normalizedIncoming := normalizeJSON(payload)
	normalizedExisting := normalizeJSON(existingPayload)
	if string(normalizedIncoming) == string(normalizedExisting) {
		return nil
	}
	return fmt.Errorf("%w: existing chunk %q has different payload", ErrChunkConflict, chunkID)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505"
}

func (cs *CloudStore) ReadChunk(ctx context.Context, project, chunkID string) ([]byte, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}
	var payload []byte
	err := cs.db.QueryRowContext(ctx, `SELECT payload FROM cloud_chunks WHERE project_name = $1 AND chunk_id = $2`, project, strings.TrimSpace(chunkID)).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrChunkNotFound, chunkID)
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: read chunk: %w", err)
	}
	return payload, nil
}

func (cs *CloudStore) migrate(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS cloud_users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS cloud_chunks (
			project_name TEXT NOT NULL DEFAULT 'default',
			chunk_id TEXT NOT NULL,
			created_by TEXT NOT NULL,
			client_created_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			payload JSONB NOT NULL,
			sessions_count INTEGER NOT NULL DEFAULT 0,
			observations_count INTEGER NOT NULL DEFAULT 0,
			prompts_count INTEGER NOT NULL DEFAULT 0
		)`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS project_name TEXT`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS client_created_at TIMESTAMPTZ`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS sessions_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS observations_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS prompts_count INTEGER NOT NULL DEFAULT 0`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'cloud_chunks' AND column_name = 'imported_at'
			) THEN
				EXECUTE 'UPDATE cloud_chunks SET created_at = imported_at WHERE imported_at IS NOT NULL AND created_at IS NULL';
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'cloud_chunks' AND column_name = 'sessions'
			) THEN
				EXECUTE 'UPDATE cloud_chunks SET sessions_count = sessions WHERE sessions_count = 0 AND sessions IS NOT NULL';
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'cloud_chunks' AND column_name = 'memories'
			) THEN
				EXECUTE 'UPDATE cloud_chunks SET observations_count = memories WHERE observations_count = 0 AND memories IS NOT NULL';
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'cloud_chunks' AND column_name = 'prompts'
			) THEN
				EXECUTE 'UPDATE cloud_chunks SET prompts_count = prompts WHERE prompts_count = 0 AND prompts IS NOT NULL';
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'cloud_chunks' AND column_name = 'user_id'
			) THEN
				ALTER TABLE cloud_chunks ALTER COLUMN user_id DROP NOT NULL;
			END IF;
		END $$`,
		`UPDATE cloud_chunks SET project_name = 'default' WHERE project_name IS NULL OR btrim(project_name) = ''`,
		`ALTER TABLE cloud_chunks ALTER COLUMN project_name SET NOT NULL`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'cloud_chunks_pkey' AND conrelid = 'cloud_chunks'::regclass
			) THEN
				ALTER TABLE cloud_chunks DROP CONSTRAINT cloud_chunks_pkey;
			END IF;
		END $$`,
		`CREATE UNIQUE INDEX IF NOT EXISTS cloud_chunks_project_chunk_uidx ON cloud_chunks (project_name, chunk_id)`,
		`CREATE TABLE IF NOT EXISTS cloud_project_sessions (
			project_name TEXT NOT NULL,
			session_id TEXT NOT NULL,
			indexed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (project_name, session_id)
		)`,
		`INSERT INTO cloud_project_sessions (project_name, session_id)
		 SELECT c.project_name, btrim(elem->>'id')
		 FROM cloud_chunks c,
		      jsonb_array_elements(COALESCE(c.payload->'sessions', '[]'::jsonb)) AS elem
		 WHERE btrim(COALESCE(elem->>'id', '')) <> ''
		 ON CONFLICT (project_name, session_id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS cloud_project_controls (
		    project       TEXT PRIMARY KEY,
		    sync_enabled  BOOLEAN NOT NULL DEFAULT TRUE,
		    paused_reason TEXT,
		    updated_by    TEXT,
		    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'cloud_project_controls'
				  AND column_name = 'updated_by'
				  AND udt_name = 'uuid'
			) THEN
				ALTER TABLE cloud_project_controls DROP CONSTRAINT IF EXISTS cloud_project_controls_updated_by_fkey;
				ALTER TABLE cloud_project_controls ALTER COLUMN updated_by TYPE TEXT USING updated_by::text;
			END IF;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_cloud_project_controls_enabled ON cloud_project_controls(sync_enabled)`,
		// cloud_mutations: journal for fine-grained mutation sync (REQ-200, REQ-201).
		`CREATE TABLE IF NOT EXISTS cloud_mutations (
			seq        BIGSERIAL PRIMARY KEY,
			project    TEXT NOT NULL,
			entity     TEXT NOT NULL,
			entity_key TEXT NOT NULL,
			op         TEXT NOT NULL,
			payload    JSONB NOT NULL DEFAULT '{}',
			occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS project TEXT`,
		`UPDATE cloud_mutations SET project = 'default' WHERE project IS NULL OR btrim(project) = ''`,
		`ALTER TABLE cloud_mutations ALTER COLUMN project SET NOT NULL`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'cloud_mutations' AND column_name = 'user_id'
			) THEN
				ALTER TABLE cloud_mutations ALTER COLUMN user_id DROP NOT NULL;
			END IF;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_project ON cloud_mutations(project)`,
		`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_seq ON cloud_mutations(seq)`,
		// cloud_sync_audit_log: persistent audit trail for push-rejection events (REQ-400).
		`CREATE TABLE IF NOT EXISTS cloud_sync_audit_log (
			id           SERIAL PRIMARY KEY,
			occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			contributor  TEXT NOT NULL,
			project      TEXT NOT NULL,
			action       TEXT NOT NULL,
			outcome      TEXT NOT NULL,
			entry_count  INT NOT NULL DEFAULT 0,
			reason_code  TEXT,
			metadata     JSONB
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_occurred_at ON cloud_sync_audit_log (occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_contributor_project ON cloud_sync_audit_log (contributor, project)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_outcome ON cloud_sync_audit_log (outcome)`,

		// ── Phase: OAuth & user attribution — denorm columns on cloud_mutations ──
		// These columns denormalize attribution data extracted from the JSONB payload
		// for efficient querying (scope filter, audit queries).
		`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS user_email  TEXT`,
		`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS user_name   TEXT`,
		`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS department  TEXT`,
		`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS user_deleted BOOLEAN NOT NULL DEFAULT FALSE`,

		// Indices for scope-filter and audit queries on attribution columns.
		`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_user_email ON cloud_mutations(user_email)`,
		`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_department  ON cloud_mutations(department)`,
	}
	for _, q := range queries {
		if _, err := cs.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("cloudstore: migrate: %w", err)
		}
	}
	if err := cs.backfillProjectSessionsFromChunks(ctx); err != nil {
		return err
	}

	// ── Mario pre-OAuth attribution backfill (R2: dual jsonb_set) ──────────────
	// For rows with entity='observation' and user_email IS NULL, stamp the denorm
	// column with '' (empty) so the row is no longer NULL (making it easy to
	// identify "no attribution yet" vs "attribution explicitly set").
	// Simultaneously apply jsonb_set to cloud_mutations.payload to embed user_email.
	// A separate pass updates cloud_chunks.payload.observations[] (dual jsonb_set).
	if err := cs.backfillAttributionDenorm(ctx); err != nil {
		return err
	}

	return nil
}

// marioEmail, marioName, marioDept are the identity constants for the sole
// pre-OAuth contributor. All rows inserted before OAuth belong to Mario.
const marioEmail = "mpradas@vivastudios.com"
const marioName = "Mario Pradas"
const marioDept = "qa"

// backfillAttributionDenorm populates the denorm columns and patches JSONB
// payload for legacy cloud_mutations rows that were inserted before OAuth.
// Only runs on rows WHERE entity = 'observation' AND user_email IS NULL.
// Safe to re-run (idempotent via the NULL check).
//
// Pre-OAuth rows belong to Mario (the only contributor before OAuth).
// An existing payload value wins via COALESCE (e.g. a client that already
// embedded user_email retains its value); otherwise Mario's identity is used.
func (cs *CloudStore) backfillAttributionDenorm(ctx context.Context) error {
	// 1. Stamp denorm columns with Mario's identity.
	// COALESCE(payload->>'field', mario_default) so a pre-existing payload value wins.
	if _, err := cs.db.ExecContext(ctx, `
		UPDATE cloud_mutations
		SET
			user_email   = COALESCE(NULLIF(payload->>'user_email',  ''), $1),
			user_name    = COALESCE(NULLIF(payload->>'user_name',   ''), $2),
			department   = COALESCE(NULLIF(payload->>'department',  ''), $3),
			user_deleted = COALESCE((payload->>'user_deleted')::boolean, FALSE)
		WHERE entity = 'observation' AND user_email IS NULL
	`, marioEmail, marioName, marioDept); err != nil {
		return fmt.Errorf("cloudstore: backfill attribution denorm: %w", err)
	}

	// 2. Dual jsonb_set — patch cloud_mutations.payload with the (now-Mario) denorm values.
	// Uses the denorm column values so step 1 always drives step 2.
	if _, err := cs.db.ExecContext(ctx, `
		UPDATE cloud_mutations
		SET payload = jsonb_set(
			jsonb_set(
				jsonb_set(payload, '{user_email}',  to_jsonb(COALESCE(user_email,  $1)), true),
				'{user_name}',  to_jsonb(COALESCE(user_name,  $2)), true
			),
			'{department}', to_jsonb(COALESCE(department, $3)), true
		)
		WHERE entity = 'observation'
		  AND NOT (payload ? 'user_email')
	`, marioEmail, marioName, marioDept); err != nil {
		return fmt.Errorf("cloudstore: backfill attribution payload (mutations): %w", err)
	}

	// 3. Dual jsonb_set — patch cloud_chunks.payload.observations[] entries.
	// For each observation element that lacks user_email, inject Mario's values.
	if _, err := cs.db.ExecContext(ctx, `
		UPDATE cloud_chunks
		SET payload = jsonb_set(
			payload,
			'{observations}',
			(
				SELECT jsonb_agg(
					CASE
						WHEN elem ? 'user_email' THEN elem
						ELSE jsonb_set(
							jsonb_set(
								jsonb_set(elem, '{user_email}',  to_jsonb($1::text), true),
								'{user_name}',  to_jsonb($2::text), true
							),
							'{department}', to_jsonb($3::text), true
						)
					END
				)
				FROM jsonb_array_elements(
					COALESCE(payload->'observations', '[]'::jsonb)
				) AS elem
			),
			true
		)
		WHERE payload ? 'observations'
		  AND EXISTS (
			SELECT 1
			FROM jsonb_array_elements(COALESCE(payload->'observations', '[]'::jsonb)) AS elem
			WHERE NOT (elem ? 'user_email')
		  )
	`, marioEmail, marioName, marioDept); err != nil {
		return fmt.Errorf("cloudstore: backfill attribution payload (chunks): %w", err)
	}

	return nil
}

// ─── Mutation Journal Queries ─────────────────────────────────────────────────

// MutationEntry mirrors cloudserver.MutationEntry to avoid a circular import.
type MutationEntry struct {
	Project   string          `json:"project"`
	Entity    string          `json:"entity"`
	EntityKey string          `json:"entity_key"`
	Op        string          `json:"op"`
	Payload   json.RawMessage `json:"payload"`
}

// StoredMutation mirrors cloudserver.StoredMutation to avoid a circular import.
type StoredMutation struct {
	Seq        int64           `json:"seq"`
	Project    string          `json:"project"`
	Entity     string          `json:"entity"`
	EntityKey  string          `json:"entity_key"`
	Op         string          `json:"op"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurred_at"`
}

type MutationChunkBackfillReport struct {
	Project             string `json:"project"`
	Applied             bool   `json:"applied"`
	CandidateMutations  int    `json:"candidate_mutations"`
	AlreadyMaterialized int    `json:"already_materialized"`
	InvalidMutations    int    `json:"invalid_mutations"`
	ChunksPlanned       int    `json:"chunks_planned"`
	ChunksInserted      int    `json:"chunks_inserted"`
}

// Attribution carries the server-resolved identity of the authenticated caller.
// It is stamped into cloud_mutations denorm columns and the JSONB payload
// for every observation entry in InsertMutationBatch.
// Zero value (all empty/false) is treated as "no attribution available".
type Attribution struct {
	UserEmail   string
	UserName    string
	Department  string
	UserDeleted bool
}

// InsertMutationBatch inserts a batch of mutations into the cloud_mutations journal.
// Returns the sequence numbers assigned to each entry — one per input entry.
//
// When attr is non-zero, server-side attribution is stamped for observation entries:
//   - denorm columns (user_email, user_name, department, user_deleted) are set
//   - JSONB payload is updated with the same values
//
// Gate B (defense-in-depth, R1): observation entries with scope=personal are
// silently dropped. A sentinel-seq value (< 0) is returned for each dropped entry
// so the client ack count always equals len(batch). The drop is recorded in
// cloud_sync_audit_log with outcome "rejected_personal_scope".
//
// BW3: The entire non-personal portion is wrapped in a transaction — partial
// failures roll back all prior entries so the client can retry the full batch.
func (cs *CloudStore) InsertMutationBatch(ctx context.Context, batch []MutationEntry, attr Attribution) ([]int64, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	if len(batch) == 0 {
		return []int64{}, nil
	}

	// Gate B pre-pass: identify personal-scope observation entries.
	// We need the final seqs slice to have one entry per input entry,
	// with sentinel values for dropped entries. Build a filtered batch
	// for storage and record which original indices were dropped.
	type dropInfo struct {
		index     int
		project   string
		entityKey string
	}
	var drops []dropInfo
	storedBatch := make([]MutationEntry, 0, len(batch))
	droppedIdx := make(map[int]struct{}, 0) // original indices that are personal

	for i, entry := range batch {
		if strings.TrimSpace(entry.Entity) == "observation" {
			scope := extractScopeFromPayload(entry.Payload)
			if scope == "personal" {
				drops = append(drops, dropInfo{
					index:     i,
					project:   strings.TrimSpace(entry.Project),
					entityKey: strings.TrimSpace(entry.EntityKey),
				})
				droppedIdx[i] = struct{}{}
				continue
			}
			// Stamp attribution into the payload + prepare denorm values for
			// non-personal observation entries.
			entry = stampAttributionIntoEntry(entry, attr)
		}
		storedBatch = append(storedBatch, entry)
	}

	chunks, err := materializedMutationBatchChunks(storedBatch)
	if err != nil {
		return nil, err
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: begin mutation batch tx: %w", err)
	}
	// Ensure rollback on any error path.
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Insert stored (non-personal) entries and collect their seqs.
	storedSeqs := make([]int64, 0, len(storedBatch))
	for _, entry := range storedBatch {
		project := strings.TrimSpace(entry.Project)
		entity := strings.TrimSpace(entry.Entity)
		entityKey := strings.TrimSpace(entry.EntityKey)
		op := strings.TrimSpace(entry.Op)
		payload := entry.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}

		// Stamp denorm columns when attribution is available and entity is observation.
		hasAttr := attr.UserEmail != ""
		if entity == "observation" && hasAttr {
			var seq int64
			err := tx.QueryRowContext(ctx, `
				INSERT INTO cloud_mutations (project, entity, entity_key, op, payload,
					user_email, user_name, department, user_deleted)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				RETURNING seq`,
				project, entity, entityKey, op, payload,
				attr.UserEmail, attr.UserName, attr.Department, attr.UserDeleted,
			).Scan(&seq)
			if err != nil {
				return nil, fmt.Errorf("cloudstore: insert mutation: %w", err)
			}
			storedSeqs = append(storedSeqs, seq)
		} else {
			var seq int64
			err := tx.QueryRowContext(ctx, `
				INSERT INTO cloud_mutations (project, entity, entity_key, op, payload)
				VALUES ($1, $2, $3, $4, $5)
				RETURNING seq`,
				project, entity, entityKey, op, payload,
			).Scan(&seq)
			if err != nil {
				return nil, fmt.Errorf("cloudstore: insert mutation: %w", err)
			}
			storedSeqs = append(storedSeqs, seq)
		}
	}

	for _, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload, sessions_count, observations_count, prompts_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (project_name, chunk_id) DO NOTHING`,
			chunk.project, chunk.id, "mutation-push", chunk.payload, chunk.counts.sessions, chunk.counts.observations, chunk.counts.prompts,
		); err != nil {
			return nil, fmt.Errorf("cloudstore: materialize mutation batch chunk: %w", err)
		}
		if err := cs.indexChunkSessionsWith(ctx, tx, chunk.project, chunk.payload); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cloudstore: commit mutation batch: %w", err)
	}
	tx = nil // mark committed so deferred Rollback is a no-op
	if len(chunks) > 0 {
		cs.invalidateDashboardReadModel()
	}

	// Audit dropped personal entries (after commit — best-effort, non-fatal).
	contributor := strings.TrimSpace(attr.UserEmail)
	if contributor == "" {
		contributor = "unknown"
	}
	for _, d := range drops {
		_ = cs.InsertAuditEntry(ctx, AuditEntry{
			Contributor: contributor,
			Project:     d.project,
			Action:      AuditActionMutationPush,
			Outcome:     AuditOutcomeRejectedPersonalScope,
			EntryCount:  1,
			ReasonCode:  "gate_b_personal_scope",
			Metadata:    map[string]any{"entity_key": d.entityKey},
		})
	}

	// Build the final seqs slice: one value per original batch entry.
	// Dropped entries get sentinel value -1 (negative, never a real DB seq).
	seqs := make([]int64, len(batch))
	storedIdx := 0
	for origIdx := range batch {
		if _, dropped := droppedIdx[origIdx]; dropped {
			seqs[origIdx] = -1 // sentinel-seq (R1)
		} else {
			seqs[origIdx] = storedSeqs[storedIdx]
			storedIdx++
		}
	}
	return seqs, nil
}

// extractScopeFromPayload extracts the "scope" field from a JSON payload.
// Returns empty string if not present or not a string.
func extractScopeFromPayload(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var p struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Scope)
}

// stampAttributionIntoEntry returns a copy of entry with attribution fields
// merged into the JSONB payload. Used for non-personal observation entries.
func stampAttributionIntoEntry(entry MutationEntry, attr Attribution) MutationEntry {
	if attr.UserEmail == "" {
		return entry
	}
	payload := entry.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return entry
	}
	m["user_email"] = attr.UserEmail
	m["user_name"] = attr.UserName
	m["department"] = attr.Department
	m["user_deleted"] = attr.UserDeleted
	stamped, err := json.Marshal(m)
	if err != nil {
		return entry
	}
	entry.Payload = json.RawMessage(stamped)
	return entry
}

const mutationBackfillChunkSize = 100

func (cs *CloudStore) BackfillMutationChunks(ctx context.Context, project string, apply bool) (MutationChunkBackfillReport, error) {
	if cs == nil || cs.db == nil {
		return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: project is required")
	}

	report := MutationChunkBackfillReport{Project: project, Applied: apply}
	materialized, err := cs.existingChunkMutationSignatures(ctx, project)
	if err != nil {
		return MutationChunkBackfillReport{}, err
	}

	rows, err := cs.db.QueryContext(ctx, `
		SELECT project, entity, entity_key, op, payload::text
		FROM cloud_mutations
		WHERE project = $1
		ORDER BY seq ASC`, project)
	if err != nil {
		return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: query mutation chunk backfill candidates: %w", err)
	}
	defer rows.Close()

	missing := make([]MutationEntry, 0)
	for rows.Next() {
		var entry MutationEntry
		var payload string
		if err := rows.Scan(&entry.Project, &entry.Entity, &entry.EntityKey, &entry.Op, &payload); err != nil {
			return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: scan mutation chunk backfill candidate: %w", err)
		}
		if !isChunkMaterializableMutationEntity(entry.Entity) {
			continue
		}
		entry.Payload = json.RawMessage(payload)
		report.CandidateMutations++
		sig, err := mutationEntrySignature(entry)
		if err != nil {
			report.InvalidMutations++
			continue
		}
		if _, ok := materialized[sig]; ok {
			report.AlreadyMaterialized++
			continue
		}
		missing = append(missing, entry)
	}
	if err := rows.Err(); err != nil {
		return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: iterate mutation chunk backfill candidates: %w", err)
	}

	chunks := make([]materializedMutationChunk, 0)
	for start := 0; start < len(missing); start += mutationBackfillChunkSize {
		end := start + mutationBackfillChunkSize
		if end > len(missing) {
			end = len(missing)
		}
		batchChunks, err := materializedMutationBatchChunks(missing[start:end])
		if err != nil {
			return MutationChunkBackfillReport{}, err
		}
		chunks = append(chunks, batchChunks...)
	}
	report.ChunksPlanned = len(chunks)
	if !apply || len(chunks) == 0 {
		return report, nil
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: begin mutation chunk backfill tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	for _, chunk := range chunks {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload, sessions_count, observations_count, prompts_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (project_name, chunk_id) DO NOTHING`,
			chunk.project, chunk.id, "mutation-backfill", chunk.payload, chunk.counts.sessions, chunk.counts.observations, chunk.counts.prompts,
		)
		if err != nil {
			return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: insert mutation chunk backfill: %w", err)
		}
		if affected, err := result.RowsAffected(); err == nil && affected > 0 {
			report.ChunksInserted++
		}
		if err := cs.indexChunkSessionsWith(ctx, tx, chunk.project, chunk.payload); err != nil {
			return MutationChunkBackfillReport{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MutationChunkBackfillReport{}, fmt.Errorf("cloudstore: commit mutation chunk backfill: %w", err)
	}
	tx = nil
	if report.ChunksInserted > 0 {
		cs.invalidateDashboardReadModel()
	}
	return report, nil
}

func (cs *CloudStore) existingChunkMutationSignatures(ctx context.Context, project string) (map[string]struct{}, error) {
	rows, err := cs.db.QueryContext(ctx, `SELECT payload FROM cloud_chunks WHERE project_name = $1`, project)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: query existing chunk mutations: %w", err)
	}
	defer rows.Close()

	signatures := make(map[string]struct{})
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("cloudstore: scan existing chunk mutations: %w", err)
		}
		chunk, err := parseChunkData(payload)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: parse existing chunk mutations: %w", err)
		}
		for _, mutation := range chunk.Mutations {
			if !isChunkMaterializableMutationEntity(mutation.Entity) {
				continue
			}
			sig, err := syncMutationSignature(mutation)
			if err != nil {
				return nil, fmt.Errorf("cloudstore: sign existing chunk mutation %s/%s/%s: %w", mutation.Project, mutation.Entity, mutation.EntityKey, err)
			}
			signatures[sig] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate existing chunk mutations: %w", err)
	}
	return signatures, nil
}

type materializedMutationChunk struct {
	project string
	id      string
	payload []byte
	counts  chunkSummary
}

func materializedMutationBatchChunks(batch []MutationEntry) ([]materializedMutationChunk, error) {
	if len(batch) == 0 {
		return nil, nil
	}
	groups := make(map[string][]MutationEntry)
	order := make([]string, 0)
	for i, entry := range batch {
		project := strings.TrimSpace(entry.Project)
		if project == "" {
			return nil, fmt.Errorf("cloudstore: materialize mutation batch: entries[%d].project is required", i)
		}
		if _, ok := groups[project]; !ok {
			order = append(order, project)
		}
		groups[project] = append(groups[project], entry)
	}

	chunks := make([]materializedMutationChunk, 0, len(order))
	for _, project := range order {
		payload, counts, err := materializedMutationBatchChunk(groups[project])
		if err != nil {
			return nil, err
		}
		if len(payload) == 0 {
			continue
		}
		chunks = append(chunks, materializedMutationChunk{project: project, id: chunkIDFromPayload(payload), payload: payload, counts: counts})
	}
	return chunks, nil
}

func materializedMutationBatchChunk(batch []MutationEntry) ([]byte, chunkSummary, error) {
	if len(batch) == 0 {
		return nil, chunkSummary{}, nil
	}
	project := strings.TrimSpace(batch[0].Project)
	chunk := engramsync.ChunkData{Mutations: make([]store.SyncMutation, 0, len(batch))}
	for i, entry := range batch {
		entryProject := strings.TrimSpace(entry.Project)
		if entryProject == "" {
			return nil, chunkSummary{}, fmt.Errorf("cloudstore: materialize mutation batch: entries[%d].project is required", i)
		}
		if project == "" {
			project = entryProject
		}
		if entryProject != project {
			return nil, chunkSummary{}, fmt.Errorf("cloudstore: materialize mutation batch: mixed projects %q and %q", project, entryProject)
		}
		entity := strings.TrimSpace(entry.Entity)
		if !isChunkMaterializableMutationEntity(entity) {
			continue
		}

		payload := entry.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		chunk.Mutations = append(chunk.Mutations, store.SyncMutation{
			Project:   entryProject,
			Entity:    entity,
			EntityKey: strings.TrimSpace(entry.EntityKey),
			Op:        strings.TrimSpace(entry.Op),
			Payload:   string(payload),
		})

		if strings.TrimSpace(entry.Op) != store.SyncOpUpsert {
			continue
		}
		switch entity {
		case store.SyncEntitySession:
			var session store.Session
			if err := json.Unmarshal(payload, &session); err != nil {
				return nil, chunkSummary{}, fmt.Errorf("cloudstore: materialize mutation batch session %q: %w", entry.EntityKey, err)
			}
			if strings.TrimSpace(session.ID) == "" {
				session.ID = strings.TrimSpace(entry.EntityKey)
			}
			chunk.Sessions = append(chunk.Sessions, session)
		case store.SyncEntityObservation:
			var observation store.Observation
			if err := json.Unmarshal(payload, &observation); err != nil {
				return nil, chunkSummary{}, fmt.Errorf("cloudstore: materialize mutation batch observation %q: %w", entry.EntityKey, err)
			}
			if strings.TrimSpace(observation.SyncID) == "" {
				observation.SyncID = strings.TrimSpace(entry.EntityKey)
			}
			chunk.Observations = append(chunk.Observations, observation)
		case store.SyncEntityPrompt:
			var prompt store.Prompt
			if err := json.Unmarshal(payload, &prompt); err != nil {
				return nil, chunkSummary{}, fmt.Errorf("cloudstore: materialize mutation batch prompt %q: %w", entry.EntityKey, err)
			}
			if strings.TrimSpace(prompt.SyncID) == "" {
				prompt.SyncID = strings.TrimSpace(entry.EntityKey)
			}
			chunk.Prompts = append(chunk.Prompts, prompt)
		}
	}
	if len(chunk.Mutations) == 0 {
		return nil, chunkSummary{}, nil
	}

	payload, err := json.Marshal(chunk)
	if err != nil {
		return nil, chunkSummary{}, fmt.Errorf("cloudstore: marshal materialized mutation batch chunk: %w", err)
	}
	payload, err = chunkcodec.CanonicalizeForProject(payload, project)
	if err != nil {
		return nil, chunkSummary{}, fmt.Errorf("cloudstore: canonicalize materialized mutation batch chunk: %w", err)
	}
	return payload, chunkSummary{sessions: len(chunk.Sessions), observations: len(chunk.Observations), prompts: len(chunk.Prompts)}, nil
}

func isChunkMaterializableMutationEntity(entity string) bool {
	switch strings.TrimSpace(entity) {
	case store.SyncEntitySession, store.SyncEntityObservation, store.SyncEntityPrompt, store.SyncEntityRelation:
		return true
	default:
		return false
	}
}

func mutationEntrySignature(entry MutationEntry) (string, error) {
	project := strings.TrimSpace(entry.Project)
	if project == "" {
		return "", fmt.Errorf("project is required")
	}
	payload := entry.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	doc := engramsync.ChunkData{Mutations: []store.SyncMutation{{
		Project:   project,
		Entity:    strings.TrimSpace(entry.Entity),
		EntityKey: strings.TrimSpace(entry.EntityKey),
		Op:        strings.TrimSpace(entry.Op),
		Payload:   string(payload),
	}}}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	canonical, err := chunkcodec.CanonicalizeForProject(encoded, project)
	if err != nil {
		return "", err
	}
	chunk, err := parseChunkData(canonical)
	if err != nil {
		return "", err
	}
	if len(chunk.Mutations) != 1 {
		return "", fmt.Errorf("expected one canonical mutation, got %d", len(chunk.Mutations))
	}
	return syncMutationSignature(chunk.Mutations[0])
}

func syncMutationSignature(mutation store.SyncMutation) (string, error) {
	normalized, err := canonicalMutationPayload([]byte(strings.TrimSpace(mutation.Payload)))
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		strings.TrimSpace(mutation.Project),
		strings.TrimSpace(mutation.Entity),
		strings.TrimSpace(mutation.EntityKey),
		strings.TrimSpace(mutation.Op),
		normalized,
	}, "\x00"), nil
}

func canonicalMutationPayload(payload []byte) (string, error) {
	payload = normalizeJSON(payload)
	if !json.Valid(payload) {
		return "", fmt.Errorf("payload is not valid JSON")
	}
	return string(payload), nil
}

// ListMutationsSince returns mutations with seq > sinceSeq, filtered to allowedProjects.
// If allowedProjects is nil, no project filter is applied (returns all).
// If allowedProjects is non-nil (even empty), only those projects are returned.
// Returns (mutations, hasMore, latestSeq, error).
func (cs *CloudStore) ListMutationsSince(ctx context.Context, sinceSeq int64, limit int, allowedProjects []string) ([]StoredMutation, bool, int64, error) {
	if cs == nil || cs.db == nil {
		return nil, false, 0, fmt.Errorf("cloudstore: not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}

	// If allowedProjects is non-nil but empty, return empty result immediately.
	if allowedProjects != nil && len(allowedProjects) == 0 {
		return []StoredMutation{}, false, 0, nil
	}

	// Fetch limit+1 to detect hasMore.
	fetchLimit := limit + 1

	var rows *sql.Rows
	var err error

	if allowedProjects == nil {
		// No enrollment filter.
		rows, err = cs.db.QueryContext(ctx, `
			SELECT seq, project, entity, entity_key, op, payload::text, occurred_at
			FROM cloud_mutations
			WHERE seq > $1
			ORDER BY seq ASC
			LIMIT $2`,
			sinceSeq, fetchLimit,
		)
	} else {
		// Filter by allowed projects.
		rows, err = cs.db.QueryContext(ctx, `
			SELECT seq, project, entity, entity_key, op, payload::text, occurred_at
			FROM cloud_mutations
			WHERE seq > $1 AND project = ANY($2)
			ORDER BY seq ASC
			LIMIT $3`,
			sinceSeq, allowedProjects, fetchLimit,
		)
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("cloudstore: list mutations since %d: %w", sinceSeq, err)
	}
	defer rows.Close()

	var all []StoredMutation
	for rows.Next() {
		var m StoredMutation
		var payloadStr string
		var occurredAt time.Time
		if err := rows.Scan(&m.Seq, &m.Project, &m.Entity, &m.EntityKey, &m.Op, &payloadStr, &occurredAt); err != nil {
			return nil, false, 0, fmt.Errorf("cloudstore: scan mutation: %w", err)
		}
		m.Payload = json.RawMessage(payloadStr)
		m.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		all = append(all, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, fmt.Errorf("cloudstore: iterate mutations: %w", err)
	}

	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}

	latestSeq := int64(0)
	if len(all) > 0 {
		latestSeq = all[len(all)-1].Seq
	}

	return all, hasMore, latestSeq, nil
}

func parseClientCreatedAt(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: invalid client_created_at: %w", err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func chunkIDFromPayload(payload []byte) string {
	return chunkcodec.ChunkID(payload)
}

func normalizeJSON(payload []byte) []byte {
	var body any
	if err := json.Unmarshal(payload, &body); err != nil {
		return payload
	}
	normalized, err := json.Marshal(body)
	if err != nil {
		return payload
	}
	return normalized
}

type chunkSummary struct {
	sessions     int
	observations int
	prompts      int
}

func summarizeChunk(payload []byte) chunkSummary {
	var body struct {
		Sessions     []json.RawMessage `json:"sessions"`
		Observations []json.RawMessage `json:"observations"`
		Prompts      []json.RawMessage `json:"prompts"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return chunkSummary{}
	}
	return chunkSummary{
		sessions:     len(body.Sessions),
		observations: len(body.Observations),
		prompts:      len(body.Prompts),
	}
}
