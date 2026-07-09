package cloudstore

// TestScopedPreservesLastClientVersion is the regression guard for the production
// bug where fleet view always showed "unknown" for contributor client versions.
//
// Root cause: scoped() re-aggregates contributors from projectDetails[].Contributors
// using contributorAgg, which did not track LastClientVersion.  The rebuilt
// DashboardContributorRow was emitted without LastClientVersion so it defaulted
// to "" → rendered as "unknown" in the template.
//
// This test only triggers the bug when allowed is a non-empty, non-wildcard map
// (the scoped re-aggregation branch). With empty/wildcard the model is returned
// unchanged, so the version would already be present — that is why existing tests
// did not catch it.
//
// RED: fails on unpatched code (LastClientVersion is dropped → "").
// GREEN: passes after adding lastClientVersion to contributorAgg and propagating
//        it into the rebuilt DashboardContributorRow.
import (
	"testing"
)

func TestScopedPreservesLastClientVersion(t *testing.T) {
	const wantVersion = "1.17.0-viva.9"
	const contributor = "alice@example.com"
	const project = "proj-alpha"

	// Build a dashboardReadModel whose projectDetails contain a contributor
	// with LastClientVersion set.
	model := dashboardReadModel{
		projects: []DashboardProjectRow{
			{Project: project, Chunks: 5},
		},
		contributors: []DashboardContributorRow{
			{CreatedBy: contributor, Chunks: 5, Projects: 1, LastClientVersion: wantVersion},
		},
		projectDetails: map[string]DashboardProjectDetail{
			project: {
				Project: project,
				Stats:   DashboardProjectRow{Project: project, Chunks: 5},
				Contributors: []DashboardContributorRow{
					{
						CreatedBy:         contributor,
						Chunks:            5,
						Projects:          1,
						LastClientVersion: wantVersion,
					},
				},
			},
		},
		admin: DashboardAdminOverview{Projects: 1, Contributors: 1, Chunks: 5},
	}

	// allowed is a non-empty, non-wildcard map that contains our project.
	// This forces scoped() to take the re-aggregation branch (lines 822-884),
	// which is the branch that had the bug.
	allowed := map[string]struct{}{
		project: {},
	}

	scoped := model.scoped(allowed)

	// Verify the contributors slice has exactly one entry.
	if len(scoped.contributors) != 1 {
		t.Fatalf("expected 1 contributor after scoping, got %d", len(scoped.contributors))
	}

	got := scoped.contributors[0]
	if got.CreatedBy != contributor {
		t.Errorf("contributor.CreatedBy: want %q, got %q", contributor, got.CreatedBy)
	}

	// This is the assertion that fails on unpatched code.
	if got.LastClientVersion != wantVersion {
		t.Errorf("contributor.LastClientVersion: want %q, got %q — version dropped by scoped() re-aggregation",
			wantVersion, got.LastClientVersion)
	}
}

// TestScopedPreservesLastClientVersion_WildcardBypass verifies that the wildcard
// early-return path still works and also preserves the version (sanity check).
func TestScopedPreservesLastClientVersion_WildcardBypass(t *testing.T) {
	const wantVersion = "1.17.0-viva.9"
	const contributor = "alice@example.com"

	model := dashboardReadModel{
		contributors: []DashboardContributorRow{
			{CreatedBy: contributor, Chunks: 3, LastClientVersion: wantVersion},
		},
	}

	// Wildcard → early return, model unchanged.
	scoped := model.scoped(map[string]struct{}{"*": {}})

	if len(scoped.contributors) != 1 {
		t.Fatalf("wildcard: expected 1 contributor, got %d", len(scoped.contributors))
	}
	if scoped.contributors[0].LastClientVersion != wantVersion {
		t.Errorf("wildcard: LastClientVersion: want %q, got %q", wantVersion, scoped.contributors[0].LastClientVersion)
	}
}

