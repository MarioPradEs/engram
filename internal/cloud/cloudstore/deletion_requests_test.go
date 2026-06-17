package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
)

// openTestDeletionDB opens a CloudStore with its own schema for deletion-request tests.
// Requires CLOUDSTORE_TEST_DSN in the environment; skips the test when absent.
func openTestDeletionDB(t *testing.T) *CloudStore {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN so a per-test search_path can be attached")
	}

	schema := fmt.Sprintf("dr_test_%d", time.Now().UnixNano())

	// Create schema via admin connection first.
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("openTestDeletionDB: sql.Open admin: %v", err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("openTestDeletionDB: create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	testDSN := dsn + "?search_path=" + schema
	if strings.Contains(dsn, "?") {
		testDSN = dsn + "&search_path=" + schema
	}

	cs, err := New(cloud.Config{DSN: testDSN})
	if err != nil {
		t.Fatalf("openTestDeletionDB: New: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// DR1: CreateDeletionRequest inserts a pending row; duplicate pending for same sync_id → conflict error.
func TestDR1_CreateDeletionRequest_PendingAndDuplicate(t *testing.T) {
	cs := openTestDeletionDB(t)
	ctx := context.Background()

	req := DeletionRequest{
		TargetSyncID:    "obs-abc123",
		RequesterEmail:  "esanchez@vivastudios.com",
		Reason:          "sensitive content",
	}
	id, err := cs.CreateDeletionRequest(ctx, req)
	if err != nil {
		t.Fatalf("CreateDeletionRequest: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}

	// Duplicate pending for same sync_id → conflict.
	_, err = cs.CreateDeletionRequest(ctx, req)
	if err == nil {
		t.Fatal("expected conflict error for duplicate pending request, got nil")
	}
	if !strings.Contains(err.Error(), "conflict") && !strings.Contains(err.Error(), "already") && !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected conflict/already/duplicate in error, got: %v", err)
	}
}

// DR2: ListPendingDeletionRequests returns only pending rows.
func TestDR2_ListPendingDeletionRequests(t *testing.T) {
	cs := openTestDeletionDB(t)
	ctx := context.Background()

	// Insert pending.
	_, err := cs.CreateDeletionRequest(ctx, DeletionRequest{
		TargetSyncID:   "obs-1",
		RequesterEmail: "user1@vivastudios.com",
		Reason:         "reason1",
	})
	if err != nil {
		t.Fatalf("CreateDeletionRequest: %v", err)
	}
	// Insert another pending.
	id2, err := cs.CreateDeletionRequest(ctx, DeletionRequest{
		TargetSyncID:   "obs-2",
		RequesterEmail: "user2@vivastudios.com",
		Reason:         "reason2",
	})
	if err != nil {
		t.Fatalf("CreateDeletionRequest 2: %v", err)
	}

	// Reject the second one to check filtering.
	if err := cs.RejectDeletionRequest(ctx, id2, "admin@vivastudios.com"); err != nil {
		t.Fatalf("RejectDeletionRequest: %v", err)
	}

	pending, err := cs.ListPendingDeletionRequests(ctx)
	if err != nil {
		t.Fatalf("ListPendingDeletionRequests: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending request, got %d", len(pending))
	}
	if pending[0].TargetSyncID != "obs-1" {
		t.Errorf("expected pending for obs-1, got %q", pending[0].TargetSyncID)
	}
}

// DR3: AcceptDeletionRequest → status=accepted, decided_by set, decided_at set.
func TestDR3_AcceptDeletionRequest(t *testing.T) {
	cs := openTestDeletionDB(t)
	ctx := context.Background()

	// Seed a real observation chunk so the hard-delete has something to remove.
	// WriteChunk puts a row in cloud_chunks; the delete path targets cloud_mutations.
	// For this test we just verify status transition + 0-rows-deleted is handled gracefully.
	id, err := cs.CreateDeletionRequest(ctx, DeletionRequest{
		TargetSyncID:   "obs-dr3",
		RequesterEmail: "esanchez@vivastudios.com",
		Reason:         "test accept",
	})
	if err != nil {
		t.Fatalf("CreateDeletionRequest: %v", err)
	}

	adminEmail := "mpradas@vivastudios.com"
	if err := cs.AcceptDeletionRequest(ctx, id, adminEmail); err != nil {
		t.Fatalf("AcceptDeletionRequest: %v", err)
	}

	// Verify state machine moved to accepted.
	found, err := cs.GetDeletionRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetDeletionRequest after accept: %v", err)
	}
	if found.Status != "accepted" {
		t.Errorf("status=%q, want accepted", found.Status)
	}
	if found.DecidedBy != adminEmail {
		t.Errorf("decided_by=%q, want %q", found.DecidedBy, adminEmail)
	}
	if found.DecidedAt == nil {
		t.Error("decided_at is nil, want non-nil")
	}
}

// DR4: RejectDeletionRequest → status=rejected; observation unchanged.
func TestDR4_RejectDeletionRequest(t *testing.T) {
	cs := openTestDeletionDB(t)
	ctx := context.Background()

	id, err := cs.CreateDeletionRequest(ctx, DeletionRequest{
		TargetSyncID:   "obs-dr4",
		RequesterEmail: "esanchez@vivastudios.com",
		Reason:         "test reject",
	})
	if err != nil {
		t.Fatalf("CreateDeletionRequest: %v", err)
	}

	adminEmail := "mpradas@vivastudios.com"
	if err := cs.RejectDeletionRequest(ctx, id, adminEmail); err != nil {
		t.Fatalf("RejectDeletionRequest: %v", err)
	}

	found, err := cs.GetDeletionRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetDeletionRequest after reject: %v", err)
	}
	if found.Status != "rejected" {
		t.Errorf("status=%q, want rejected", found.Status)
	}
	if found.DecidedBy != adminEmail {
		t.Errorf("decided_by=%q, want %q", found.DecidedBy, adminEmail)
	}
	if found.DecidedAt == nil {
		t.Error("decided_at is nil, want non-nil")
	}
}

// DR5: Accept on already-accepted row → returns ErrRequestAlreadyDecided.
func TestDR5_AcceptAlreadyAccepted_ReturnsErrAlreadyDecided(t *testing.T) {
	cs := openTestDeletionDB(t)
	ctx := context.Background()

	id, err := cs.CreateDeletionRequest(ctx, DeletionRequest{
		TargetSyncID:   "obs-dr5",
		RequesterEmail: "esanchez@vivastudios.com",
		Reason:         "test already decided",
	})
	if err != nil {
		t.Fatalf("CreateDeletionRequest: %v", err)
	}

	// First accept.
	if err := cs.AcceptDeletionRequest(ctx, id, "mpradas@vivastudios.com"); err != nil {
		t.Fatalf("AcceptDeletionRequest (first): %v", err)
	}

	// Second accept → ErrRequestAlreadyDecided.
	err = cs.AcceptDeletionRequest(ctx, id, "mpradas@vivastudios.com")
	if err == nil {
		t.Fatal("expected ErrRequestAlreadyDecided, got nil")
	}
	if !errors.Is(err, ErrRequestAlreadyDecided) {
		t.Errorf("expected ErrRequestAlreadyDecided, got: %v", err)
	}
}

// DR6: PendingDeletionRequestCount returns the integer count of pending rows.
func TestDR6_PendingDeletionRequestCount(t *testing.T) {
	cs := openTestDeletionDB(t)
	ctx := context.Background()

	count0, err := cs.PendingDeletionRequestCount(ctx)
	if err != nil {
		t.Fatalf("PendingDeletionRequestCount (empty): %v", err)
	}
	if count0 != 0 {
		t.Errorf("expected 0 pending initially, got %d", count0)
	}

	_, _ = cs.CreateDeletionRequest(ctx, DeletionRequest{TargetSyncID: "obs-c1", RequesterEmail: "a@vivastudios.com"})
	_, _ = cs.CreateDeletionRequest(ctx, DeletionRequest{TargetSyncID: "obs-c2", RequesterEmail: "b@vivastudios.com"})

	count2, err := cs.PendingDeletionRequestCount(ctx)
	if err != nil {
		t.Fatalf("PendingDeletionRequestCount (2 pending): %v", err)
	}
	if count2 != 2 {
		t.Errorf("expected 2 pending, got %d", count2)
	}
}
