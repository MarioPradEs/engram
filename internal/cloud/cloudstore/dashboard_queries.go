package cloudstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrDashboardProjectInvalid = errors.New("cloudstore: dashboard project is invalid")
var ErrDashboardProjectForbidden = errors.New("cloudstore: dashboard project is outside allowed scope")
var ErrDashboardProjectNotFound = errors.New("cloudstore: dashboard project not found")

// ErrDashboardIdentityRequired is returned when a non-admin caller has no resolved email.
// This is the DENY path: missing identity must never fall through to an unscoped result.
var ErrDashboardIdentityRequired = errors.New("cloudstore: dashboard caller identity required")

// ReadScope carries the authenticated dashboard caller's identity for read-side scoping.
// Mirrors MutationScopeFilter (cloudstore.go:1427) on the read path.
//
//	IsAdmin == true  → unscoped (all rows)
//	IsAdmin == false → rows where row.UserEmail == Email (case-insensitive)
//	Email == "" && !IsAdmin → DENY: return nil + ErrDashboardIdentityRequired
type ReadScope struct {
	Email   string
	IsAdmin bool
}

// applyReadScope filters rows by the caller's identity.
// It is a pure generic function — one testable predicate for ALL row types.
//
//   - nil scope or IsAdmin == true → all rows returned unmodified
//   - IsAdmin == false, Email non-empty → only rows where emailOf(row) matches Email (case-insensitive)
//   - IsAdmin == false, Email empty → nil, ErrDashboardIdentityRequired (DENY — never leak)
func applyReadScope[T any](scope *ReadScope, rows []T, emailOf func(T) string) ([]T, error) {
	if scope == nil || scope.IsAdmin {
		return rows, nil
	}
	email := strings.ToLower(strings.TrimSpace(scope.Email))
	if email == "" {
		return nil, ErrDashboardIdentityRequired
	}
	out := rows[:0:0]
	for _, r := range rows {
		if strings.EqualFold(strings.TrimSpace(emailOf(r)), email) {
			out = append(out, r)
		}
	}
	return out, nil
}

// ErrDashboardContributorNotFound is returned when GetContributorDetail cannot find the named contributor.
// R4-7: Use a dedicated error so classifyStoreError can return "Contributor not found" instead of "Project not found".
var ErrDashboardContributorNotFound = errors.New("cloudstore: dashboard contributor not found")

// R5-4: Dedicated not-found errors for session, observation, and prompt detail lookups.
// Using project-not-found for these was misleading — users would see "Project not found"
// when actually only the session/observation/prompt was missing within a valid project.
var ErrDashboardSessionNotFound = errors.New("cloudstore: dashboard session not found")
var ErrDashboardObservationNotFound = errors.New("cloudstore: dashboard observation not found")
var ErrDashboardPromptNotFound = errors.New("cloudstore: dashboard prompt not found")

type DashboardProjectRow struct {
	Project      string
	Chunks       int
	Sessions     int
	Observations int
	Prompts      int
}

type DashboardContributorRow struct {
	CreatedBy         string
	Chunks            int
	Projects          int
	LastChunkAt       string
	LastClientVersion string // "" means not yet recorded (render as "unknown")
}

type DashboardSessionRow struct {
	Project   string
	SessionID string
	StartedAt string
	EndedAt   string // NEW — populated from session close chunk if available
	Summary   string // NEW — from session chunk summary field
	Directory string // NEW — from session chunk directory field
	UserEmail string // NEW (D2) — per-session attribution; from the originating chunk's created_by
}

type DashboardObservationRow struct {
	Project   string
	SessionID string
	SyncID    string // unique map key — used for detail page URL segment
	ChunkID   string // chunk the observation was first written in (preserved across mutations)
	Type      string
	Title     string
	Content   string // NEW — materialized from chunk payload
	TopicKey  string // NEW — from observation payload
	ToolName  string // NEW — from observation payload
	CreatedAt string
	UserEmail string // NEW (D2) — per-observation attribution: cloud_mutations.user_email wins,
	// falls back to originating chunk's created_by (legacy path with no per-mutation user_email).
}

type DashboardPromptRow struct {
	Project   string
	SessionID string
	SyncID    string // unique map key — used for detail page URL segment
	ChunkID   string // chunk the prompt was first written in (preserved across mutations)
	Content   string
	CreatedAt string
	UserEmail string // NEW (D2) — per-prompt attribution; from the originating chunk's created_by
}

// DashboardSystemHealth holds aggregate metrics for the admin health page.
// Satisfies REQ-105 / AD-3.
type DashboardSystemHealth struct {
	DBConnected  bool
	Projects     int
	Contributors int
	Sessions     int
	Observations int
	Prompts      int
	Chunks       int
}

type DashboardAdminOverview struct {
	Projects     int
	Contributors int
	Chunks       int
	// Member-scoped counts — populated only by ScopedMemberOverview for non-admin callers.
	// Zero for admin (admin stats use Projects/Contributors/Chunks above).
	Observations int
	Sessions     int
	Prompts      int
}

type DashboardProjectDetail struct {
	Project      string
	Stats        DashboardProjectRow
	Contributors []DashboardContributorRow
	Sessions     []DashboardSessionRow
	Observations []DashboardObservationRow
	Prompts      []DashboardPromptRow
}

type dashboardReadModel struct {
	projects       []DashboardProjectRow
	contributors   []DashboardContributorRow
	projectDetails map[string]DashboardProjectDetail
	admin          DashboardAdminOverview
}

type dashboardEntityKey struct {
	project   string
	entityKey string
}

func newDashboardEntityKey(project, entityKey string) dashboardEntityKey {
	return dashboardEntityKey{
		project:   strings.TrimSpace(project),
		entityKey: strings.TrimSpace(entityKey),
	}
}

func buildDashboardReadModel(chunks []dashboardChunkRow) (dashboardReadModel, error) {
	return buildDashboardReadModelFromRows(chunks, nil)
}