// TestScopedPreservesLastClientVersion_MultipleContributors verifies that when
// multiple contributors exist across multiple projects, each one retains its own
// LastClientVersion after scoping.
func TestScopedPreservesLastClientVersion_MultipleContributors(t *testing.T) {
	model := dashboardReadModel{
		projects: []DashboardProjectRow{
			{Project: "proj-a", Chunks: 3},
			{Project: "proj-b", Chunks: 2},
		},
		projectDetails: map[string]DashboardProjectDetail{
			"proj-a": {
				Project: "proj-a",
				Stats:   DashboardProjectRow{Project: "proj-a", Chunks: 3},
				Contributors: []DashboardContributorRow{
					{CreatedBy: "alice@example.com", Chunks: 3, LastClientVersion: "1.17.0-viva.9"},
				},
			},
			"proj-b": {
				Project: "proj-b",
				Stats:   DashboardProjectRow{Project: "proj-b", Chunks: 2},
				Contributors: []DashboardContributorRow{
					{CreatedBy: "bob@example.com", Chunks: 2, LastClientVersion: "1.16.0-viva.3"},
				},
			},
		},
	}

	allowed := map[string]struct{}{
		"proj-a": {},
		"proj-b": {},
	}

	scoped := model.scoped(allowed)

	if len(scoped.contributors) != 2 {
		t.Fatalf("expected 2 contributors, got %d", len(scoped.contributors))
	}

	byEmail := map[string]DashboardContributorRow{}
	for _, c := range scoped.contributors {
		byEmail[c.CreatedBy] = c
	}

	alice, ok := byEmail["alice@example.com"]
	if !ok {
		t.Fatal("alice not found in scoped contributors")
	}
	if alice.LastClientVersion != "1.17.0-viva.9" {
		t.Errorf("alice: LastClientVersion: want %q, got %q", "1.17.0-viva.9", alice.LastClientVersion)
	}

	bob, ok := byEmail["bob@example.com"]
	if !ok {
		t.Fatal("bob not found in scoped contributors")
	}
	if bob.LastClientVersion != "1.16.0-viva.3" {
		t.Errorf("bob: LastClientVersion: want %q, got %q", "1.16.0-viva.3", bob.LastClientVersion)
	}
}

// TestScopedPreservesLastClientVersion_ContributorAcrossProjects verifies that
// when one contributor appears in multiple allowed projects, the first non-empty
// LastClientVersion is preserved (per-contributor-global, consistent across projects).
func TestScopedPreservesLastClientVersion_ContributorAcrossProjects(t *testing.T) {
	const wantVersion = "1.17.0-viva.9"

	model := dashboardReadModel{
		projects: []DashboardProjectRow{
			{Project: "proj-a", Chunks: 3},
			{Project: "proj-b", Chunks: 2},
		},
		projectDetails: map[string]DashboardProjectDetail{
			"proj-a": {
				Project: "proj-a",
				Stats:   DashboardProjectRow{Project: "proj-a", Chunks: 3},
				Contributors: []DashboardContributorRow{
					{CreatedBy: "alice@example.com", Chunks: 3, LastClientVersion: wantVersion},
				},
			},
			"proj-b": {
				Project: "proj-b",
				Stats:   DashboardProjectRow{Project: "proj-b", Chunks: 2},
				// Same contributor in second project — version is consistent.
				Contributors: []DashboardContributorRow{
					{CreatedBy: "alice@example.com", Chunks: 2, LastClientVersion: wantVersion},
				},
			},
		},
	}

	allowed := map[string]struct{}{
		"proj-a": {},
		"proj-b": {},
	}

	scoped := model.scoped(allowed)

	if len(scoped.contributors) != 1 {
		t.Fatalf("expected 1 deduplicated contributor, got %d", len(scoped.contributors))
	}
	if scoped.contributors[0].LastClientVersion != wantVersion {
		t.Errorf("across-projects: LastClientVersion: want %q, got %q",
			wantVersion, scoped.contributors[0].LastClientVersion)
	}
	// Chunks should be summed across both projects.
	if scoped.contributors[0].Chunks != 5 {
		t.Errorf("across-projects: Chunks: want 5, got %d", scoped.contributors[0].Chunks)
	}
}
