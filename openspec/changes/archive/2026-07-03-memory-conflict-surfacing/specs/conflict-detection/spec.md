# Delta for Conflict Detection (memory-conflict-surfacing)

## Purpose

Defines the candidate detection behavior on `mem_save` and the annotation behavior
on `mem_search` introduced by Phase 1 of memory-conflict-surfacing. Covers what the
enriched MCP response envelopes MUST contain and what backward-compatibility guarantees
MUST hold for existing clients.

---

## ADDED Requirements

### Requirement: Candidate Detection on mem_save

After every successful `mem_save` (new insert or topic_key revision), the system MUST
run a post-transaction FTS5 candidate query against observations in the same `project`
and `scope`. If one or more candidates pass the BM25 floor, the response JSON envelope
MUST include the fields `candidates[]`, `judgment_id`, `judgment_status: "pending"`, and
`judgment_required: true`. If no candidates pass the floor, those fields MUST be absent
(or `judgment_required: false` with empty `candidates[]`) and the `result` string MUST
be byte-identical to the pre-change format.

Candidate detection MUST NOT block the save. Any error from detection is logged and
swallowed; the observation is saved regardless.

Detection query: FTS5 MATCH on the saved observation's `title` only (the signal-dense
column). Soft-deleted observations and the just-saved observation (excluded by `id`) are
filtered out. Results are limited to the top N by BM25 rank. N defaults to 3 and is
configurable. The BM25 floor defaults to -2.0 and is configurable via
`Config.CandidateBM25Floor` (`*float64`; nil pointer = use default).

Each entry in `candidates[]` MUST include: `id` (INTEGER), `sync_id` (TEXT), `title`
(TEXT), `type` (TEXT), `topic_key` (TEXT or null), `score` (BM25 rank float64), and
`judgment_id` (the `sync_id` of the corresponding pending `memory_relations` row,
prefixed `rel-`).

The top-level `judgment_id` in the envelope is the first candidate's `judgment_id`
(display convenience for single-candidate cases). When multiple candidates exist, agents
MUST iterate `candidates[]` and call `mem_judge` once per entry using
`candidates[i].judgment_id`.

The `result` string MUST append the line "CONFLICT REVIEW PENDING — N candidate(s); use
mem_judge to record verdicts." ONLY when `candidates` is non-empty. This line is a
belt-and-suspenders nudge for agents that parse only the human-readable result text.

#### Scenario: Similar title triggers candidates

- GIVEN a store containing an observation titled "We use sessions for auth"
- AND the observation is in project "engram" with scope "project"
- WHEN `mem_save` is called with title "Switched from sessions to JWT for auth" in the same project and scope
- THEN the response JSON envelope includes a non-empty `candidates` array
- AND each candidate entry has `id`, `sync_id`, `title`, `type`, `score`, and `judgment_id` (prefixed `rel-`)
- AND the top-level `judgment_id` equals `candidates[0].judgment_id`
- AND `judgment_required` is `true`
- AND `judgment_status` is `"pending"`
- AND the `result` string contains "CONFLICT REVIEW PENDING"

#### Scenario: topic_key revision also returns candidates

- GIVEN a store containing observation A titled "JWT auth design" and observation B titled "JWT auth strategy"
- AND A was saved with `topic_key = "architecture/auth"`
- WHEN `mem_save` is called again with `topic_key = "architecture/auth"` and a title similar to B
- THEN candidates are returned (topic_key revisions trigger detection just as new inserts do)
- AND the re-saved observation row is NOT present in its own candidates list

#### Scenario: Just-saved observation excluded from own candidates

- GIVEN an empty store
- WHEN `mem_save` is called with any title
- THEN the saved observation is NOT included in `candidates` regardless of title content

#### Scenario: BM25 floor filters borderline candidates

- GIVEN a store with two observations: one with a title very similar to the incoming title (high BM25 rank) and one borderline (low BM25 rank)
- AND `CandidateBM25Floor` is configured to a value that admits only the high-rank candidate
- WHEN `mem_save` is called with the new title
- THEN `candidates` contains only the high-rank candidate
- AND the borderline observation is absent from `candidates`

#### Scenario: Candidate results capped at 3

- GIVEN a store containing 10 observations all with titles very similar to the incoming title
- WHEN `mem_save` is called
- THEN `candidates` contains at most 3 entries (top 3 by BM25 rank)

#### Scenario: Candidates are scoped to same project and scope

- GIVEN observations exist in project "project-A" and project "project-B" with matching titles
- AND the new observation is saved in project "project-A" with scope "project"
- WHEN candidate detection runs
- THEN only candidates from project "project-A" appear in `candidates`
- AND observations from project "project-B" are excluded

#### Scenario: Unrelated title produces no candidates

- GIVEN a store with observations about authentication, sessions, and JWT
- WHEN `mem_save` is called with the title "Refactored the CSS layout component"
- THEN `candidates` is empty or absent
- AND `judgment_required` is `false`
- AND the `result` string is byte-identical to the pre-change format (no "CONFLICT REVIEW PENDING" line)

