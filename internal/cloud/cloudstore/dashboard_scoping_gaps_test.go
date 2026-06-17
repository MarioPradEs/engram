package cloudstore

// RED tests for verify-reported scoping gaps:
//   - Gap 3: detail-sidebar related items not re-filtered by scope (GetSessionDetail,
//     GetObservationDetail, GetPromptDetail)
//   - Gap 2: ScopedMemberOverview (member stats)
//
// These tests FAIL before the implementation is added and PASS after.
// Each test is structured so removing scope enforcement causes a failure.

import (
	"errors"
	"testing"
)

// newInjectableStore returns a *CloudStore backed by a fixed dashboardReadModel
// loaded via dashboardReadModelLoad, so no real DB is required.
func newInjectableStore(model dashboardReadModel) *CloudStore {
	cs := &CloudStore{
		dashboardAllowedAll: true,
		dashboardReadModelLoad: func() (dashboardReadModel, error) {
			return model, nil
		},
	}
	return cs
}

// ─── Gap 3: detail sidebar scoping ──────────────────────────────────────────

// TestGetSessionDetail_RelatedObsExcludeOtherUsersForMember verifies that when
// a member calls GetSessionDetail, the related observations list contains ONLY
// observations attributed to that member, not observations from other users in
// the same session.
//
// FAILS without fix: related obs returned unfiltered → bob's obs appears.
// PASSES after fix: applyReadScope applied to related obs/prompts.
func TestGetSessionDetail_RelatedObsExcludeOtherUsersForMember(t *testing.T) {
	model := dashboardReadModel{
		projectDetails: map[string]DashboardProjectDetail{
			"proj-a": {
				Project: "proj-a",
				Stats:   DashboardProjectRow{Project: "proj-a"},
				Sessions: []DashboardSessionRow{
					{Project: "proj-a", SessionID: "sess-1", UserEmail: "alice@example.com"},
				},
				Observations: []DashboardObservationRow{
					{Project: "proj-a", SessionID: "sess-1", SyncID: "obs-alice-1", UserEmail: "alice@example.com"},
					{Project: "proj-a", SessionID: "sess-1", SyncID: "obs-bob-1", UserEmail: "bob@example.com"},
				},
				Prompts: []DashboardPromptRow{
					{Project: "proj-a", SessionID: "sess-1", SyncID: "prompt-alice-1", UserEmail: "alice@example.com"},
					{Project: "proj-a", SessionID: "sess-1", SyncID: "prompt-bob-1", UserEmail: "bob@example.com"},
				},
			},
		},
	}
	cs := newInjectableStore(model)
	scope := &ReadScope{Email: "alice@example.com", IsAdmin: false}
	_, obs, prompts, err := cs.GetSessionDetail(scope, "proj-a", "sess-1")
	if err != nil {
		t.Fatalf("GetSessionDetail: unexpected error: %v", err)
	}
	// alice's own obs must be present.
	if len(obs) == 0 {
		t.Error("related obs: expected alice's own obs to be returned, got none")
	}
	// bob's obs must NOT appear.
	for _, o := range obs {
		if o.UserEmail != "alice@example.com" {
			t.Errorf("related obs leak: member alice received obs from %q (SyncID=%q) — scope not enforced on related items", o.UserEmail, o.SyncID)
		}
	}
	// alice's own prompt must be present.
	if len(prompts) == 0 {
		t.Error("related prompts: expected alice's own prompts to be returned, got none")
	}
	// bob's prompt must NOT appear.
	for _, p := range prompts {
		if p.UserEmail != "alice@example.com" {
			t.Errorf("related prompts leak: member alice received prompt from %q (SyncID=%q) — scope not enforced on related items", p.UserEmail, p.SyncID)
		}
	}
}

