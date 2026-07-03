# Delta for Decay Defaults (memory-conflict-surfacing)

## Purpose

Defines the type-based `review_after` defaults applied to new observation saves, the
`expires_at` and embedding column additions on `observations`, and the invariant that
these defaults are NOT applied retroactively to pre-existing rows. Phase 1 populates the
columns; decay query activation is explicitly deferred to Phase 2.

---

## ADDED Requirements

### Requirement: Type-Based review_after Default on New Saves

When a new observation row is INSERTed (not an update, topic_key revision, or
duplicate-skip), the system MUST set `review_after` according to the observation's `type`
relative to `created_at`. `expires_at` MUST remain NULL for all types in Phase 1. These
defaults MUST NOT be applied retroactively to existing rows by `migrate()` or by any
other path.

Locked constants in `internal/store/store.go`:

| Constant | Value |
|----------|-------|
| `decayDecisionMonths` | 6 |
| `decayPolicyMonths` | 12 |
| `decayPreferenceMonths` | 3 |

Resulting defaults:

| type | review_after offset | expires_at |
|------|---------------------|------------|
| `decision` | +6 months from `created_at` | NULL |
| `policy` | +12 months from `created_at` | NULL |
| `preference` | +3 months from `created_at` | NULL |
| `observation` | NULL | NULL |
| `manual` | NULL | NULL |
| `config` | NULL | NULL |
| `pattern` | NULL | NULL |
| `discovery` | NULL | NULL |
| `bugfix` | NULL | NULL |
| `architecture` | NULL (Phase 1; tune in Phase 2 with real corpus data) | NULL |

The `review_after` value is computed and written via `UPDATE observations SET review_after = ?
WHERE id = ?` AFTER the row is inserted (same transaction). `expires_at` is left as NULL
for all types; it requires no write.

No query, CLI command, autosync process, or cloud server reads `review_after` or
`expires_at` in Phase 1. The columns are populated and reserved for Phase 2 activation.

#### Scenario: decision observation gets review_after +6 months

- GIVEN a store with no observations
- WHEN `mem_save` is called with `type = "decision"`
- THEN the saved row has `review_after` approximately equal to `created_at + 6 months`
  (within a ±2 second tolerance)
- AND `expires_at` is NULL

#### Scenario: policy observation gets review_after +12 months

- GIVEN a store with no observations
- WHEN `mem_save` is called with `type = "policy"`
- THEN the saved row has `review_after` approximately equal to `created_at + 12 months`
- AND `expires_at` is NULL

#### Scenario: preference observation gets review_after +3 months

- GIVEN a store with no observations
- WHEN `mem_save` is called with `type = "preference"`
- THEN the saved row has `review_after` approximately equal to `created_at + 3 months`
- AND `expires_at` is NULL

#### Scenario: observation type has no decay date

- GIVEN a store with no observations
- WHEN `mem_save` is called with `type = "observation"`
- THEN `review_after` is NULL
- AND `expires_at` is NULL

#### Scenario: Non-decay types produce NULL review_after

- GIVEN `mem_save` is called separately for types `manual`, `config`, `pattern`,
  `discovery`, `bugfix`, and `architecture`
- WHEN each row is saved
- THEN `review_after` is NULL for each row
- AND `expires_at` is NULL for each row

#### Scenario: Decay not applied retroactively on migration

- GIVEN a database migrated from the pre-change version containing observation rows of
  types `decision`, `policy`, and `preference` (inserted before this change shipped)
- WHEN `migrate()` runs
- THEN all pre-existing rows have `review_after = NULL`
  (migration is schema-only; no backfill of decay dates on existing data)

#### Scenario: Decay applied only to new insert, not to topic_key revision

- GIVEN observation A was originally inserted with `type = "decision"` and `review_after`
  set to date T
- WHEN `mem_save` is called again with the same `topic_key` (a revision — updates the
  existing row rather than inserting a new one)
- THEN `review_after` retains its original value T (decay default is not recalculated on revision)

---

## Phase 1 Deferral Notes

- `engram review` CLI command (decay activation): Phase 2. The query
  `WHERE review_after <= datetime('now') AND deleted_at IS NULL` is NOT added in Phase 1.
  No schema migration needed at activation; the column is already indexed-nowhere and
  ready for a new query.
- `expires_at` expiration logic: NULL for all rows in Phase 1. Activation is Phase 2.
- Embedding generation (`embedding`, `embedding_model`, `embedding_created_at`): Phase 2.
  These columns exist as nullable placeholders on `observations`; no generation pipeline
  is wired in Phase 1.
- Per-type tuning of decay offsets: the constants `decayDecisionMonths`,
  `decayPolicyMonths`, and `decayPreferenceMonths` are configurable in code. A tuning
  loop with real corpus data is a Phase 2 operational concern.
- `architecture` type decay offset: deliberately left NULL in Phase 1; architecture
  observations are long-lived and the appropriate offset needs real-usage data to calibrate.
