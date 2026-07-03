# Delta for Relation Store (memory-conflict-surfacing)

## Purpose

Defines the persistence contract for the new `memory_relations` table and the additive
changes to `observations`. Covers the full schema DDL, multi-actor semantics, provenance
requirements, migration safety guarantees, sync boundary invariants, and the orphaning
mechanism for hard-deleted observations.

---

## ADDED Requirements

### Requirement: memory_relations Table Schema

The local SQLite database MUST include a `memory_relations` table created via
`CREATE TABLE IF NOT EXISTS` with the following schema:

```sql
CREATE TABLE IF NOT EXISTS memory_relations (
    id                         INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id                    TEXT    NOT NULL UNIQUE,
    source_id                  TEXT,
    target_id                  TEXT,
    relation                   TEXT    NOT NULL DEFAULT 'pending',
    reason                     TEXT,
    evidence                   TEXT,
    confidence                 REAL,
    judgment_status            TEXT    NOT NULL DEFAULT 'pending',
    marked_by_actor            TEXT,
    marked_by_kind             TEXT,
    marked_by_model            TEXT,
    session_id                 TEXT,
    superseded_at              TEXT,
    superseded_by_relation_id  INTEGER REFERENCES memory_relations(id) ON DELETE SET NULL,
    created_at                 TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at                 TEXT    NOT NULL DEFAULT (datetime('now'))
);
```

The `source_id` and `target_id` columns hold `sync_id` values from the `observations`
table (TEXT, cross-machine portable). They MUST NOT be declared as FOREIGN KEY columns
with ON DELETE CASCADE or SET NULL — the orphaning mechanism is handled at the
application layer (see Orphaning requirement). The self-referential FK
`superseded_by_relation_id` does use ON DELETE SET NULL; this covers retraction chains
between relation rows only.

The following indexes MUST exist:

```sql
CREATE INDEX IF NOT EXISTS idx_memrel_source    ON memory_relations(source_id, judgment_status);
CREATE INDEX IF NOT EXISTS idx_memrel_target    ON memory_relations(target_id, judgment_status);
CREATE INDEX IF NOT EXISTS idx_memrel_supersede ON memory_relations(superseded_by_relation_id);
```

The `sync_id` UNIQUE constraint provides the fourth index implicitly.

Locked `relation` vocabulary (validated in Go before any write):
`pending`, `related`, `compatible`, `scoped`, `conflicts_with`, `supersedes`, `not_conflict`.
The value `pending` is the DB default; `mem_judge` flips it to one of the verdict verbs.

Locked `judgment_status` vocabulary:
`pending`, `judged`, `orphaned`, `ignored`.

#### Scenario: memory_relations table and indexes created by migrate()

- GIVEN a fresh SQLite database
- WHEN `migrate()` runs
- THEN a table named `memory_relations` exists with all columns listed above
- AND indexes `idx_memrel_source`, `idx_memrel_target`, and `idx_memrel_supersede` exist
- AND a new pending relation row can be inserted with only `sync_id`, `source_id`, `target_id`,
  and `relation` (all other columns default or nullable)

---

### Requirement: observations Additive Columns

The `observations` table MUST have five new nullable columns added via `addColumnIfNotExists`:

| Column | Type | Phase 1 behavior |
|--------|------|------------------|
| `review_after` | TEXT NULL | Populated by decay defaults (see decay-defaults spec) |
| `expires_at` | TEXT NULL | Always NULL in Phase 1 |
| `embedding` | BLOB NULL | Always NULL in Phase 1; reserved for Phase 2 |
| `embedding_model` | TEXT NULL | Always NULL in Phase 1; reserved for Phase 2 |
| `embedding_created_at` | TEXT NULL | Always NULL in Phase 1; reserved for Phase 2 |

All columns are nullable with no DEFAULT value. No existing INSERT statement that omits
these columns is broken.

#### Scenario: New columns exist after migration

- GIVEN a pre-change database
- WHEN `migrate()` runs
- THEN `PRAGMA table_info(observations)` shows all five new columns
- AND all five columns are nullable (`notnull = 0`)

---

### Requirement: Multi-Actor Relations at Schema Level

The `memory_relations` table MUST NOT have a UNIQUE constraint on `(source_id, target_id)`.
Two independent agents MUST each be able to record a verdict for the same observation pair.
Each verdict produces a separate row with its own unique `sync_id`. Resolution logic for
multi-actor disagreement is deferred to Phase 2.

#### Scenario: Two actors produce two independent rows for the same pair

- GIVEN a pending relation row for pair (obs-A, obs-B) with `sync_id = "rel-row1"`
- WHEN a second insert is performed for the same `(source_id=obs-A, target_id=obs-B)` pair
  with `sync_id = "rel-row2"`
- THEN both rows exist in `memory_relations`
- AND `GetRelationsForObservation(obs-A)` returns both rows
- AND `GetRelationsForObservation(obs-B)` returns both rows (target direction)