func buildDashboardReadModelFromRows(chunks []dashboardChunkRow, mutationRows []dashboardMutationRow) (dashboardReadModel, error) {
	ordered := append([]dashboardChunkRow(nil), chunks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].createdAt.Equal(ordered[j].createdAt) {
			return ordered[i].createdAt.Before(ordered[j].createdAt)
		}
		if ordered[i].chunkID != ordered[j].chunkID {
			return ordered[i].chunkID < ordered[j].chunkID
		}
		if ordered[i].project != ordered[j].project {
			return ordered[i].project < ordered[j].project
		}
		return ordered[i].createdBy < ordered[j].createdBy
	})

	projectChunkCounts := make(map[string]int)
	contributors := make(map[string]*DashboardContributorRow)
	contributorProjects := make(map[string]map[string]struct{})
	projectContributors := make(map[string]map[string]*DashboardContributorRow)

	sessions := make(map[dashboardEntityKey]DashboardSessionRow)
	observations := make(map[dashboardEntityKey]DashboardObservationRow)
	prompts := make(map[dashboardEntityKey]DashboardPromptRow)

	for _, chunk := range ordered {
		project := strings.TrimSpace(chunk.project)
		if project == "" {
			continue
		}
		projectChunkCounts[project]++

		for _, session := range chunk.parsed.Sessions {
			endedAt := ""
			if session.EndedAt != nil {
				endedAt = *session.EndedAt
			}
			summary := ""
			if session.Summary != nil {
				summary = *session.Summary
			}
			// R4-1: Preserve fields from prior sessions-array passes (e.g. open chunk
			// sets started_at+project; close chunk sets ended_at+summary but omits them).
			// Inherit any non-empty field from the existing row before calling upsert.
			trimmedID := strings.TrimSpace(session.ID)
			sessionProject := resolveProjectValue(session.Project, project)
			if existing, ok := sessions[newDashboardEntityKey(sessionProject, trimmedID)]; ok {
				if session.StartedAt == "" {
					session.StartedAt = existing.StartedAt
				}
				if endedAt == "" {
					endedAt = existing.EndedAt
				}
				if summary == "" {
					summary = existing.Summary
				}
				if session.Directory == "" {
					session.Directory = existing.Directory
				}
				if strings.TrimSpace(session.Project) == "" && existing.Project != "" {
					// Resolve project below will use existing.Project as primary if session.Project is empty.
					session.Project = existing.Project
				}
			}
			upsertDashboardSession(sessions, session.ID, sessionProject, session.StartedAt, endedAt, summary, session.Directory, chunk.createdBy)
		}
		for _, obs := range chunk.parsed.Observations {
			obsProject := project
			if obs.Project != nil {
				obsProject = resolveProjectValue(*obs.Project, project)
			}
			if obs.DeletedAt != nil && strings.TrimSpace(*obs.DeletedAt) != "" {
				delete(observations, newDashboardEntityKey(obsProject, obs.SyncID))
				continue
			}
			topicKey := ""
			if obs.TopicKey != nil {
				topicKey = *obs.TopicKey
			}
			toolName := ""
			if obs.ToolName != nil {
				toolName = *obs.ToolName
			}
			upsertDashboardObservation(observations, obs.SyncID, obsProject, obs.SessionID, obs.Type, obs.Title, obs.Content, topicKey, toolName, chunk.chunkID, obs.CreatedAt, chunk.createdBy)
		}
		for _, prompt := range chunk.parsed.Prompts {
			upsertDashboardPrompt(prompts, prompt.SyncID, resolveProjectValue(prompt.Project, project), prompt.SessionID, prompt.Content, chunk.chunkID, prompt.CreatedAt, chunk.createdBy)
		}

		for _, mutation := range chunk.parsed.Mutations {
			if err := applyDashboardMutation(project, mutation, sessions, observations, prompts, chunk.createdBy); err != nil {
				return dashboardReadModel{}, fmt.Errorf("cloudstore: invalid dashboard mutation payload in chunk %q: %w", strings.TrimSpace(chunk.chunkID), err)
			}
		}

		creator := strings.TrimSpace(chunk.createdBy)
		if creator != "" {
			if _, ok := contributors[creator]; !ok {
				contributors[creator] = &DashboardContributorRow{CreatedBy: creator}
			}
			contributors[creator].Chunks++
			if contributorProjects[creator] == nil {
				contributorProjects[creator] = make(map[string]struct{})
			}
			contributorProjects[creator][project] = struct{}{}

			if projectContributors[project] == nil {
				projectContributors[project] = make(map[string]*DashboardContributorRow)
			}
			if _, ok := projectContributors[project][creator]; !ok {
				projectContributors[project][creator] = &DashboardContributorRow{CreatedBy: creator}
			}
			projectContributors[project][creator].Chunks++

			if chunk.createdAt.After(parseRFC3339(contributors[creator].LastChunkAt)) {
				contributors[creator].LastChunkAt = chunk.createdAt.UTC().Format(time.RFC3339)
			}
			if chunk.createdAt.After(parseRFC3339(projectContributors[project][creator].LastChunkAt)) {
				projectContributors[project][creator].LastChunkAt = chunk.createdAt.UTC().Format(time.RFC3339)
			}
		}
	}

	orderedMutations := append([]dashboardMutationRow(nil), mutationRows...)
	sort.SliceStable(orderedMutations, func(i, j int) bool {
		if orderedMutations[i].seq != orderedMutations[j].seq {
			return orderedMutations[i].seq < orderedMutations[j].seq
		}
		if !orderedMutations[i].occurredAt.Equal(orderedMutations[j].occurredAt) {
			return orderedMutations[i].occurredAt.Before(orderedMutations[j].occurredAt)
		}
		if orderedMutations[i].project != orderedMutations[j].project {
			return orderedMutations[i].project < orderedMutations[j].project
		}
		if orderedMutations[i].entity != orderedMutations[j].entity {
			return orderedMutations[i].entity < orderedMutations[j].entity
		}
		return orderedMutations[i].entityKey < orderedMutations[j].entityKey
	})
	for _, mutationRow := range orderedMutations {
		mutation := store.SyncMutation{
			Seq:        mutationRow.seq,
			Entity:     mutationRow.entity,
			EntityKey:  mutationRow.entityKey,
			Op:         mutationRow.op,
			Payload:    strings.TrimSpace(string(mutationRow.payload)),
			Project:    mutationRow.project,
			OccurredAt: mutationRow.occurredAt.UTC().Format(time.RFC3339),
		}
		if err := applyDashboardMutation(mutationRow.project, mutation, sessions, observations, prompts, mutationRow.userEmail); err != nil {
			return dashboardReadModel{}, fmt.Errorf("cloudstore: invalid dashboard mutation payload in cloud_mutations seq %d: %w", mutationRow.seq, err)
		}
	}
	pruneDashboardEntitiesWithoutCloudMutation(orderedMutations, sessions, observations, prompts)

	projects := make(map[string]*DashboardProjectRow, len(projectChunkCounts))
	detailSessions := make(map[string][]DashboardSessionRow)
	detailObservations := make(map[string][]DashboardObservationRow)
	detailPrompts := make(map[string][]DashboardPromptRow)

	for project, chunks := range projectChunkCounts {
		projects[project] = &DashboardProjectRow{Project: project, Chunks: chunks}
	}

	for _, row := range sessions {
		if strings.TrimSpace(row.Project) == "" {
			continue
		}
		if _, ok := projects[row.Project]; !ok {
			projects[row.Project] = &DashboardProjectRow{Project: row.Project}
		}
		projects[row.Project].Sessions++
		detailSessions[row.Project] = append(detailSessions[row.Project], row)
	}
	for _, row := range observations {
		if strings.TrimSpace(row.Project) == "" {
			continue
		}
		if _, ok := projects[row.Project]; !ok {
			projects[row.Project] = &DashboardProjectRow{Project: row.Project}
		}
		projects[row.Project].Observations++
		detailObservations[row.Project] = append(detailObservations[row.Project], row)
	}
	for _, row := range prompts {
		if strings.TrimSpace(row.Project) == "" {
			continue
		}
		if _, ok := projects[row.Project]; !ok {
			projects[row.Project] = &DashboardProjectRow{Project: row.Project}
		}
		projects[row.Project].Prompts++
		detailPrompts[row.Project] = append(detailPrompts[row.Project], row)
	}

	projectRows := make([]DashboardProjectRow, 0, len(projects))
	projectDetails := make(map[string]DashboardProjectDetail, len(projects))
	for project, row := range projects {
		projectRows = append(projectRows, *row)

		contributorsForProject := make([]DashboardContributorRow, 0, len(projectContributors[project]))
		for _, contributorRow := range projectContributors[project] {
			contributorsForProject = append(contributorsForProject, *contributorRow)
		}
		sort.Slice(contributorsForProject, func(i, j int) bool {
			if contributorsForProject[i].Chunks != contributorsForProject[j].Chunks {
				return contributorsForProject[i].Chunks > contributorsForProject[j].Chunks
			}
			return contributorsForProject[i].CreatedBy < contributorsForProject[j].CreatedBy
		})

		sessionsForProject := append([]DashboardSessionRow(nil), detailSessions[project]...)
		sort.Slice(sessionsForProject, func(i, j int) bool {
			return sessionsForProject[i].StartedAt > sessionsForProject[j].StartedAt
		})

		observationsForProject := append([]DashboardObservationRow(nil), detailObservations[project]...)
		sort.Slice(observationsForProject, func(i, j int) bool {
			return observationsForProject[i].CreatedAt > observationsForProject[j].CreatedAt
		})

		promptsForProject := append([]DashboardPromptRow(nil), detailPrompts[project]...)
		sort.Slice(promptsForProject, func(i, j int) bool {
			return promptsForProject[i].CreatedAt > promptsForProject[j].CreatedAt
		})

		projectDetails[project] = DashboardProjectDetail{
			Project:      project,
			Stats:        *row,
			Contributors: contributorsForProject,
			Sessions:     sessionsForProject,
			Observations: observationsForProject,
			Prompts:      promptsForProject,
		}
	}

	sort.Slice(projectRows, func(i, j int) bool { return projectRows[i].Project < projectRows[j].Project })

	contributorRows := make([]DashboardContributorRow, 0, len(contributors))
	for _, row := range contributors {
		rowCopy := *row
		rowCopy.Projects = len(contributorProjects[row.CreatedBy])
		contributorRows = append(contributorRows, rowCopy)
	}
	sort.Slice(contributorRows, func(i, j int) bool {
		if contributorRows[i].Chunks != contributorRows[j].Chunks {
			return contributorRows[i].Chunks > contributorRows[j].Chunks
		}
		return contributorRows[i].CreatedBy < contributorRows[j].CreatedBy
	})

	totalChunks := 0
	for _, row := range projectRows {
		totalChunks += row.Chunks
	}

	return dashboardReadModel{
		projects:       projectRows,
		contributors:   contributorRows,
		projectDetails: projectDetails,
		admin: DashboardAdminOverview{
			Projects:     len(projectRows),
			Contributors: len(contributorRows),
			Chunks:       totalChunks,
		},
	}, nil
}