// TestGetObservationDetail_RelatedObsExcludeOtherUsersForMember verifies that when
// a member calls GetObservationDetail, the related observations sidebar excludes
// observations from other users in the same session.
//
// FAILS without fix: obs-bob-1 appears in related.
// PASSES after fix: applyReadScope applied to related slice.
func TestGetObservationDetail_RelatedObsExcludeOtherUsersForMember(t *testing.T) {
	model := dashboardReadModel{
		projectDetails: map[string]DashboardProjectDetail{
			"proj-a": {
				Project: "proj-a",
				Stats:   DashboardProjectRow{Project: "proj-a"},
				Sessions: []DashboardSessionRow{
					{Project: "proj-a", SessionID: "sess-1", UserEmail: "alice@example.com"},
				},
				Observations: []DashboardObservationRow{
					// obs-alice-1 is the requested observation.
					{Project: "proj-a", SessionID: "sess-1", SyncID: "obs-alice-1", UserEmail: "alice@example.com"},
					// obs-alice-2 is a sibling owned by alice — must appear in related.
					{Project: "proj-a", SessionID: "sess-1", SyncID: "obs-alice-2", UserEmail: "alice@example.com"},
					// obs-bob-1 is a sibling owned by bob — must NOT appear in related.
					{Project: "proj-a", SessionID: "sess-1", SyncID: "obs-bob-1", UserEmail: "bob@example.com"},
				},
			},
		},
	}
	cs := newInjectableStore(model)
	scope := &ReadScope{Email: "alice@example.com", IsAdmin: false}
	_, _, related, err := cs.GetObservationDetail(scope, "proj-a", "sess-1", "obs-alice-1")
	if err != nil {
		t.Fatalf("GetObservationDetail: unexpected error: %v", err)
	}
	// obs-alice-2 must appear in related.
	foundAlice2 := false
	for _, o := range related {
		if o.SyncID == "obs-alice-2" {
			foundAlice2 = true
		}
		if o.UserEmail != "alice@example.com" {
			t.Errorf("related obs leak: member alice received related obs from %q (SyncID=%q) — scope not enforced", o.UserEmail, o.SyncID)
		}
	}
	if !foundAlice2 {
		t.Errorf("expected obs-alice-2 in related, but it was missing")
	}
}

// TestGetPromptDetail_RelatedPromptsExcludeOtherUsersForMember verifies that when
// a member calls GetPromptDetail, the related prompts sidebar excludes prompts
// from other users in the same session.
//
// FAILS without fix: prompt-bob-1 appears in related.
// PASSES after fix: applyReadScope applied to related prompts slice.
func TestGetPromptDetail_RelatedPromptsExcludeOtherUsersForMember(t *testing.T) {
	model := dashboardReadModel{
		projectDetails: map[string]DashboardProjectDetail{
			"proj-a": {
				Project: "proj-a",
				Stats:   DashboardProjectRow{Project: "proj-a"},
				Sessions: []DashboardSessionRow{
					{Project: "proj-a", SessionID: "sess-1", UserEmail: "alice@example.com"},
				},
				Prompts: []DashboardPromptRow{
					// prompt-alice-1 is the requested prompt.
					{Project: "proj-a", SessionID: "sess-1", SyncID: "prompt-alice-1", UserEmail: "alice@example.com"},
					// prompt-alice-2 is a sibling owned by alice — must appear in related.
					{Project: "proj-a", SessionID: "sess-1", SyncID: "prompt-alice-2", UserEmail: "alice@example.com"},
					// prompt-bob-1 must NOT appear in related.
					{Project: "proj-a", SessionID: "sess-1", SyncID: "prompt-bob-1", UserEmail: "bob@example.com"},
				},
				Observations: []DashboardObservationRow{},
			},
		},
	}
	cs := newInjectableStore(model)
	scope := &ReadScope{Email: "alice@example.com", IsAdmin: false}
	_, _, related, err := cs.GetPromptDetail(scope, "proj-a", "sess-1", "prompt-alice-1")
	if err != nil {
		t.Fatalf("GetPromptDetail: unexpected error: %v", err)
	}
	foundAlice2 := false
	for _, p := range related {
		if p.SyncID == "prompt-alice-2" {
			foundAlice2 = true
		}
		if p.UserEmail != "alice@example.com" {
			t.Errorf("related prompts leak: member alice received prompt from %q (SyncID=%q) — scope not enforced", p.UserEmail, p.SyncID)
		}
	}
	if !foundAlice2 {
		t.Errorf("expected prompt-alice-2 in related, but it was missing")
	}
}

// TestGetSessionDetail_AdminSeesAllRelatedItems verifies that an admin scope
// returns ALL related obs/prompts (no over-filtering).
func TestGetSessionDetail_AdminSeesAllRelatedItems(t *testing.T) {
	model := dashboardReadModel{
		projectDetails: map[string]DashboardProjectDetail{
			"proj-a": {
				Project: "proj-a",
				Stats:   DashboardProjectRow{Project: "proj-a"},
				Sessions: []DashboardSessionRow{
					{Project: "proj-a", SessionID: "sess-1", UserEmail: "alice@example.com"},
				},
				Observations: []DashboardObservationRow{
					{Project: "proj-a", SessionID: "sess-1", SyncID: "obs-alice-1", UserEmail: "alice@example.com"},
					{Project: "proj-a", SessionID: "sess-1", SyncID: "obs-bob-1", UserEmail: "bob@example.com"},
				},
				Prompts: []DashboardPromptRow{
					{Project: "proj-a", SessionID: "sess-1", SyncID: "prompt-alice-1", UserEmail: "alice@example.com"},
					{Project: "proj-a", SessionID: "sess-1", SyncID: "prompt-bob-1", UserEmail: "bob@example.com"},
				},
			},
		},
	}
	cs := newInjectableStore(model)
	scope := &ReadScope{Email: "admin@example.com", IsAdmin: true}
	_, obs, prompts, err := cs.GetSessionDetail(scope, "proj-a", "sess-1")
	if err != nil {
		t.Fatalf("GetSessionDetail (admin): unexpected error: %v", err)
	}
	if len(obs) != 2 {
		t.Errorf("admin: expected 2 related obs, got %d", len(obs))
	}
	if len(prompts) != 2 {
		t.Errorf("admin: expected 2 related prompts, got %d", len(prompts))
	}
}

