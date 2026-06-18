package cloudstore

import (
	"errors"
	"testing"
)

// TestApplyReadScope_Admin verifies that an admin scope returns all rows unmodified.
func TestApplyReadScope_Admin(t *testing.T) {
	rows := []DashboardObservationRow{
		{SyncID: "obs-1", UserEmail: "alice@example.com"},
		{SyncID: "obs-2", UserEmail: "bob@example.com"},
	}
	scope := &ReadScope{Email: "admin@example.com", IsAdmin: true}
	result, err := applyReadScope(scope, rows, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		t.Fatalf("admin scope: unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("admin scope: expected 2 rows, got %d", len(result))
	}
}

// TestApplyReadScope_Member verifies that a member scope returns only their own rows.
func TestApplyReadScope_Member(t *testing.T) {
	rows := []DashboardObservationRow{
		{SyncID: "obs-1", UserEmail: "alice@example.com"},
		{SyncID: "obs-2", UserEmail: "bob@example.com"},
		{SyncID: "obs-3", UserEmail: "alice@example.com"},
	}
	scope := &ReadScope{Email: "alice@example.com", IsAdmin: false}
	result, err := applyReadScope(scope, rows, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		t.Fatalf("member scope: unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("member scope: expected 2 rows for alice, got %d", len(result))
	}
	for _, r := range result {
		if r.UserEmail != "alice@example.com" {
			t.Errorf("member scope: expected only alice's rows, got row with email %q", r.UserEmail)
		}
	}
}

// TestApplyReadScope_MissingIdentityDenies verifies that a missing email on a non-admin
// returns ErrDashboardIdentityRequired and a nil slice.
func TestApplyReadScope_MissingIdentityDenies(t *testing.T) {
	rows := []DashboardObservationRow{
		{SyncID: "obs-1", UserEmail: "alice@example.com"},
	}
	scope := &ReadScope{Email: "", IsAdmin: false}
	result, err := applyReadScope(scope, rows, func(r DashboardObservationRow) string { return r.UserEmail })
	if !errors.Is(err, ErrDashboardIdentityRequired) {
		t.Errorf("missing identity: expected ErrDashboardIdentityRequired, got %v", err)
	}
	if result != nil {
		t.Errorf("missing identity: expected nil slice, got %v", result)
	}
}

// TestApplyReadScope_CaseInsensitiveMatch verifies case-insensitive email matching.
func TestApplyReadScope_CaseInsensitiveMatch(t *testing.T) {
	rows := []DashboardObservationRow{
		{SyncID: "obs-1", UserEmail: "Alice@Example.COM"},
		{SyncID: "obs-2", UserEmail: "bob@example.com"},
	}
	scope := &ReadScope{Email: "alice@example.com", IsAdmin: false}
	result, err := applyReadScope(scope, rows, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		t.Fatalf("case-insensitive: unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("case-insensitive: expected 1 row for alice (case-insensitive), got %d", len(result))
	}
	if result[0].SyncID != "obs-1" {
		t.Errorf("case-insensitive: expected obs-1, got %q", result[0].SyncID)
	}
}

// TestApplyReadScope_NilScopeUnscoped verifies that a nil scope (admin fast-path) returns all rows.
func TestApplyReadScope_NilScopeUnscoped(t *testing.T) {
	rows := []DashboardObservationRow{
		{SyncID: "obs-1", UserEmail: "alice@example.com"},
		{SyncID: "obs-2", UserEmail: "bob@example.com"},
	}
	result, err := applyReadScope[DashboardObservationRow](nil, rows, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		t.Fatalf("nil scope: unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("nil scope: expected 2 rows, got %d", len(result))
	}
}

// TestApplyReadScope_MemberSeesNoOtherRows verifies a member cannot see another user's rows.
func TestApplyReadScope_MemberSeesNoOtherRows(t *testing.T) {
	rows := []DashboardObservationRow{
		{SyncID: "obs-bob-1", UserEmail: "bob@example.com"},
		{SyncID: "obs-bob-2", UserEmail: "bob@example.com"},
	}
	scope := &ReadScope{Email: "alice@example.com", IsAdmin: false}
	result, err := applyReadScope(scope, rows, func(r DashboardObservationRow) string { return r.UserEmail })
	if err != nil {
		t.Fatalf("member sees no other rows: unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("member sees no other rows: expected 0 rows, got %d", len(result))
	}
}