#### Scenario: sync_id uniqueness is enforced per row

- GIVEN a relation row exists with `sync_id = "rel-abc123def456"`
- WHEN a second INSERT attempts to use `sync_id = "rel-abc123def456"`
- THEN the insert fails with a UNIQUE constraint error
- AND the first row is unchanged

---

### Requirement: Full Provenance on Every Relation Row

Every relation row updated by `mem_judge` MUST capture provenance at the time of
judgment. The following fields MUST be populated:

| Field | Required | Notes |
|-------|----------|-------|
| `marked_by_actor` | yes | e.g. "agent:claude-sonnet-4-6" or "user" |
| `marked_by_kind` | yes | "agent", "human", or "system" |
| `marked_by_model` | when kind="agent" | model identifier; NULL for human/system actors |
| `session_id` | when available | session that wrote the judgment |
| `created_at` | yes | NOT NULL; set on initial insert |
| `updated_at` | yes | NOT NULL; advanced on every `mem_judge` call |

Optional fields (`reason`, `evidence`, `confidence`) are populated only when the caller
provides them; they remain NULL otherwise.

#### Scenario: Agent judgment captures full provenance

- GIVEN `mem_judge` is called by an agent identified as "claude-sonnet-4-6" in session "sess-xyz"
  with `confidence = 0.85` and `evidence = '{"basis":"title overlap"}'`
- WHEN the relation row is written
- THEN `marked_by_actor = "agent:claude-sonnet-4-6"`, `marked_by_kind = "agent"`,
  `marked_by_model = "claude-sonnet-4-6"`, `session_id = "sess-xyz"`,
  `confidence = 0.85`, `evidence = '{"basis":"title overlap"}'`
- AND `created_at` is NOT NULL and `updated_at` is NOT NULL

#### Scenario: Optional provenance fields remain NULL when omitted

- GIVEN `mem_judge` is called with only `judgment_id` and `relation`
- WHEN the relation row is updated
- THEN `reason` is NULL, `evidence` is NULL, and `confidence` is NULL in the stored row

#### Scenario: Human actor has NULL model field

- GIVEN a relation is created with `marked_by_kind = "human"` and `marked_by_actor = "user"`
- WHEN the row is read back
- THEN `marked_by_model` is NULL
- AND `marked_by_actor = "user"`, `marked_by_kind = "human"`, and `created_at` are all populated

---

### Requirement: Additive Migration Safety

`migrate()` MUST extend the pre-change schema purely additively. Running on a database
that was created with the pre-change DDL MUST satisfy all of the following:

1. All existing observation rows are intact: `id`, `sync_id`, `content`, and `created_at`
   are byte-identical after migration.
2. All five new `observations` columns exist with NULL for every pre-existing row.
3. The `memory_relations` table and its three named indexes exist.
4. Running `migrate()` a second time is a no-op: no error, identical schema.
5. The `obs_fts` FTS5 virtual table and its associated triggers are unchanged.
6. The `sync_mutations` table and its rows are unchanged.

`migrate()` MUST use `addColumnIfNotExists` for observation columns and
`CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS` for the new table.
No `DROP COLUMN`, no `ALTER COLUMN`, no data migration on existing rows.

A `newTestStoreWithLegacySchema(t, fixtureRows)` test helper (introduced in this change as
standing migration test infrastructure) opens a temp SQLite database with the pre-change
DDL constant `legacyDDLPreMemoryConflictSurfacing`, inserts fixture rows, and calls
`New(cfg)` to trigger `migrate()`. This helper is the pattern for all future migration tests.

#### Scenario: Pre-change database migrates cleanly

- GIVEN a SQLite database built with the pre-change DDL containing 5 observation rows of
  mixed types (decision, policy, preference, observation, manual)
- WHEN `migrate()` runs on that database
- THEN all 5 rows are present with identical `id`, `sync_id`, `content`, and `created_at`
- AND `review_after` and `expires_at` are NULL on all 5 pre-existing rows
- AND `memory_relations` table exists
- AND indexes `idx_memrel_source`, `idx_memrel_target`, and `idx_memrel_supersede` exist

#### Scenario: Idempotent — second migrate() is a no-op

- GIVEN `migrate()` has already run successfully on a database
- WHEN `migrate()` is called a second time on the same database
- THEN no error is returned
- AND the schema is identical to after the first run
- AND no duplicate columns, tables, or indexes are created

#### Scenario: FTS5 virtual table and sync_mutations are untouched

- GIVEN the database has an `obs_fts` virtual table, its associated triggers
  (`obs_fts_insert`, `obs_fts_update`, `obs_fts_delete`), and rows in `sync_mutations`
- WHEN `migrate()` runs
- THEN `obs_fts`, its triggers, and all `sync_mutations` rows are unchanged

---

### Requirement: Relations Are Local-Only in Phase 1