// ─── Gap 2: ScopedMemberOverview ────────────────────────────────────────────

// TestScopedMemberOverview_MemberSeesOnlyOwnCounts verifies that a member's stats
// reflect only their own observations, sessions, and prompts — not team-wide totals.
//
// FAILS if ScopedMemberOverview doesn't exist or doesn't apply scope.
func TestScopedMemberOverview_MemberSeesOnlyOwnCounts(t *testing.T) {
	model := dashboardReadModel{
		projectDetails: map[string]DashboardProjectDetail{
			"proj-a": {
				Project: "proj-a",
				Observations: []DashboardObservationRow{
					{Project: "proj-a", SyncID: "obs-alice-1", UserEmail: "alice@example.com"},
					{Project: "proj-a", SyncID: "obs-bob-1", UserEmail: "bob@example.com"},
				},
				Sessions: []DashboardSessionRow{
					{Project: "proj-a", SessionID: "sess-alice-1", UserEmail: "alice@example.com"},
					{Project: "proj-a", SessionID: "sess-bob-1", UserEmail: "bob@example.com"},
				},
				Prompts: []DashboardPromptRow{
					{Project: "proj-a", SyncID: "prompt-alice-1", UserEmail: "alice@example.com"},
					{Project: "proj-a", SyncID: "prompt-bob-1", UserEmail: "bob@example.com"},
				},
			},
		},
		admin: DashboardAdminOverview{Projects: 1, Contributors: 2, Chunks: 42},
	}
	cs := newInjectableStore(model)
	scope := &ReadScope{Email: "alice@example.com", IsAdmin: false}
	stats, err := cs.ScopedMemberOverview(scope)
	if err != nil {
		t.Fatalf("ScopedMemberOverview: unexpected error: %v", err)
	}
	if stats.Observations != 1 {
		t.Errorf("member stats: expected Observations=1 (alice only), got %d", stats.Observations)
	}
	if stats.Sessions != 1 {
		t.Errorf("member stats: expected Sessions=1 (alice only), got %d", stats.Sessions)
	}
	if stats.Prompts != 1 {
		t.Errorf("member stats: expected Prompts=1 (alice only), got %d", stats.Prompts)
	}
	// Member must NOT see team-wide Contributors or Chunks.
	if stats.Contributors != 0 {
		t.Errorf("member stats: expected Contributors=0 (not team-wide), got %d", stats.Contributors)
	}
	if stats.Chunks != 0 {
		t.Errorf("member stats: expected Chunks=0 (not team-wide), got %d", stats.Chunks)
	}
}

// TestScopedMemberOverview_AdminGetsAdminOverview verifies that an admin scope
// returns the team-wide DashboardAdminOverview (not member-scoped counts).
func TestScopedMemberOverview_AdminGetsAdminOverview(t *testing.T) {
	model := dashboardReadModel{
		admin: DashboardAdminOverview{Projects: 5, Contributors: 3, Chunks: 100},
	}
	cs := newInjectableStore(model)
	scope := &ReadScope{Email: "admin@example.com", IsAdmin: true}
	stats, err := cs.ScopedMemberOverview(scope)
	if err != nil {
		t.Fatalf("ScopedMemberOverview (admin): unexpected error: %v", err)
	}
	if stats.Projects != 5 || stats.Contributors != 3 || stats.Chunks != 100 {
		t.Errorf("admin: expected team-wide (5/3/100), got (%d/%d/%d)", stats.Projects, stats.Contributors, stats.Chunks)
	}
}

// TestScopedMemberOverview_MissingIdentityDenies verifies that a missing email
// on a non-admin returns ErrDashboardIdentityRequired.
func TestScopedMemberOverview_MissingIdentityDenies(t *testing.T) {
	cs := newInjectableStore(dashboardReadModel{})
	scope := &ReadScope{Email: "", IsAdmin: false}
	_, err := cs.ScopedMemberOverview(scope)
	if !errors.Is(err, ErrDashboardIdentityRequired) {
		t.Errorf("missing identity: expected ErrDashboardIdentityRequired, got %v", err)
	}
}