---

### Requirement: mem_search Conflict Annotations

Each result entry in `mem_search` output MUST include annotation lines when the
observation is involved in non-orphaned `memory_relations` rows. Annotation lines appear
immediately below the standard result metadata line for that observation.

Phase 1 annotation line formats (title enrichment with `(<title>)` suffix is deferred to
Phase 2; see deferral note below):

```
supersedes: #<id>
superseded_by: #<id>
conflict: contested by #<id> (pending)
```

Semantics:
- `supersedes: #<id>` — emitted when this observation is the `source_id` of a relation
  with `relation = 'supersedes'`.
- `superseded_by: #<id>` — emitted when this observation is the `target_id` of a
  relation with `relation = 'supersedes'`.
- `conflict: contested by #<id> (pending)` — emitted when a relation referencing this
  observation has `judgment_status = 'pending'`.

Relations with `judgment_status = 'orphaned'` MUST be excluded from annotations.

Observations with no non-orphaned relations MUST produce result entries that are
byte-identical to the pre-change format (regression guard).

Relation data for all returned observations in a single search call MUST be fetched in a
single batch query to avoid N+1 round-trips.

#### Scenario: Source observation annotated with supersedes

- GIVEN observation #42 is the `source_id` of a judged relation with `relation = 'supersedes'` targeting observation #18
- WHEN `mem_search` returns observation #42 in results
- THEN the result entry for #42 includes the annotation line `supersedes: #18`

#### Scenario: Target observation annotated with superseded_by

- GIVEN observation #18 is the `target_id` of observation #42's `supersedes` relation
- WHEN `mem_search` returns observation #18 in results
- THEN the result entry for #18 includes the annotation line `superseded_by: #42`

#### Scenario: Pending relation shown as contested

- GIVEN observation #77 has a relation row with `judgment_status = 'pending'` involving observation #91
- WHEN `mem_search` returns observation #77
- THEN the result entry includes the annotation line `conflict: contested by #91 (pending)`

#### Scenario: Orphaned relation excluded from annotations

- GIVEN observation B has one relation with `judgment_status = 'orphaned'` and one with `judgment_status = 'judged'`
- WHEN `mem_search` returns observation B
- THEN only the judged relation's annotation line appears
- AND the orphaned relation produces no annotation line

#### Scenario: No relations means no annotation (regression guard)

- GIVEN an observation has no rows in `memory_relations`
- WHEN `mem_search` returns it
- THEN its result entry is byte-for-byte identical to the format produced before this change

---

### Requirement: Backwards Compatibility of mem_save and mem_search

The `mem_save` JSON response envelope MUST remain backward-compatible. New fields
(`id`, `sync_id`, `candidates`, `judgment_id`, `judgment_status`, `judgment_required`)
are additive; clients that read only the `result` string MUST continue to work without
modification.

The leading line of the `result` string (`Memory saved: "<title>" (<type>)`) MUST be
unchanged in all cases — candidates present or absent.

When no candidates exist, the `result` string MUST be byte-identical to the pre-change
format. `judgment_required` MUST be `false` and no `judgment_id` or `candidates` fields
MUST appear in the envelope.

The `mem_search` result text format MUST be unchanged for observations with no relation
markers. Annotation lines are strictly additive and appear only when applicable.

#### Scenario: Old client reads only result string without error

- GIVEN a client that extracts only the `result` field from the `mem_save` JSON envelope
- WHEN `mem_save` returns a response containing `candidates`, `judgment_id`, and `judgment_required`
- THEN the client reads `result` successfully (unknown JSON fields are ignored per JSON specification)
- AND the `result` string starts with `Memory saved: "`

#### Scenario: No-candidate response is unchanged from pre-change format

- GIVEN an empty store (or a store with no FTS5 matches for the incoming title)
- WHEN `mem_save` is called
- THEN the `result` string is unchanged from the pre-change format
- AND no `candidates`, `judgment_id`, or `judgment_required` fields appear in the envelope
  (or `judgment_required` is `false` with empty/absent `candidates`)

#### Scenario: Existing mem_search format preserved for unrelated observations

- GIVEN observations A and B have no rows in `memory_relations`
- WHEN `mem_search` returns both
- THEN the result text for each is identical to what it would have produced before this change

#### Scenario: Existing result string assertions continue to pass

- GIVEN a test that asserts the `mem_save` result string starts with `Memory saved: "`
- WHEN the enriched `handleSave` runs — regardless of whether candidates are found or not
- THEN that assertion still passes

---

## Phase 1 Deferral Notes

- Annotation title enrichment: `(<title>)` suffix on annotation lines is deferred to Phase 2.
  Current Phase 1 format uses `#<id>` only. This is documented in `docs/PLUGINS.md`.
- pgvector / embedding generation: columns `embedding`, `embedding_model`,
  `embedding_created_at` exist on `observations` as nullable placeholders; no generation
  pipeline runs in Phase 1.
- Decay query activation (`engram review` command): `review_after` and `expires_at` are
  populated (see decay-defaults spec) but no query reads them in Phase 1.
