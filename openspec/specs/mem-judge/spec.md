# Spec: mem_judge Tool

This specification defines the `mem_judge` MCP tool contract, the agent behavior heuristics encoded in `serverInstructions`, the full conflict-loop integration requirements, and the documentation requirements. Together these ensure agents can surface, judge, and track memory conflicts end-to-end.

---

## Requirements

### Requirement: mem_judge MCP Tool

The system MUST expose a `mem_judge` tool registered in `ProfileAgent` (always available; not behind ToolSearch or a deferred profile). Agents need `mem_judge` eagerly because conflict surfacing is reactive within the same `mem_save` round-trip.

Input parameters:

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `judgment_id` | string | yes | The relation's `sync_id` from `candidates[i].judgment_id` in the `mem_save` response |
| `relation` | string | yes | One of: `related`, `compatible`, `scoped`, `conflicts_with`, `supersedes`, `not_conflict` |
| `reason` | string | no | One-sentence natural-language rationale |
| `evidence` | string | no | Free-form; recommended format: JSON `{"basis":"...","key_phrases":["..."]}` |
| `confidence` | number | no | 0.0..1.0; clamped with a warning if out of bounds |

On success, the `memory_relations` row identified by `judgment_id` MUST be updated with the new verdict: `relation`, `reason`, `evidence`, `confidence`, `marked_by_actor`, `marked_by_kind`, `marked_by_model`, `session_id`, and `updated_at` are written; `judgment_status` flips to `'judged'`. The updated relation row MUST be returned in the response.

`mem_judge` MUST NOT mutate the target observation. Recording a `supersedes` verdict does not retract or soft-delete the target — that is a separate deliberate action by the agent.

Re-judging an already-judged row MUST overwrite the verdict and advance `updated_at`. This is a deliberate revision, not an error.

`confidence` outside [0.0, 1.0] MUST be clamped to the range boundary; a warning line MUST appear in the `result` string describing the clamping.

#### Scenario: Happy path — agent records a verdict

- GIVEN a pending relation with `judgment_id = "rel-abc123def45678"` exists in `memory_relations`
- WHEN `mem_judge` is called with `judgment_id = "rel-abc123def45678"`, `relation = "not_conflict"`, `confidence = 0.9`, `reason = "different scope: A is local, B is cloud"`
- THEN the relation row has `judgment_status = 'judged'`, `relation = 'not_conflict'`, `confidence = 0.9`, `reason = "different scope: A is local, B is cloud"`
- AND `updated_at` is advanced relative to its previous value
- AND the updated relation row is returned in the response (not `IsError`)

#### Scenario: Optional fields remain NULL when omitted

- GIVEN a pending relation exists
- WHEN `mem_judge` is called with only `judgment_id` and `relation` (no reason, evidence, or confidence)
- THEN the stored row has `reason = NULL`, `evidence = NULL`, and `confidence = NULL`
- AND `judgment_status = 'judged'`

#### Scenario: Unknown judgment_id returns typed error

- GIVEN no relation with `sync_id = "rel-does-not-exist"` exists in `memory_relations`
- WHEN `mem_judge` is called with `judgment_id = "rel-does-not-exist"`
- THEN the response has `IsError: true` with a message containing "unknown judgment_id"
- AND no row in `memory_relations` is mutated

#### Scenario: Invalid relation verb rejected

- GIVEN a pending relation with `judgment_id = "rel-valid123"` exists
- WHEN `mem_judge` is called with `relation = "invalidverb"`
- THEN the response has `IsError: true`
- AND the relation row retains `judgment_status = 'pending'` and its original `relation` value

#### Scenario: Confidence clamped with warning

- GIVEN a pending relation exists
- WHEN `mem_judge` is called with `confidence = 1.5`
- THEN the stored `confidence` is clamped to 1.0
- AND the `result` string contains a warning message about the clamping
- AND `judgment_status = 'judged'` (the call succeeds)

#### Scenario: Re-judging overwrites with new provenance (idempotent overwrite)

- GIVEN a relation row with `judgment_status = 'judged'` and `relation = 'compatible'` and an `updated_at` timestamp T1
- WHEN `mem_judge` is called again on the same `judgment_id` with `relation = 'conflicts_with'` and a new `confidence` value
- THEN the row now has `relation = 'conflicts_with'` and the new confidence
- AND `updated_at` is later than T1
- AND no error is returned

---

### Requirement: Agent Behavior Heuristics in serverInstructions

The MCP `serverInstructions` constant MUST include a `CONFLICT SURFACING` block appended to the existing instructions. The block documents:

1. How to detect that judgment is needed: check for `judgment_required: true` in the `mem_save` JSON envelope, or "CONFLICT REVIEW PENDING" in the `result` string.
2. Iteration pattern: call `mem_judge` once per candidate using `candidates[i].judgment_id`. The top-level `judgment_id` is a display convenience for the first candidate only.
3. When to ASK the user (conversationally — never via blocking CLI prompt or dashboard):
   - `confidence < 0.7`, OR
   - the intended `relation` is in `{supersedes, conflicts_with}` AND the saved observation's `type` is in `{architecture, policy, decision}`.
4. When to RESOLVE SILENTLY (call `mem_judge` without asking the user):
   - `confidence >= 0.7` AND `relation` is in `{not_conflict, related, compatible, scoped}`.
5. Conversational pattern for raising a conflict: surface it in the agent's next reply as a natural observation.

#### Scenario: serverInstructions contains the CONFLICT SURFACING block