func pruneDashboardEntitiesWithoutCloudMutation(
	mutationRows []dashboardMutationRow,
	sessions map[dashboardEntityKey]DashboardSessionRow,
	observations map[dashboardEntityKey]DashboardObservationRow,
	prompts map[dashboardEntityKey]DashboardPromptRow,
) {
	// cloud_chunks remain audit history, but once cloud_mutations has rows for an
	// entity type, dashboard browser counts should reflect the mutation-backed
	// current-state keys instead of treating every historical chunk payload row as
	// currently visible.
	currentKeys := make(map[string]map[string]map[string]struct{})
	for _, row := range mutationRows {
		entity := strings.TrimSpace(row.entity)
		project := strings.TrimSpace(row.project)
		key := strings.TrimSpace(row.entityKey)
		if entity == "" || project == "" || key == "" {
			continue
		}
		if currentKeys[entity] == nil {
			currentKeys[entity] = make(map[string]map[string]struct{})
		}
		if currentKeys[entity][project] == nil {
			currentKeys[entity][project] = make(map[string]struct{})
		}
		currentKeys[entity][project][key] = struct{}{}
	}

	if keys, ok := currentKeys[store.SyncEntityObservation]; ok {
		pruneDashboardRowsWithoutCloudMutation(observations, keys, func(row DashboardObservationRow) string { return row.Project }, nil)
	}
	if keys, ok := currentKeys[store.SyncEntityPrompt]; ok {
		pruneDashboardRowsWithoutCloudMutation(prompts, keys, func(row DashboardPromptRow) string { return row.Project }, nil)
	}

	referencedSessions := make(map[string]map[string]struct{})
	addReferencedSession := func(project, sessionID string) {
		project = strings.TrimSpace(project)
		sessionID = strings.TrimSpace(sessionID)
		if project == "" || sessionID == "" {
			return
		}
		if referencedSessions[project] == nil {
			referencedSessions[project] = make(map[string]struct{})
		}
		referencedSessions[project][sessionID] = struct{}{}
	}
	for _, row := range observations {
		addReferencedSession(row.Project, row.SessionID)
	}
	for _, row := range prompts {
		addReferencedSession(row.Project, row.SessionID)
	}
	if keys, ok := currentKeys[store.SyncEntitySession]; ok {
		pruneDashboardRowsWithoutCloudMutation(sessions, keys, func(row DashboardSessionRow) string { return row.Project }, referencedSessions)
	}
}

func pruneDashboardRowsWithoutCloudMutation[T any](rows map[dashboardEntityKey]T, currentKeys map[string]map[string]struct{}, projectOf func(T) string, preserveKeys map[string]map[string]struct{}) {
	for key := range rows {
		trimmedKey := strings.TrimSpace(key.entityKey)
		project := strings.TrimSpace(projectOf(rows[key]))
		if keys, ok := preserveKeys[project]; ok {
			if _, preserved := keys[trimmedKey]; preserved {
				continue
			}
		}
		keys, ok := currentKeys[project]
		if !ok {
			continue
		}
		if _, ok := keys[trimmedKey]; !ok {
			delete(rows, key)
		}
	}
}

func resolveProjectValue(primary string, fallback string) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func upsertDashboardSession(sessions map[dashboardEntityKey]DashboardSessionRow, sessionID, project, startedAt, endedAt, summary, directory, userEmail string) {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		return
	}
	trimmedProject := strings.TrimSpace(project)
	sessions[newDashboardEntityKey(trimmedProject, key)] = DashboardSessionRow{
		Project:   trimmedProject,
		SessionID: key,
		StartedAt: strings.TrimSpace(startedAt),
		EndedAt:   strings.TrimSpace(endedAt),
		Summary:   strings.TrimSpace(summary),
		Directory: strings.TrimSpace(directory),
		UserEmail: strings.TrimSpace(userEmail),
	}
}

func upsertDashboardObservation(observations map[dashboardEntityKey]DashboardObservationRow, syncID, project, sessionID, obsType, title, content, topicKey, toolName, chunkID, createdAt, userEmail string) {
	key := strings.TrimSpace(syncID)
	if key == "" {
		return
	}
	trimmedProject := strings.TrimSpace(project)
	observations[newDashboardEntityKey(trimmedProject, key)] = DashboardObservationRow{
		SyncID:    key,
		Project:   trimmedProject,
		SessionID: strings.TrimSpace(sessionID),
		ChunkID:   strings.TrimSpace(chunkID),
		Type:      strings.TrimSpace(obsType),
		Title:     strings.TrimSpace(title),
		Content:   strings.TrimSpace(content),
		TopicKey:  strings.TrimSpace(topicKey),
		ToolName:  strings.TrimSpace(toolName),
		CreatedAt: strings.TrimSpace(createdAt),
		UserEmail: strings.TrimSpace(userEmail),
	}
}

func upsertDashboardPrompt(prompts map[dashboardEntityKey]DashboardPromptRow, syncID, project, sessionID, content, chunkID, createdAt, userEmail string) {
	key := strings.TrimSpace(syncID)
	if key == "" {
		return
	}
	trimmedProject := strings.TrimSpace(project)
	prompts[newDashboardEntityKey(trimmedProject, key)] = DashboardPromptRow{
		SyncID:    key,
		Project:   trimmedProject,
		SessionID: strings.TrimSpace(sessionID),
		ChunkID:   strings.TrimSpace(chunkID),
		Content:   strings.TrimSpace(content),
		CreatedAt: strings.TrimSpace(createdAt),
		UserEmail: strings.TrimSpace(userEmail),
	}
}

type dashboardSessionMutationPayload struct {
	ID         string  `json:"id"`
	Project    string  `json:"project"`
	StartedAt  string  `json:"started_at,omitempty"`
	EndedAt    string  `json:"ended_at,omitempty"`
	Summary    string  `json:"summary,omitempty"`
	Directory  string  `json:"directory,omitempty"`
	Deleted    bool    `json:"deleted,omitempty"`
	DeletedAt  *string `json:"deleted_at,omitempty"`
	HardDelete bool    `json:"hard_delete,omitempty"`
}

type dashboardObservationMutationPayload struct {
	SyncID     string  `json:"sync_id"`
	SessionID  string  `json:"session_id"`
	Type       string  `json:"type"`
	Title      string  `json:"title"`
	Content    string  `json:"content,omitempty"`
	TopicKey   string  `json:"topic_key,omitempty"`
	ToolName   string  `json:"tool_name,omitempty"`
	Project    *string `json:"project,omitempty"`
	CreatedAt  string  `json:"created_at,omitempty"`
	Deleted    bool    `json:"deleted,omitempty"`
	HardDelete bool    `json:"hard_delete,omitempty"`
}

type dashboardPromptMutationPayload struct {
	SyncID     string  `json:"sync_id"`
	SessionID  string  `json:"session_id"`
	Content    string  `json:"content"`
	Project    *string `json:"project,omitempty"`
	CreatedAt  string  `json:"created_at,omitempty"`
	Deleted    bool    `json:"deleted,omitempty"`
	HardDelete bool    `json:"hard_delete,omitempty"`
}