`memory_relations` rows MUST NOT be enqueued into `sync_mutations`. The sync wire format
for observations (`syncObservationPayload`) MUST remain identical to the pre-change shape.
New `observations` columns (`review_after`, `expires_at`, `embedding`, `embedding_model`,
`embedding_created_at`) MUST NOT appear in the sync payload in Phase 1.

This is an explicit Phase 1 scope boundary. Cloud sync of `memory_relations` is deferred
to Phase 2 (requires: `SyncEntityRelation` constant, `syncRelationPayload` struct,
`applyRelationUpsertTx` / `applyRelationDeleteTx`, and a `cloud_memory_relations` table
on the Postgres cloud schema). The local `memory_relations.sync_id` TEXT column exists now
to make Phase 2 a mechanical addition without a schema migration.

#### Scenario: Relation insert does not create a sync mutation

- GIVEN cloud sync enrollment is active for the current project
- WHEN `mem_judge` is called and a `memory_relations` row is inserted or updated
- THEN the count of rows in `sync_mutations` is unchanged
- AND no row in `sync_mutations` references the relation's `sync_id`

#### Scenario: Observation sync payload does not include new columns

- GIVEN an observation is saved with `type = "decision"` (which sets `review_after`)
- WHEN the sync mutation for that observation is enqueued into `sync_mutations`
- THEN the mutation payload JSON does NOT contain `review_after`, `expires_at`,
  `embedding`, `embedding_model`, or `embedding_created_at`
- AND the sync test suite passes without modification

---

### Requirement: Orphaning on Observation Hard-Delete

When an observation is hard-deleted and one or more `memory_relations` rows reference
its `sync_id` as `source_id` or `target_id`, those relation rows MUST NOT be
cascade-deleted. Instead, in the same transaction as the hard-delete,
`judgment_status` on each referencing relation row MUST be set to `'orphaned'` via an
application-layer UPDATE. Relation rows are audit history and MUST be preserved.

`mem_search` result annotations MUST exclude relations with `judgment_status = 'orphaned'`
by filtering them out in the batch fetch query.

Soft-delete (setting `deleted_at` on an observation) MUST NOT orphan relations. Soft-
deleted observations are already excluded from annotation rendering by the
`o.deleted_at IS NULL` join filter on the observation join side.

The self-referential FK `superseded_by_relation_id REFERENCES memory_relations(id) ON
DELETE SET NULL` handles only inter-relation retraction chains; it does NOT apply to
`source_id` or `target_id` references to observations.

#### Scenario: Hard-deleting source observation orphans referencing relations

- GIVEN a relation row with `source_id = "obs-aaa"`, `judgment_status = "judged"`
- WHEN the observation with `sync_id = "obs-aaa"` is hard-deleted
- THEN the relation row still exists in `memory_relations`
- AND `judgment_status = 'orphaned'` and `updated_at` is advanced

#### Scenario: Orphaned relations are invisible in mem_search annotations

- GIVEN observation B has one relation with `judgment_status = 'orphaned'` and one with
  `judgment_status = 'judged'` involving observation C
- WHEN `mem_search` returns observation B
- THEN only the judged relation produces an annotation line (e.g., `supersedes: #C.id`)
- AND the orphaned relation produces no annotation line

#### Scenario: Orphaned relation does not taint candidate eligibility

- GIVEN observation B has an orphaned relation (its source observation A was hard-deleted)
- AND a new observation C is saved with a title similar to B
- WHEN candidate detection runs for C
- THEN B is still returned as a candidate (the orphaned relation does not remove B from
  FTS5 index or mark B as deleted)

#### Scenario: Soft-delete does NOT orphan relations

- GIVEN a relation row with `source_id = "obs-bbb"` and `judgment_status = "judged"`
- WHEN the observation with `sync_id = "obs-bbb"` is soft-deleted (its `deleted_at` is set;
  it is NOT hard-deleted)
- THEN the relation row retains `judgment_status = "judged"` (NOT changed to orphaned)
- AND the annotation is suppressed naturally because `obs-bbb` is excluded from the
  observation join by `o.deleted_at IS NULL`

---

## Phase 1 Deferral Notes

- Cloud sync of `memory_relations`: deferred to Phase 2. The `sync_id` TEXT column on
  every row is the Phase 2 portability hook. No `cloud_memory_relations` Postgres table
  is created in Phase 1.
- pgvector / embedding generation: `embedding`, `embedding_model`, and
  `embedding_created_at` on `observations` are Phase 2 placeholders; no generation
  pipeline runs in Phase 1.
- Multi-actor disagreement resolution algorithm: the schema permits multiple rows per
  `(source_id, target_id)` pair, but the resolution logic (e.g., weighted-by-confidence
  aggregation, latest-wins) is Phase 2.
- Decay query activation (`engram review` command): `review_after` is populated (see
  decay-defaults spec) but no query reads it in Phase 1.
