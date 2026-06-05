package tui

import (
	"strings"
	"testing"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newScopeFixture(t *testing.T) (testFixture, Model) {
	t.Helper()
	fx := newTestFixture(t)
	m := New(fx.store, "")
	m.Screen = ScreenObservationDetail
	m.PrevScreen = ScreenRecent
	obs, err := fx.store.GetObservation(fx.obsID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	m.SelectedObservation = obs
	return fx, m
}

// ─── Display: scope line in ObservationDetail view ───────────────────────────

func TestViewObservationDetailShowsScope(t *testing.T) {
	_, m := newScopeFixture(t)
	// fixture creates obs with Scope="project"
	out := m.viewObservationDetail()
	if !strings.Contains(out, "Scope:") {
		t.Fatal("detail view should render a 'Scope:' label")
	}
	if !strings.Contains(out, "project") {
		t.Fatal("detail view should render the observation's scope value")
	}
}

func TestViewObservationDetailHelpIncludesP(t *testing.T) {
	_, m := newScopeFixture(t)
	out := m.viewObservationDetail()
	// Help bar must advertise the 'p' key
	if !strings.Contains(out, "p") {
		t.Fatal("help bar should include 'p' to hint the scope selector")
	}
}

// ─── Opening the scope selector ──────────────────────────────────────────────

func TestPressPInObservationDetailOpensScopeSelector(t *testing.T) {
	_, m := newScopeFixture(t)
	m.PrevScreen = ScreenRecent // entered detail from the Recent list

	updatedModel, cmd := m.handleObservationDetailKeys("p")
	updated := updatedModel.(Model)

	if updated.Screen != ScreenScopeSelector {
		t.Fatalf("screen = %v, want ScreenScopeSelector", updated.Screen)
	}
	// Opening the selector must NOT clobber PrevScreen, so ESC in the detail
	// still returns to the real origin afterwards (regression guard).
	if updated.PrevScreen != ScreenRecent {
		t.Fatalf("PrevScreen = %v, want it preserved as ScreenRecent", updated.PrevScreen)
	}
	if cmd != nil {
		t.Fatal("opening scope selector should not dispatch a command")
	}
}

func TestPressPWithNoObservationIsNoOp(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenObservationDetail
	m.SelectedObservation = nil

	updatedModel, cmd := m.handleObservationDetailKeys("p")
	updated := updatedModel.(Model)
	if updated.Screen != ScreenObservationDetail {
		t.Fatal("pressing p with no observation should not change screen")
	}
	if cmd != nil {
		t.Fatal("should not dispatch a command when observation is nil")
	}
}

// ─── Scope selector renders the 4 tiers ──────────────────────────────────────

func TestViewScopeSelectorRendersAllTiers(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.PrevScreen = ScreenObservationDetail
	// fixture obs scope = "project" → cursor should be positioned at "project"
	m.ScopeSelectorCursor = scopeIndex("project")

	out := m.viewScopeSelector()

	for _, tier := range scopeTiers {
		if !strings.Contains(out, tier) {
			t.Fatalf("scope selector should render tier %q", tier)
		}
	}
}

func TestViewScopeSelectorHighlightsCurrentScope(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.ScopeSelectorCursor = scopeIndex("project")

	out := m.viewScopeSelector()

	// The cursor marker must appear somewhere in the output.
	if !strings.Contains(out, "▸") {
		t.Fatal("selected tier should be highlighted with cursor marker")
	}
}

// ─── Navigation inside scope selector ────────────────────────────────────────

func TestScopeSelectorNavigationDownAndUp(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.ScopeSelectorCursor = 0

	updatedModel, _ := m.handleScopeSelectorKeys("down")
	updated := updatedModel.(Model)
	if updated.ScopeSelectorCursor != 1 {
		t.Fatalf("cursor = %d, want 1", updated.ScopeSelectorCursor)
	}

	updatedModel, _ = updated.handleScopeSelectorKeys("up")
	updated = updatedModel.(Model)
	if updated.ScopeSelectorCursor != 0 {
		t.Fatalf("cursor = %d, want 0", updated.ScopeSelectorCursor)
	}
}

func TestScopeSelectorNavigationAliasesJK(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.ScopeSelectorCursor = 0

	updatedModel, _ := m.handleScopeSelectorKeys("j")
	if updatedModel.(Model).ScopeSelectorCursor != 1 {
		t.Fatal("j should behave as down")
	}
	updatedModel, _ = updatedModel.(Model).handleScopeSelectorKeys("k")
	if updatedModel.(Model).ScopeSelectorCursor != 0 {
		t.Fatal("k should behave as up")
	}
}

func TestScopeSelectorBoundaries(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.ScopeSelectorCursor = 0

	updatedModel, _ := m.handleScopeSelectorKeys("up")
	if updatedModel.(Model).ScopeSelectorCursor != 0 {
		t.Fatal("up at top should stay at zero")
	}

	m.ScopeSelectorCursor = len(scopeTiers) - 1
	updatedModel, _ = m.handleScopeSelectorKeys("down")
	if updatedModel.(Model).ScopeSelectorCursor != len(scopeTiers)-1 {
		t.Fatal("down at bottom should stay at last item")
	}
}

// ─── Esc cancels without changing scope ──────────────────────────────────────

func TestScopeSelectorEscCancels(t *testing.T) {
	_, m := newScopeFixture(t)
	originalScope := m.SelectedObservation.Scope

	m.Screen = ScreenScopeSelector
	m.PrevScreen = ScreenObservationDetail
	m.ScopeSelectorCursor = scopeIndex("team") // changed cursor, but we ESC

	updatedModel, cmd := m.handleScopeSelectorKeys("esc")
	updated := updatedModel.(Model)

	if updated.Screen != ScreenObservationDetail {
		t.Fatalf("screen = %v, want ScreenObservationDetail", updated.Screen)
	}
	// Scope must not have changed
	if updated.SelectedObservation != nil && updated.SelectedObservation.Scope != originalScope {
		t.Fatalf("scope changed on cancel: %q", updated.SelectedObservation.Scope)
	}
	if cmd != nil {
		t.Fatal("esc should not dispatch a command")
	}
}

// Regression: opening the scope selector from the detail must NOT clobber
// PrevScreen, so ESC in the detail still returns to the original origin
// (e.g. the Recent list) instead of looping back into the detail.
func TestObservationDetailEscReturnsToOriginAfterScopeSelector(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenObservationDetail
	m.PrevScreen = ScreenRecent // entered detail from the Recent list

	// Open the scope selector with 'p'
	m1, _ := m.handleObservationDetailKeys("p")
	mm := m1.(Model)
	if mm.Screen != ScreenScopeSelector {
		t.Fatalf("p did not open scope selector: screen=%v", mm.Screen)
	}

	// Cancel the selector with esc → back to detail
	m2, _ := mm.handleScopeSelectorKeys("esc")
	mm2 := m2.(Model)
	if mm2.Screen != ScreenObservationDetail {
		t.Fatalf("esc in selector did not return to detail: screen=%v", mm2.Screen)
	}

	// esc in the detail must return to the ORIGIN (Recent), not stay in detail
	m3, _ := mm2.handleObservationDetailKeys("esc")
	mm3 := m3.(Model)
	if mm3.Screen != ScreenRecent {
		t.Fatalf("esc in detail after scope selector did not return to origin: got screen=%v, want ScreenRecent", mm3.Screen)
	}
}

func TestScopeSelectorQAliasForEsc(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.PrevScreen = ScreenObservationDetail

	updatedModel, _ := m.handleScopeSelectorKeys("q")
	if updatedModel.(Model).Screen != ScreenObservationDetail {
		t.Fatal("q should behave like esc in scope selector")
	}
}

// ─── Confirm dispatches update + reload ──────────────────────────────────────

func TestScopeSelectorEnterDispatchesUpdateCommand(t *testing.T) {
	fx, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.PrevScreen = ScreenObservationDetail
	// Choose "team" — different from fixture's "project"
	m.ScopeSelectorCursor = scopeIndex("team")

	updatedModel, cmd := m.handleScopeSelectorKeys("enter")
	updated := updatedModel.(Model)

	if cmd == nil {
		t.Fatal("enter should dispatch an update command")
	}
	// Screen should still show detail (not jump immediately; the msg will trigger)
	if updated.Screen == ScreenScopeSelector {
		t.Fatal("should have left scope selector after confirm")
	}

	// Execute the command and verify it produces a scopeUpdateMsg
	msg := cmd()
	suMsg, ok := msg.(scopeUpdateMsg)
	if !ok {
		t.Fatalf("command produced %T, want scopeUpdateMsg", msg)
	}
	if suMsg.err != nil {
		t.Fatalf("unexpected error: %v", suMsg.err)
	}
	if suMsg.observation == nil {
		t.Fatal("scopeUpdateMsg should carry the updated observation")
	}
	if suMsg.observation.Scope != "team" {
		t.Fatalf("scope = %q, want %q", suMsg.observation.Scope, "team")
	}

	// Verify the DB was actually updated
	obs, err := fx.store.GetObservation(fx.obsID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Scope != "team" {
		t.Fatalf("DB scope = %q, want %q", obs.Scope, "team")
	}
}

func TestScopeUpdateMsgAppliesNewScopeAndFeedback(t *testing.T) {
	fx, m := newScopeFixture(t)
	updatedScope := "personal"

	obs, _ := fx.store.GetObservation(fx.obsID)
	obs.Scope = updatedScope

	updatedModel, cmd := m.Update(scopeUpdateMsg{observation: obs})
	updated := updatedModel.(Model)

	if updated.SelectedObservation == nil || updated.SelectedObservation.Scope != updatedScope {
		t.Fatalf("scope = %q, want %q", updated.SelectedObservation.Scope, updatedScope)
	}
	if updated.ScopeFeedback == "" {
		t.Fatal("ScopeFeedback should be set after update")
	}
	if !strings.Contains(updated.ScopeFeedback, "✓") {
		t.Fatalf("ScopeFeedback should contain ✓, got %q", updated.ScopeFeedback)
	}
	if cmd == nil {
		t.Fatal("should schedule feedback clear command")
	}
}

func TestScopeUpdateMsgErrorSetsErrorMsg(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenObservationDetail

	updatedModel, cmd := m.Update(scopeUpdateMsg{err: errScopeUpdateFailed})
	updated := updatedModel.(Model)

	if updated.ErrorMsg == "" {
		t.Fatal("error in scopeUpdateMsg should set ErrorMsg")
	}
	if cmd != nil {
		t.Fatal("error path should not dispatch command")
	}
}

func TestScopeFeedbackClearsAfterMsg(t *testing.T) {
	_, m := newScopeFixture(t)
	m.ScopeFeedback = "✓ Scope updated"

	updatedModel, _ := m.Update(scopeFeedbackClearMsg{})
	updated := updatedModel.(Model)
	if updated.ScopeFeedback != "" {
		t.Fatal("scopeFeedbackClearMsg should clear ScopeFeedback")
	}
}

// ─── View router covers ScreenScopeSelector ──────────────────────────────────

func TestViewRouterCoversScopeSelector(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.ScopeSelectorCursor = 0

	out := m.View()
	if !strings.Contains(out, "Scope") {
		t.Fatal("View() router should delegate to viewScopeSelector")
	}
}

// ─── handleKeyPress router covers ScreenScopeSelector ────────────────────────

func TestHandleKeyPressRouterCoversScopeSelectorAndClearsError(t *testing.T) {
	_, m := newScopeFixture(t)
	m.Screen = ScreenScopeSelector
	m.ErrorMsg = "old error"
	m.ScopeSelectorCursor = 0

	updatedModel, _ := m.handleKeyPress("esc")
	updated := updatedModel.(Model)
	if updated.ErrorMsg != "" {
		t.Fatal("handleKeyPress should clear error for ScreenScopeSelector")
	}
}

// ─── updateObservationScope command unit test ─────────────────────────────────

func TestUpdateObservationScopeCommand(t *testing.T) {
	fx, _ := newScopeFixture(t)

	newScope := "department"
	cmd := updateObservationScope(fx.store, fx.obsID, newScope)
	msg := cmd()

	suMsg, ok := msg.(scopeUpdateMsg)
	if !ok {
		t.Fatalf("command produced %T, want scopeUpdateMsg", msg)
	}
	if suMsg.err != nil {
		t.Fatalf("unexpected error: %v", suMsg.err)
	}
	if suMsg.observation == nil || suMsg.observation.Scope != "department" {
		t.Fatalf("scope = %q, want department", suMsg.observation.Scope)
	}
}

// ─── scopeIndex helper ────────────────────────────────────────────────────────

func TestScopeIndex(t *testing.T) {
	for i, tier := range scopeTiers {
		got := scopeIndex(tier)
		if got != i {
			t.Fatalf("scopeIndex(%q) = %d, want %d", tier, got, i)
		}
	}
	// Unknown scope defaults to 0
	if scopeIndex("unknown") != 0 {
		t.Fatal("scopeIndex with unknown scope should return 0")
	}
}