// applyDashboardMutation applies a single mutation to the in-memory maps.
// mutationUserEmail carries the cloud_mutations.user_email value (empty string for legacy rows).
// When non-empty it WINS over the existing chunk-derived UserEmail on an observation (D2: authoritative attribution).
func applyDashboardMutation(
	chunkProject string,
	mutation store.SyncMutation,
	sessions map[dashboardEntityKey]DashboardSessionRow,
	observations map[dashboardEntityKey]DashboardObservationRow,
	prompts map[dashboardEntityKey]DashboardPromptRow,
	mutationUserEmail string,
) error {
	entity := strings.TrimSpace(mutation.Entity)
	op := strings.TrimSpace(mutation.Op)
	entityKey := strings.TrimSpace(mutation.EntityKey)

	switch entity {
	case store.SyncEntitySession:
		var body dashboardSessionMutationPayload
		if err := chunkcodec.DecodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return err
		}
		key := resolveProjectValue(entityKey, body.ID)
		project := resolveProjectValue(body.Project, chunkProject)
		if op == store.SyncOpDelete || body.Deleted || body.HardDelete || (body.DeletedAt != nil && strings.TrimSpace(*body.DeletedAt) != "") {
			delete(sessions, newDashboardEntityKey(project, key))
			return nil
		}
		// C3 + R2-5: Preserve close fields and StartedAt from a prior sessions-array pass.
		// Mutations may omit started_at or close fields; values from the prior pass must survive.
		existingStartedAt := ""
		existingEndedAt := ""
		existingSummary := ""
		existingDirectory := ""
		existingSessionUserEmail := ""
		if existing, ok := sessions[newDashboardEntityKey(project, key)]; ok {
			existingStartedAt = existing.StartedAt
			existingEndedAt = existing.EndedAt
			existingSummary = existing.Summary
			existingDirectory = existing.Directory
			existingSessionUserEmail = existing.UserEmail
		}
		resolvedStartedAt := body.StartedAt
		if resolvedStartedAt == "" {
			resolvedStartedAt = existingStartedAt
		}
		resolvedEndedAt := body.EndedAt
		if resolvedEndedAt == "" {
			resolvedEndedAt = existingEndedAt
		}
		resolvedSummary := body.Summary
		if resolvedSummary == "" {
			resolvedSummary = existingSummary
		}
		resolvedDirectory := body.Directory
		if resolvedDirectory == "" {
			resolvedDirectory = existingDirectory
		}
		// D2: preserve-on-empty — session mutations do not carry user_email; keep chunk-derived value.
		resolvedSessionUserEmail := mutationUserEmail
		if resolvedSessionUserEmail == "" {
			resolvedSessionUserEmail = existingSessionUserEmail
		}
		upsertDashboardSession(sessions, key, project, resolvedStartedAt, resolvedEndedAt, resolvedSummary, resolvedDirectory, resolvedSessionUserEmail)
	case store.SyncEntityObservation:
		var body dashboardObservationMutationPayload
		if err := chunkcodec.DecodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return err
		}
		key := resolveProjectValue(entityKey, body.SyncID)
		project := strings.TrimSpace(chunkProject)
		if body.Project != nil {
			project = resolveProjectValue(*body.Project, project)
		}
		if project == "" {
			if session, ok := sessions[newDashboardEntityKey(project, body.SessionID)]; ok {
				project = strings.TrimSpace(session.Project)
			}
		}
		if op == store.SyncOpDelete || body.Deleted || body.HardDelete {
			delete(observations, newDashboardEntityKey(project, key))
			return nil
		}
		// R2-6 + prior fixes: Preserve all scalar fields from a prior observations-array pass.
		// Mutations may carry only a subset of fields; any empty field inherits the stored value.
		existingChunkID := ""
		existingSessionID := ""
		existingType := ""
		existingTitle := ""
		existingContent := ""
		existingTopicKey := ""
		existingToolName := ""
		existingCreatedAt := ""
		existingUserEmail := ""
		if existing, ok := observations[newDashboardEntityKey(project, key)]; ok {
			existingChunkID = existing.ChunkID
			existingSessionID = existing.SessionID
			existingType = existing.Type
			existingTitle = existing.Title
			existingContent = existing.Content
			existingTopicKey = existing.TopicKey
			existingToolName = existing.ToolName
			existingCreatedAt = existing.CreatedAt
			existingUserEmail = existing.UserEmail
		}
		resolvedSessionID := body.SessionID
		if resolvedSessionID == "" {
			resolvedSessionID = existingSessionID
		}
		resolvedType := body.Type
		if resolvedType == "" {
			resolvedType = existingType
		}
		resolvedTitle := body.Title
		if resolvedTitle == "" {
			resolvedTitle = existingTitle
		}
		resolvedContent := body.Content
		if resolvedContent == "" {
			resolvedContent = existingContent
		}
		resolvedTopicKey := body.TopicKey
		if resolvedTopicKey == "" {
			resolvedTopicKey = existingTopicKey
		}
		resolvedToolName := body.ToolName
		if resolvedToolName == "" {
			resolvedToolName = existingToolName
		}
		resolvedCreatedAt := body.CreatedAt
		if resolvedCreatedAt == "" {
			resolvedCreatedAt = existingCreatedAt
		}
		// D2: cloud_mutations.user_email WINS over chunk-derived created_by (preserve-on-empty).
		// mutationUserEmail is the authoritative per-observation attribution from cloud_mutations.
		resolvedUserEmail := mutationUserEmail
		if resolvedUserEmail == "" {
			resolvedUserEmail = existingUserEmail
		}
		upsertDashboardObservation(observations, key, project, resolvedSessionID, resolvedType, resolvedTitle, resolvedContent, resolvedTopicKey, resolvedToolName, existingChunkID, resolvedCreatedAt, resolvedUserEmail)
	case store.SyncEntityPrompt:
		var body dashboardPromptMutationPayload
		if err := chunkcodec.DecodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return err
		}
		key := resolveProjectValue(entityKey, body.SyncID)
		project := strings.TrimSpace(chunkProject)
		if body.Project != nil {
			project = resolveProjectValue(*body.Project, project)
		}
		if project == "" {
			if session, ok := sessions[newDashboardEntityKey(project, body.SessionID)]; ok {
				project = strings.TrimSpace(session.Project)
			}
		}
		if op == store.SyncOpDelete || body.Deleted || body.HardDelete {
			delete(prompts, newDashboardEntityKey(project, key))
			return nil
		}
		// C2: Preserve ChunkID from a prior prompts-array pass — mutations do not
		// carry which chunk they originated from, so inherit the stored value.
		// R3-4: Preserve SessionID and CreatedAt when the mutation omits them (same
		// pattern as observations R2-6).
		existingPromptChunkID := ""
		existingPromptContent := ""
		existingPromptSessionID := ""
		existingPromptCreatedAt := ""
		existingPromptUserEmail := ""
		if existing, ok := prompts[newDashboardEntityKey(project, key)]; ok {
			existingPromptChunkID = existing.ChunkID
			existingPromptContent = existing.Content
			existingPromptSessionID = existing.SessionID
			existingPromptCreatedAt = existing.CreatedAt
			existingPromptUserEmail = existing.UserEmail
		}
		resolvedPromptContent := body.Content
		if resolvedPromptContent == "" {
			resolvedPromptContent = existingPromptContent
		}
		resolvedPromptSessionID := body.SessionID
		if resolvedPromptSessionID == "" {
			resolvedPromptSessionID = existingPromptSessionID
		}
		resolvedPromptCreatedAt := body.CreatedAt
		if resolvedPromptCreatedAt == "" {
			resolvedPromptCreatedAt = existingPromptCreatedAt
		}
		// D2: preserve-on-empty for prompt attribution.
		resolvedPromptUserEmail := mutationUserEmail
		if resolvedPromptUserEmail == "" {
			resolvedPromptUserEmail = existingPromptUserEmail
		}
		upsertDashboardPrompt(prompts, key, project, resolvedPromptSessionID, resolvedPromptContent, existingPromptChunkID, resolvedPromptCreatedAt, resolvedPromptUserEmail)
	}
	return nil
}

func (m dashboardReadModel) scoped(allowed map[string]struct{}) dashboardReadModel {
	// Empty map or wildcard sentinel "*" means no filtering.
	if len(allowed) == 0 {
		return m
	}
	if _, ok := allowed["*"]; ok {
		return m
	}
	projects := make([]DashboardProjectRow, 0, len(m.projects))
	projectDetails := make(map[string]DashboardProjectDetail)
	totalChunks := 0
	for _, row := range m.projects {
		if _, ok := allowed[strings.TrimSpace(row.Project)]; !ok {
			continue
		}
		projects = append(projects, row)
		totalChunks += row.Chunks
		if detail, exists := m.projectDetails[row.Project]; exists {
			projectDetails[row.Project] = detail
		}
	}

	type contributorAgg struct {
		chunks      int
		projects    map[string]struct{}
		lastChunkAt string
	}
	agg := make(map[string]*contributorAgg)
	for project, detail := range projectDetails {
		for _, contributor := range detail.Contributors {
			key := strings.TrimSpace(contributor.CreatedBy)
			if key == "" {
				continue
			}
			if _, ok := agg[key]; !ok {
				agg[key] = &contributorAgg{projects: make(map[string]struct{})}
			}
			agg[key].chunks += contributor.Chunks
			agg[key].projects[project] = struct{}{}
			if parseRFC3339(contributor.LastChunkAt).After(parseRFC3339(agg[key].lastChunkAt)) {
				agg[key].lastChunkAt = contributor.LastChunkAt
			}
		}
	}

	contributors := make([]DashboardContributorRow, 0, len(agg))
	for createdBy, row := range agg {
		contributors = append(contributors, DashboardContributorRow{
			CreatedBy:   createdBy,
			Chunks:      row.chunks,
			Projects:    len(row.projects),
			LastChunkAt: row.lastChunkAt,
		})
	}
	sort.Slice(contributors, func(i, j int) bool {
		if contributors[i].Chunks != contributors[j].Chunks {
			return contributors[i].Chunks > contributors[j].Chunks
		}
		return contributors[i].CreatedBy < contributors[j].CreatedBy
	})

	return dashboardReadModel{
		projects:       projects,
		contributors:   contributors,
		projectDetails: projectDetails,
		admin: DashboardAdminOverview{
			Projects:     len(projects),
			Contributors: len(contributors),
			Chunks:       totalChunks,
		},
	}
}