- GIVEN the MCP server starts and exposes its instructions
- WHEN the `serverInstructions` string is inspected
- THEN it contains the literal text "CONFLICT SURFACING"
- AND `go build ./...` passes with no compile errors

#### Scenario: Agent asks user before recording supersedes on architecture observation

- GIVEN `mem_save` returns `judgment_required = true` with candidates
- AND the saved observation has `type = "architecture"`
- AND the agent's assessed verdict is `relation = "supersedes"`
- WHEN the agent processes the serverInstructions heuristics
- THEN the agent raises the conflict in its next conversational reply to the user
- AND does NOT call `mem_judge` unilaterally before receiving user input

#### Scenario: Agent resolves not_conflict silently

- GIVEN `mem_save` returns `judgment_required = true` with a candidate
- AND the agent's assessed verdict is `relation = "not_conflict"` with `confidence = 0.85`
- WHEN the agent processes the serverInstructions heuristics
- THEN the agent calls `mem_judge` with `relation = "not_conflict"` without asking the user

---

### Requirement: Full Conflict Loop (save → judge → search)

An agent MUST be able to complete the full conflict surfacing loop within a single session: save a new observation that triggers candidates, call `mem_judge` per candidate, and then observe the judgment reflected as annotation lines in subsequent `mem_search` results. This loop is the primary agent-facing workflow.

#### Scenario: Full loop — save, judge, search reflects verdict

- GIVEN observation A exists with title "We use sessions for auth" (project "engram", scope "project")
- WHEN observation B is saved with title "Switched from sessions to JWT for auth" in the same project and scope
- THEN the `mem_save` response includes `judgment_required = true` and `candidates` containing A
- WHEN the agent calls `mem_judge` with `candidates[0].judgment_id` and `relation = "supersedes"`
- THEN the relation row has `judgment_status = 'judged'` and `relation = 'supersedes'`
- WHEN the agent calls `mem_search` and A appears in results
- THEN A's result entry includes the annotation line `superseded_by: #<B.id>`
- AND B's result entry (if also returned) includes `supersedes: #<A.id>`

#### Scenario: Multi-actor loop — two independent judgments for the same pair

- GIVEN observation A and observation B exist with similar titles
- AND agent-1 saves C (similar to A) and receives candidate A with `judgment_id = "rel-j1"`
- AND agent-2 saves D (similar to A) and receives candidate A with `judgment_id = "rel-j2"`
- WHEN agent-1 calls `mem_judge` with `judgment_id = "rel-j1"` and `relation = "compatible"`
- AND agent-2 calls `mem_judge` with `judgment_id = "rel-j2"` and `relation = "conflicts_with"`
- THEN both relation rows exist in `memory_relations` with their respective verdicts
- AND `mem_search` for A can surface both annotations

#### Scenario: Orphan loop — hard-delete source clears annotation

- GIVEN a judged `supersedes` relation where observation B (source) supersedes observation A (target)
- AND `mem_search` for A shows `superseded_by: #<B.id>`
- WHEN observation B is hard-deleted
- THEN the relation row's `judgment_status` becomes `'orphaned'`
- AND a subsequent `mem_search` returning A does NOT show the `superseded_by: #<B.id>` line

#### Scenario: Sync regression — relations do not enter sync_mutations

- GIVEN cloud sync enrollment is active
- WHEN `mem_judge` is called and a relation row is written to `memory_relations`
- THEN no new row appears in `sync_mutations`
- AND observation sync payload does NOT carry `review_after` or `embedding*` fields

---

### Requirement: mem_judge Documentation

The project documentation MUST be updated to cover the new conflict surfacing surfaces, so that agents and integrators can use them without reading the source code.

`docs/AGENT-SETUP.md` MUST include:
- `mem_judge` tool entry: params, when it fires (after `judgment_required = true` in `mem_save` response), and an example conversational flow for an ambiguous case where the agent asks the user before recording a verdict.
- Explicit note on Phase 1 deferrals: cloud sync of `memory_relations`, the `engram review` decay command, and pgvector embedding generation are not yet active.

The MCP tools reference (`docs/PLUGINS.md` or equivalent) MUST include:
- `mem_judge` tool entry with the full parameter table and the response shape.
- Updated `mem_save` entry documenting the new response fields: `id`, `sync_id`, `candidates[]`, `judgment_id`, `judgment_status`, `judgment_required`.
- Updated `mem_search` entry documenting the annotation format: `supersedes: #<id>`, `superseded_by: #<id>`, `conflict: contested by #<id> (pending)`.

#### Scenario: AGENT-SETUP.md documents mem_judge

- GIVEN a developer or agent reads `docs/AGENT-SETUP.md`
- WHEN they search for "mem_judge"
- THEN they find documentation covering: when the tool is triggered, required params, and an example conversational flow

#### Scenario: PLUGINS.md documents new mem_save fields

- GIVEN a developer reads the MCP tools reference
- WHEN they look up the `mem_save` entry
- THEN they find documentation of the new response fields and the annotation format for `mem_search` results

---

## Phase 1 Deferral Notes

- Retraction of target on `supersedes` verdict: `mem_judge` records judgment only; mutating the target is a separate deliberate agent action.
- Multi-actor disagreement resolution: schema permits multiple verdicts; resolution algorithm is Phase 2.
- Cloud sync of `memory_relations`: deferred to Phase 2.
- `mem_judge` verdict vocabulary extension: locked verbs in Phase 1; extension in Phase 2 without breaking Phase 1 clients.