func parseRFC3339(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func (m dashboardReadModel) listContributors(query string) []DashboardContributorRow {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return append([]DashboardContributorRow(nil), m.contributors...)
	}
	filtered := make([]DashboardContributorRow, 0)
	for _, row := range m.contributors {
		if strings.Contains(strings.ToLower(row.CreatedBy), query) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func (m dashboardReadModel) filterObservations(project, query string) []DashboardObservationRow {
	query = strings.ToLower(strings.TrimSpace(query))
	project = strings.TrimSpace(project)
	rows := make([]DashboardObservationRow, 0)
	for _, detail := range m.projectDetails {
		if project != "" && detail.Project != project {
			continue
		}
		for _, row := range detail.Observations {
			if query != "" {
				haystack := strings.ToLower(row.Title + " " + row.Type)
				if !strings.Contains(haystack, query) {
					continue
				}
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt > rows[j].CreatedAt })
	return rows
}

func (m dashboardReadModel) filterSessions(project, query string) []DashboardSessionRow {
	query = strings.ToLower(strings.TrimSpace(query))
	project = strings.TrimSpace(project)
	rows := make([]DashboardSessionRow, 0)
	for _, detail := range m.projectDetails {
		if project != "" && detail.Project != project {
			continue
		}
		for _, row := range detail.Sessions {
			if query != "" && !strings.Contains(strings.ToLower(row.SessionID), query) {
				continue
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].StartedAt > rows[j].StartedAt })
	return rows
}

func (m dashboardReadModel) filterPrompts(project, query string) []DashboardPromptRow {
	query = strings.ToLower(strings.TrimSpace(query))
	project = strings.TrimSpace(project)
	rows := make([]DashboardPromptRow, 0)
	for _, detail := range m.projectDetails {
		if project != "" && detail.Project != project {
			continue
		}
		for _, row := range detail.Prompts {
			if query != "" && !strings.Contains(strings.ToLower(row.Content), query) {
				continue
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt > rows[j].CreatedAt })
	return rows
}

func (cs *CloudStore) loadDashboardReadModel() (dashboardReadModel, error) {
	if cs == nil {
		return dashboardReadModel{}, fmt.Errorf("cloudstore: not initialized")
	}

	cs.dashboardReadModelMu.RLock()
	if cs.dashboardReadModelOK {
		cached := cs.dashboardReadModel
		cs.dashboardReadModelMu.RUnlock()
		return cached, nil
	}
	cs.dashboardReadModelMu.RUnlock()

	var (
		model dashboardReadModel
		err   error
	)
	if cs.dashboardReadModelLoad != nil {
		model, err = cs.dashboardReadModelLoad()
	} else {
		model, err = cs.buildDashboardReadModel()
	}
	if err != nil {
		return dashboardReadModel{}, err
	}

	cs.dashboardReadModelMu.Lock()
	defer cs.dashboardReadModelMu.Unlock()
	if cs.dashboardReadModelOK {
		return cs.dashboardReadModel, nil
	}
	cs.dashboardReadModel = model
	cs.dashboardReadModelOK = true
	return cs.dashboardReadModel, nil
}

func (cs *CloudStore) buildDashboardReadModel() (dashboardReadModel, error) {
	chunks, err := cs.loadChunkRows("")
	if err != nil {
		return dashboardReadModel{}, err
	}
	mutations, err := cs.loadMutationRows("")
	if err != nil {
		return dashboardReadModel{}, err
	}
	versionRows, versionErr := cs.loadClientVersionRows()
	if versionErr != nil {
		// Non-fatal: proceed without version stamping so the dashboard still loads.
		versionRows = nil
	}
	model, err := buildDashboardReadModelFromRowsWithVersions(chunks, mutations, versionRows)
	if err != nil {
		return dashboardReadModel{}, err
	}
	return model.scoped(cs.dashboardAllowedScopes), nil
}

func (cs *CloudStore) ListProjects(query string) ([]DashboardProjectRow, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return model.projects, nil
	}
	filtered := make([]DashboardProjectRow, 0)
	for _, row := range model.projects {
		if strings.Contains(strings.ToLower(row.Project), query) {
			filtered = append(filtered, row)
		}
	}
	return filtered, nil
}

func (cs *CloudStore) ProjectDetail(project string) (DashboardProjectDetail, error) {
	normalized, err := cs.normalizeDashboardProject(project)
	if err != nil {
		return DashboardProjectDetail{}, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardProjectDetail{}, err
	}
	detail, ok := model.projectDetails[normalized]
	if !ok {
		return DashboardProjectDetail{}, fmt.Errorf("%w: %s", ErrDashboardProjectNotFound, normalized)
	}
	return detail, nil
}

func (cs *CloudStore) ListContributors(query string) ([]DashboardContributorRow, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, err
	}
	return model.listContributors(query), nil
}

func (cs *CloudStore) ListRecentSessions(project string, query string, limit int) ([]DashboardSessionRow, error) {
	normalizedProject, err := cs.normalizeDashboardProjectFilter(project)
	if err != nil {
		return nil, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, err
	}
	if normalizedProject != "" {
		if _, ok := model.projectDetails[normalizedProject]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrDashboardProjectNotFound, normalizedProject)
		}
	}
	rows := model.filterSessions(normalizedProject, query)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (cs *CloudStore) ListRecentObservations(project string, query string, limit int) ([]DashboardObservationRow, error) {
	normalizedProject, err := cs.normalizeDashboardProjectFilter(project)
	if err != nil {
		return nil, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, err
	}
	if normalizedProject != "" {
		if _, ok := model.projectDetails[normalizedProject]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrDashboardProjectNotFound, normalizedProject)
		}
	}
	rows := model.filterObservations(normalizedProject, query)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (cs *CloudStore) ListRecentPrompts(project string, query string, limit int) ([]DashboardPromptRow, error) {
	normalizedProject, err := cs.normalizeDashboardProjectFilter(project)
	if err != nil {
		return nil, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, err
	}
	if normalizedProject != "" {
		if _, ok := model.projectDetails[normalizedProject]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrDashboardProjectNotFound, normalizedProject)
		}
	}
	rows := model.filterPrompts(normalizedProject, query)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (cs *CloudStore) normalizeDashboardProject(project string) (string, error) {
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return "", fmt.Errorf("%w", ErrDashboardProjectInvalid)
	}
	if cs.dashboardAllowedAll {
		return project, nil
	}
	if len(cs.dashboardAllowedScopes) > 0 {
		if _, ok := cs.dashboardAllowedScopes[project]; !ok {
			return "", fmt.Errorf("%w", ErrDashboardProjectForbidden)
		}
	}
	return project, nil
}

func (cs *CloudStore) normalizeDashboardProjectFilter(project string) (string, error) {
	if strings.TrimSpace(project) == "" {
		return "", nil
	}
	return cs.normalizeDashboardProject(project)
}

func (cs *CloudStore) AdminOverview() (DashboardAdminOverview, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardAdminOverview{}, err
	}
	return model.admin, nil
}

// ScopedMemberOverview returns stats tailored to the caller's ReadScope.
//
//   - Admin scope → returns team-wide AdminOverview (Projects/Contributors/Chunks).
//   - Member scope → returns Observations/Sessions/Prompts counts scoped to the
//     caller's email across ALL projects. Contributors and Chunks remain zero
//     (chunk-level data is admin-only).
//   - Missing identity (non-admin, empty email) → ErrDashboardIdentityRequired.
func (cs *CloudStore) ScopedMemberOverview(scope *ReadScope) (DashboardAdminOverview, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardAdminOverview{}, err
	}
	// Admin: return team-wide overview unchanged.
	if scope == nil || scope.IsAdmin {
		return model.admin, nil
	}
	// Member: validate identity first.
	email := strings.ToLower(strings.TrimSpace(scope.Email))
	if email == "" {
		return DashboardAdminOverview{}, ErrDashboardIdentityRequired
	}
	// Count scoped obs/sessions/prompts across all project details.
	var obsCount, sessCount, promptCount int
	for _, detail := range model.projectDetails {
		for _, o := range detail.Observations {
			if strings.EqualFold(strings.TrimSpace(o.UserEmail), email) {
				obsCount++
			}
		}
		for _, s := range detail.Sessions {
			if strings.EqualFold(strings.TrimSpace(s.UserEmail), email) {
				sessCount++
			}
		}
		for _, p := range detail.Prompts {
			if strings.EqualFold(strings.TrimSpace(p.UserEmail), email) {
				promptCount++
			}
		}
	}
	return DashboardAdminOverview{
		Observations: obsCount,
		Sessions:     sessCount,
		Prompts:      promptCount,
		// Projects, Contributors, Chunks intentionally zero for member scope —
		// those are team-wide metrics reserved for admin views.
	}, nil
}

type dashboardChunkRow struct {
	chunkID   string
	project   string
	createdBy string
	createdAt time.Time
	parsed    engramsync.ChunkData
}

type dashboardMutationRow struct {
	seq        int64
	project    string
	entity     string
	entityKey  string
	op         string
	payload    []byte
	occurredAt time.Time
	userEmail  string // NEW (D2) — cloud_mutations.user_email; per-observation authoritative attribution
}

// clientVersionRow holds a row from cloud_client_versions for read-model stamping.
type clientVersionRow struct {
	contributor       string
	lastClientVersion string
}

func (cs *CloudStore) loadChunkRows(project string) ([]dashboardChunkRow, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	query := `SELECT chunk_id, project_name, created_by, created_at, payload FROM cloud_chunks`
	args := []any{}
	if project == "" && !cs.dashboardAllowedAll && len(cs.dashboardAllowedScopes) > 0 {
		allowed := make([]string, 0, len(cs.dashboardAllowedScopes))
		for name := range cs.dashboardAllowedScopes {
			allowed = append(allowed, name)
		}
		sort.Strings(allowed)
		query += ` WHERE project_name = ANY($1)`
		args = append(args, allowed)
	}
	if project != "" {
		if !cs.dashboardAllowedAll && len(cs.dashboardAllowedScopes) > 0 {
			if _, ok := cs.dashboardAllowedScopes[project]; !ok {
				return []dashboardChunkRow{}, nil
			}
		}
		if len(args) > 0 {
			return nil, fmt.Errorf("cloudstore: internal dashboard query invariant violated")
		}
		query += ` WHERE project_name = $1`
		args = append(args, project)
	}
	query += ` ORDER BY created_at DESC, chunk_id DESC`
	rows, err := cs.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: dashboard query chunks: %w", err)
	}
	defer rows.Close()

	result := make([]dashboardChunkRow, 0)
	for rows.Next() {
		var chunkID string
		var projectName string
		var createdBy string
		var createdAt time.Time
		var payload []byte
		if err := rows.Scan(&chunkID, &projectName, &createdBy, &createdAt, &payload); err != nil {
			return nil, fmt.Errorf("cloudstore: dashboard scan chunk row: %w", err)
		}
		parsed := engramsync.ChunkData{}
		if err := json.Unmarshal(payload, &parsed); err != nil {
			return nil, fmt.Errorf("cloudstore: dashboard decode chunk payload for chunk %q: %w", strings.TrimSpace(chunkID), err)
		}
		result = append(result, dashboardChunkRow{chunkID: strings.TrimSpace(chunkID), project: projectName, createdBy: strings.TrimSpace(createdBy), createdAt: createdAt.UTC(), parsed: parsed})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: dashboard iterate chunks: %w", err)
	}
	return result, nil
}

func (cs *CloudStore) loadMutationRows(project string) ([]dashboardMutationRow, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	query := `SELECT seq, project, entity, entity_key, op, payload::text, occurred_at, COALESCE(user_email,'') FROM cloud_mutations`
	args := []any{}
	if project == "" && !cs.dashboardAllowedAll && len(cs.dashboardAllowedScopes) > 0 {
		allowed := make([]string, 0, len(cs.dashboardAllowedScopes))
		for name := range cs.dashboardAllowedScopes {
			allowed = append(allowed, name)
		}
		sort.Strings(allowed)
		query += ` WHERE project = ANY($1)`
		args = append(args, allowed)
	}
	if project != "" {
		if !cs.dashboardAllowedAll && len(cs.dashboardAllowedScopes) > 0 {
			if _, ok := cs.dashboardAllowedScopes[project]; !ok {
				return []dashboardMutationRow{}, nil
			}
		}
		if len(args) > 0 {
			return nil, fmt.Errorf("cloudstore: internal dashboard mutation query invariant violated")
		}
		query += ` WHERE project = $1`
		args = append(args, project)
	}
	query += ` ORDER BY seq ASC, occurred_at ASC`
	rows, err := cs.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: dashboard query mutations: %w", err)
	}
	defer rows.Close()

	result := make([]dashboardMutationRow, 0)
	for rows.Next() {
		var row dashboardMutationRow
		var payloadText string
		if err := rows.Scan(&row.seq, &row.project, &row.entity, &row.entityKey, &row.op, &payloadText, &row.occurredAt, &row.userEmail); err != nil {
			return nil, fmt.Errorf("cloudstore: dashboard scan mutation row: %w", err)
		}
		row.project = strings.TrimSpace(row.project)
		row.entity = strings.TrimSpace(row.entity)
		row.entityKey = strings.TrimSpace(row.entityKey)
		row.op = strings.TrimSpace(row.op)
		row.payload = []byte(strings.TrimSpace(payloadText))
		row.occurredAt = row.occurredAt.UTC()
		if len(row.payload) == 0 {
			row.payload = []byte(`{}`)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: dashboard iterate mutations: %w", err)
	}
	return result, nil
}

// loadClientVersionRows returns all rows from cloud_client_versions for read-model stamping.
// Returns an empty slice (no error) when the table exists but is empty, or when
// the table does not yet exist (pre-migration, SQLSTATE 42P01). All other errors
// are propagated so the caller's versionErr handling is live.
// Satisfies: FIX #7 — stop swallowing non-table-missing DB errors.
func (cs *CloudStore) loadClientVersionRows() ([]clientVersionRow, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	rows, err := cs.db.QueryContext(context.Background(), `
		SELECT contributor, last_client_version
		FROM cloud_client_versions`)
	if err != nil {
		// Only ignore "undefined table" (42P01): table not yet created on a
		// pre-migration instance. All other errors (permissions, network, syntax,
		// etc.) are real failures and must propagate to the caller.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return []clientVersionRow{}, nil
		}
		return nil, fmt.Errorf("cloudstore: query client version rows: %w", err)
	}
	defer rows.Close()

	var result []clientVersionRow
	for rows.Next() {
		var r clientVersionRow
		if err := rows.Scan(&r.contributor, &r.lastClientVersion); err != nil {
			return nil, fmt.Errorf("cloudstore: scan client version row: %w", err)
		}
		r.contributor = strings.TrimSpace(r.contributor)
		r.lastClientVersion = strings.TrimSpace(r.lastClientVersion)
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate client version rows: %w", err)
	}
	return result, nil
}

// buildDashboardReadModelFromRowsWithVersions is like buildDashboardReadModelFromRows
// but additionally stamps LastClientVersion onto contributor rows from versionRows.
// Satisfies: REQ-CVR-07, REQ-CVR-08.
func buildDashboardReadModelFromRowsWithVersions(chunks []dashboardChunkRow, mutationRows []dashboardMutationRow, versionRows []clientVersionRow) (dashboardReadModel, error) {
	model, err := buildDashboardReadModelFromRows(chunks, mutationRows)
	if err != nil {
		return dashboardReadModel{}, err
	}

	if len(versionRows) == 0 {
		return model, nil
	}

	// Build a lookup map: contributor → last_client_version.
	versionByContributor := make(map[string]string, len(versionRows))
	for _, r := range versionRows {
		if r.contributor != "" {
			versionByContributor[r.contributor] = r.lastClientVersion
		}
	}

	// Stamp top-level contributor rows.
	for i, row := range model.contributors {
		if v, ok := versionByContributor[row.CreatedBy]; ok {
			model.contributors[i].LastClientVersion = v
		}
	}

	// Stamp per-project contributor rows inside projectDetails.
	for project, detail := range model.projectDetails {
		for i, row := range detail.Contributors {
			if v, ok := versionByContributor[row.CreatedBy]; ok {
				detail.Contributors[i].LastClientVersion = v
			}
		}
		model.projectDetails[project] = detail
	}

	return model, nil
}

// ─── SystemHealth ─────────────────────────────────────────────────────────────

// SystemHealth returns aggregate metrics from the in-memory read model plus a DB ping.
// Satisfies REQ-105 / AD-3.
func (cs *CloudStore) SystemHealth() (DashboardSystemHealth, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardSystemHealth{}, err
	}

	totalSessions := 0
	totalObservations := 0
	totalPrompts := 0
	for _, detail := range model.projectDetails {
		totalSessions += len(detail.Sessions)
		totalObservations += len(detail.Observations)
		totalPrompts += len(detail.Prompts)
	}

	// Ping DB with a short timeout.
	dbConnected := false
	if cs.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := cs.db.PingContext(ctx); err == nil {
			dbConnected = true
		}
	}

	return DashboardSystemHealth{
		DBConnected:  dbConnected,
		Projects:     len(model.projects),
		Contributors: len(model.contributors),
		Sessions:     totalSessions,
		Observations: totalObservations,
		Prompts:      totalPrompts,
		Chunks:       model.admin.Chunks,
	}, nil
}

// ─── Batch 6: Connected Navigation Methods ───────────────────────────────────

// GetContributorDetail returns the contributor row plus all sessions, observations,
// and prompts that belong to projects the contributor has created chunks in.
// Purely in-memory scan of dashboardReadModel. Satisfies (h).
func (cs *CloudStore) GetContributorDetail(name string) (DashboardContributorRow, []DashboardSessionRow, []DashboardObservationRow, []DashboardPromptRow, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardContributorRow{}, nil, nil, nil, err
	}

	name = strings.TrimSpace(name)
	var contributor DashboardContributorRow
	found := false
	for _, c := range model.contributors {
		if strings.EqualFold(strings.TrimSpace(c.CreatedBy), name) {
			contributor = c
			found = true
			break
		}
	}
	if !found {
		// R4-7: Return ErrDashboardContributorNotFound (not ErrDashboardProjectNotFound) so the
		// HTTP handler can produce "Contributor not found" instead of "Project not found".
		return DashboardContributorRow{}, nil, nil, nil, fmt.Errorf("%w: contributor %s", ErrDashboardContributorNotFound, name)
	}

	// Find all projects that this contributor has contributed chunks to.
	contributorProjects := make(map[string]struct{})
	for project, detail := range model.projectDetails {
		for _, c := range detail.Contributors {
			if strings.EqualFold(strings.TrimSpace(c.CreatedBy), name) {
				contributorProjects[project] = struct{}{}
				break
			}
		}
	}

	var sessions []DashboardSessionRow
	var observations []DashboardObservationRow
	var prompts []DashboardPromptRow
	for project := range contributorProjects {
		detail, ok := model.projectDetails[project]
		if !ok {
			continue
		}
		// Scope to THIS contributor's own rows. A project can be shared by several
		// contributors, so appending the whole project's rows would leak other members'
		// sessions/observations/prompts (mislabeled) into this contributor's detail view.
		// Attribution is per-row via UserEmail (derived from the chunk's created_by).
		for _, s := range detail.Sessions {
			if strings.EqualFold(strings.TrimSpace(s.UserEmail), name) {
				sessions = append(sessions, s)
			}
		}
		for _, o := range detail.Observations {
			if strings.EqualFold(strings.TrimSpace(o.UserEmail), name) {
				observations = append(observations, o)
			}
		}
		for _, pr := range detail.Prompts {
			if strings.EqualFold(strings.TrimSpace(pr.UserEmail), name) {
				prompts = append(prompts, pr)
			}
		}
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].StartedAt > sessions[j].StartedAt })
	sort.Slice(observations, func(i, j int) bool { return observations[i].CreatedAt > observations[j].CreatedAt })
	sort.Slice(prompts, func(i, j int) bool { return prompts[i].CreatedAt > prompts[j].CreatedAt })

	return contributor, sessions, observations, prompts, nil
}

// ListDistinctTypes returns a sorted list of distinct, non-empty observation types
// from the in-memory read model. Satisfies (m).
func (cs *CloudStore) ListDistinctTypes() ([]string, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	for _, detail := range model.projectDetails {
		for _, obs := range detail.Observations {
			t := strings.TrimSpace(obs.Type)
			if t != "" {
				seen[t] = struct{}{}
			}
		}
	}

	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	sort.Strings(types)
	return types, nil
}

// ─── Paginated List Methods (in-memory slicing) ───────────────────────────────

// ListProjectsPaginated returns a page of projects filtered by query.
// Satisfies Design Decision 1 (in-memory slicing, no SQL LIMIT/OFFSET).
func (cs *CloudStore) ListProjectsPaginated(query string, limit, offset int) ([]DashboardProjectRow, int, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, 0, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	filtered := make([]DashboardProjectRow, 0, len(model.projects))
	for _, row := range model.projects {
		if query == "" || strings.Contains(strings.ToLower(row.Project), query) {
			filtered = append(filtered, row)
		}
	}
	total := len(filtered)
	if offset > total {
		return []DashboardProjectRow{}, total, nil
	}
	end := offset + limit
	if end > total || limit <= 0 {
		end = total
	}
	return filtered[offset:end], total, nil
}

// ListRecentObservationsPaginated returns a page of observations filtered by the caller's ReadScope.
// scope must not be nil for non-admin callers; missing identity (empty email, non-admin) returns
// ErrDashboardIdentityRequired so the handler can render a 403 deny — never an unscoped result.
func (cs *CloudStore) ListRecentObservationsPaginated(scope *ReadScope, project, query, obsType string, limit, offset int) ([]DashboardObservationRow, int, error) {
	normalizedProject, err := cs.normalizeDashboardProjectFilter(project)
	if err != nil {
		return nil, 0, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, 0, err
	}
	rows := model.filterObservations(normalizedProject, query)
	// Apply type filter.
	obsType = strings.TrimSpace(obsType)
	if obsType != "" {
		filtered := make([]DashboardObservationRow, 0)
		for _, r := range rows {
			if strings.EqualFold(r.Type, obsType) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	// D2: apply per-request read scope BEFORE pagination (cache stays unscoped).
	rows, err = applyReadScope(scope, rows, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		return nil, 0, err
	}
	total := len(rows)
	if offset > total {
		return []DashboardObservationRow{}, total, nil
	}
	end := offset + limit
	if end > total || limit <= 0 {
		end = total
	}
	return rows[offset:end], total, nil
}

// ListRecentSessionsPaginated returns a page of sessions filtered by the caller's ReadScope.
func (cs *CloudStore) ListRecentSessionsPaginated(scope *ReadScope, project, query string, limit, offset int) ([]DashboardSessionRow, int, error) {
	normalizedProject, err := cs.normalizeDashboardProjectFilter(project)
	if err != nil {
		return nil, 0, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, 0, err
	}
	rows := model.filterSessions(normalizedProject, query)
	// D2: apply per-request read scope BEFORE pagination.
	rows, err = applyReadScope(scope, rows, func(r DashboardSessionRow) string { return r.UserEmail })
	if err != nil {
		return nil, 0, err
	}
	total := len(rows)
	if offset > total {
		return []DashboardSessionRow{}, total, nil
	}
	end := offset + limit
	if end > total || limit <= 0 {
		end = total
	}
	return rows[offset:end], total, nil
}

// ListRecentPromptsPaginated returns a page of prompts filtered by the caller's ReadScope.
func (cs *CloudStore) ListRecentPromptsPaginated(scope *ReadScope, project, query string, limit, offset int) ([]DashboardPromptRow, int, error) {
	normalizedProject, err := cs.normalizeDashboardProjectFilter(project)
	if err != nil {
		return nil, 0, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, 0, err
	}
	rows := model.filterPrompts(normalizedProject, query)
	// D2: apply per-request read scope BEFORE pagination.
	rows, err = applyReadScope(scope, rows, func(r DashboardPromptRow) string { return r.UserEmail })
	if err != nil {
		return nil, 0, err
	}
	total := len(rows)
	if offset > total {
		return []DashboardPromptRow{}, total, nil
	}
	end := offset + limit
	if end > total || limit <= 0 {
		end = total
	}
	return rows[offset:end], total, nil
}

// ListContributorsPaginated returns a page of contributors. Contributors are admin-only data
// and not scoped per-member — the caller must gate on IsAdmin before invoking this.
// The scope parameter is accepted for interface consistency but contributors are not filtered
// by UserEmail (they represent chunk upload identity, always team-wide for admin use).
func (cs *CloudStore) ListContributorsPaginated(scope *ReadScope, query string, limit, offset int) ([]DashboardContributorRow, int, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return nil, 0, err
	}
	rows := model.listContributors(query)
	total := len(rows)
	if offset > total {
		return []DashboardContributorRow{}, total, nil
	}
	end := offset + limit
	if end > total || limit <= 0 {
		end = total
	}
	return rows[offset:end], total, nil
}

// ─── Detail Query Methods ─────────────────────────────────────────────────────

// GetSessionDetail returns session detail with its observations and prompts.
// scope controls whether the caller can see this session (admin → all, member → own only).
func (cs *CloudStore) GetSessionDetail(scope *ReadScope, project, sessionID string) (DashboardSessionRow, []DashboardObservationRow, []DashboardPromptRow, error) {
	normalizedProject, err := cs.normalizeDashboardProject(project)
	if err != nil {
		return DashboardSessionRow{}, nil, nil, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardSessionRow{}, nil, nil, err
	}
	detail, ok := model.projectDetails[normalizedProject]
	if !ok {
		return DashboardSessionRow{}, nil, nil, fmt.Errorf("%w: %s", ErrDashboardProjectNotFound, normalizedProject)
	}
	sessionID = strings.TrimSpace(sessionID)
	var sess DashboardSessionRow
	found := false
	for _, s := range detail.Sessions {
		if s.SessionID == sessionID {
			sess = s
			found = true
			break
		}
	}
	if !found {
		// R5-4: return ErrDashboardSessionNotFound (not ErrDashboardProjectNotFound) when
		// the project exists but the specific session is missing.
		return DashboardSessionRow{}, nil, nil, fmt.Errorf("%w: session %s in project %s", ErrDashboardSessionNotFound, sessionID, normalizedProject)
	}
	// D2: scope check on the found session — member cannot see another user's session.
	// Return ErrDashboardSessionNotFound (not 403) so membership in a session is not leaked.
	scopedSessions, scopeErr := applyReadScope(scope, []DashboardSessionRow{sess}, func(r DashboardSessionRow) string { return r.UserEmail })
	if scopeErr != nil || len(scopedSessions) == 0 {
		return DashboardSessionRow{}, nil, nil, fmt.Errorf("%w: session %s in project %s", ErrDashboardSessionNotFound, sessionID, normalizedProject)
	}
	// Collect observations and prompts for this session.
	var obs []DashboardObservationRow
	for _, o := range detail.Observations {
		if o.SessionID == sessionID {
			obs = append(obs, o)
		}
	}
	// D2: re-apply scope to related observations so a member never sees another
	// user's observations in the session sidebar (Warning 4).
	obs, err = applyReadScope(scope, obs, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		// ErrDashboardIdentityRequired on related items: return an empty slice rather
		// than an error — the session header already passed scope; only related items
		// are filtered out. Callers that lack identity were already denied above.
		obs = nil
	}
	var prompts []DashboardPromptRow
	for _, p := range detail.Prompts {
		if p.SessionID == sessionID {
			prompts = append(prompts, p)
		}
	}
	// D2: re-apply scope to related prompts (Warning 4).
	prompts, err = applyReadScope(scope, prompts, func(r DashboardPromptRow) string { return r.UserEmail })
	if err != nil {
		prompts = nil
	}
	return sess, obs, prompts, nil
}

// GetObservationDetail returns an observation with its parent session and related observations.
// The third parameter is syncID — the unique per-observation identifier (map key).
// scope controls visibility: member callers see only their own observation; missing identity → deny.
// Returns ErrDashboardObservationNotFound (not 403) so existence is not leaked to member callers.
func (cs *CloudStore) GetObservationDetail(scope *ReadScope, project, sessionID, syncID string) (DashboardObservationRow, DashboardSessionRow, []DashboardObservationRow, error) {
	normalizedProject, err := cs.normalizeDashboardProject(project)
	if err != nil {
		return DashboardObservationRow{}, DashboardSessionRow{}, nil, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardObservationRow{}, DashboardSessionRow{}, nil, err
	}
	detail, ok := model.projectDetails[normalizedProject]
	if !ok {
		return DashboardObservationRow{}, DashboardSessionRow{}, nil, fmt.Errorf("%w: %s", ErrDashboardProjectNotFound, normalizedProject)
	}
	syncID = strings.TrimSpace(syncID)
	sessionID = strings.TrimSpace(sessionID)
	var obs DashboardObservationRow
	found := false
	for _, o := range detail.Observations {
		if o.SyncID == syncID && o.SessionID == sessionID {
			obs = o
			found = true
			break
		}
	}
	if !found {
		// R5-4: return ErrDashboardObservationNotFound when the project exists but the observation is missing.
		return DashboardObservationRow{}, DashboardSessionRow{}, nil, fmt.Errorf("%w: observation %s/%s in project %s", ErrDashboardObservationNotFound, sessionID, syncID, normalizedProject)
	}
	// D2: scope check — member cannot see another user's observation.
	// Return ErrDashboardObservationNotFound (not 403) so existence is not leaked.
	scopedObs, scopeErr := applyReadScope(scope, []DashboardObservationRow{obs}, func(r DashboardObservationRow) string { return r.UserEmail })
	if scopeErr != nil || len(scopedObs) == 0 {
		return DashboardObservationRow{}, DashboardSessionRow{}, nil, fmt.Errorf("%w: observation %s/%s in project %s", ErrDashboardObservationNotFound, sessionID, syncID, normalizedProject)
	}
	var sess DashboardSessionRow
	for _, s := range detail.Sessions {
		if s.SessionID == sessionID {
			sess = s
			break
		}
	}
	var related []DashboardObservationRow
	for _, o := range detail.Observations {
		if o.SessionID == sessionID && o.SyncID != syncID {
			related = append(related, o)
		}
	}
	// D2: re-apply scope to related observations so a member never sees another
	// user's sibling observations in the detail sidebar (Warning 4).
	related, err = applyReadScope(scope, related, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		related = nil
	}
	return obs, sess, related, nil
}

// GetObservationBySyncID looks up a single observation by its syncID alone,
// without requiring a project or sessionID. This is used by the deletion-request
// submission path (handleRequestRemoval) where the caller only has the syncID
// extracted from the URL path.
//
// scope controls visibility:
//   - admin (IsAdmin=true)       → searches all observations across all projects
//   - member (IsAdmin=false)     → searches, but returns ErrDashboardObservationNotFound
//     unless obs.UserEmail == scope.Email (ownership check, case-insensitive)
//   - missing identity (!IsAdmin && Email=="") → returns ErrDashboardIdentityRequired
//
// Returns ErrDashboardObservationNotFound when the syncID is not in the read model
// (either because it was deleted, never existed, or the member doesn't own it).
func (cs *CloudStore) GetObservationBySyncID(scope *ReadScope, syncID string) (DashboardObservationRow, error) {
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardObservationRow{}, err
	}
	syncID = strings.TrimSpace(syncID)

	// Validate member identity before scanning so we never leak existence.
	if scope != nil && !scope.IsAdmin {
		email := strings.ToLower(strings.TrimSpace(scope.Email))
		if email == "" {
			return DashboardObservationRow{}, ErrDashboardIdentityRequired
		}
	}

	// Linear scan across all project details — the in-memory model is small.
	for _, detail := range model.projectDetails {
		for _, obs := range detail.Observations {
			if obs.SyncID != syncID {
				continue
			}
			// Found. Apply scope check.
			if scope != nil && !scope.IsAdmin {
				callerEmail := strings.ToLower(strings.TrimSpace(scope.Email))
				if !strings.EqualFold(strings.TrimSpace(obs.UserEmail), callerEmail) {
					// Member does not own this observation — return not-found to avoid leaking existence.
					return DashboardObservationRow{}, fmt.Errorf("%w: %s", ErrDashboardObservationNotFound, syncID)
				}
			}
			return obs, nil
		}
	}
	return DashboardObservationRow{}, fmt.Errorf("%w: %s", ErrDashboardObservationNotFound, syncID)
}

// GetPromptDetail returns a prompt with its parent session and related prompts.
// The third parameter is syncID — the unique per-prompt identifier (map key).
// scope controls visibility: member callers see only their own prompts; missing identity → deny.
func (cs *CloudStore) GetPromptDetail(scope *ReadScope, project, sessionID, syncID string) (DashboardPromptRow, DashboardSessionRow, []DashboardPromptRow, error) {
	normalizedProject, err := cs.normalizeDashboardProject(project)
	if err != nil {
		return DashboardPromptRow{}, DashboardSessionRow{}, nil, err
	}
	model, err := cs.loadDashboardReadModel()
	if err != nil {
		return DashboardPromptRow{}, DashboardSessionRow{}, nil, err
	}
	detail, ok := model.projectDetails[normalizedProject]
	if !ok {
		return DashboardPromptRow{}, DashboardSessionRow{}, nil, fmt.Errorf("%w: %s", ErrDashboardProjectNotFound, normalizedProject)
	}
	syncID = strings.TrimSpace(syncID)
	sessionID = strings.TrimSpace(sessionID)
	var prompt DashboardPromptRow
	found := false
	for _, p := range detail.Prompts {
		if p.SyncID == syncID && p.SessionID == sessionID {
			prompt = p
			found = true
			break
		}
	}
	if !found {
		// R5-4: return ErrDashboardPromptNotFound when the project exists but the prompt is missing.
		return DashboardPromptRow{}, DashboardSessionRow{}, nil, fmt.Errorf("%w: prompt %s/%s in project %s", ErrDashboardPromptNotFound, sessionID, syncID, normalizedProject)
	}
	// D2: scope check — member cannot see another user's prompt.
	// Return ErrDashboardPromptNotFound (not 403) so existence is not leaked.
	scopedPrompts, scopeErr := applyReadScope(scope, []DashboardPromptRow{prompt}, func(r DashboardPromptRow) string { return r.UserEmail })
	if scopeErr != nil || len(scopedPrompts) == 0 {
		return DashboardPromptRow{}, DashboardSessionRow{}, nil, fmt.Errorf("%w: prompt %s/%s in project %s", ErrDashboardPromptNotFound, sessionID, syncID, normalizedProject)
	}
	var sess DashboardSessionRow
	for _, s := range detail.Sessions {
		if s.SessionID == sessionID {
			sess = s
			break
		}
	}
	var related []DashboardPromptRow
	for _, p := range detail.Prompts {
		if p.SessionID == sessionID && p.SyncID != syncID {
			related = append(related, p)
		}
	}
	// D2: re-apply scope to related prompts so a member never sees another
	// user's sibling prompts in the detail sidebar (Warning 4).
	related, err = applyReadScope(scope, related, func(r DashboardPromptRow) string { return r.UserEmail })
	if err != nil {
		related = nil
	}
	return prompt, sess, related, nil
}
