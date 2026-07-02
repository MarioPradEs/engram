package cloudserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// enrollMu serializes all self-service enroll/unenroll writes to users.yaml.
// It guards the complete read-Lookup-modify-WriteAtomic-RunLocalGitCommit-UserReload
// cycle so that concurrent requests from different goroutines never produce a
// lost update across the read-modify-write window (D8).
var enrollMu sync.Mutex

// handleSelfEnrollProject handles POST /user/enrolled-projects.
//
// The authenticated caller adds a project to their own Enrolled list.
// Operation is idempotent: re-enrolling an already-enrolled project is a no-op
// that returns 200. Auth is enforced by the withAuth wrapper at route registration.
func (s *CloudServer) handleSelfEnrollProject(w http.ResponseWriter, r *http.Request) {
	project, ok := s.parseEnrollBody(w, r)
	if !ok {
		return
	}

	email := s.callerEmail(r.Context())
	if email == "" {
		http.Error(w, "could not determine caller identity", http.StatusInternalServerError)
		return
	}

	lister, usersPath, ok := s.enrollWriteSetup(w)
	if !ok {
		return
	}

	enrollMu.Lock()
	defer enrollMu.Unlock()

	principals := lister.List()
	for i, p := range principals {
		if !strings.EqualFold(p.Email, email) {
			continue
		}
		// Dedup: return early when project is already enrolled (idempotent).
		for _, ep := range p.Enrolled {
			if strings.EqualFold(ep, project) {
				jsonResponse(w, http.StatusOK, map[string]any{"status": "ok"})
				return
			}
		}
		principals[i].Enrolled = append(p.Enrolled, project)
		if err := s.writePrincipalsAtomic(principals, usersPath,
			fmt.Sprintf("feat(users): %s self-enrolled project %s", email, project)); err != nil {
			http.Error(w, fmt.Sprintf("write error: %v", err), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	http.Error(w, "caller not found in user directory", http.StatusInternalServerError)
}

// handleSelfUnenrollProject handles DELETE /user/enrolled-projects.
//
// The authenticated caller removes a project from their own Enrolled list.
// Operation is idempotent: removing a project that is not enrolled returns 200.
// Existing cloud observations for the project are NOT deleted (per spec §D7).
func (s *CloudServer) handleSelfUnenrollProject(w http.ResponseWriter, r *http.Request) {
	project, ok := s.parseEnrollBody(w, r)
	if !ok {
		return
	}

	email := s.callerEmail(r.Context())
	if email == "" {
		http.Error(w, "could not determine caller identity", http.StatusInternalServerError)
		return
	}

	lister, usersPath, ok := s.enrollWriteSetup(w)
	if !ok {
		return
	}

	enrollMu.Lock()
	defer enrollMu.Unlock()

	principals := lister.List()
	for i, p := range principals {
		if !strings.EqualFold(p.Email, email) {
			continue
		}
		// Filter out the project (idempotent: no-op if absent).
		newEnrolled := make([]string, 0, len(p.Enrolled))
		for _, ep := range p.Enrolled {
			if !strings.EqualFold(ep, project) {
				newEnrolled = append(newEnrolled, ep)
			}
		}
		principals[i].Enrolled = newEnrolled
		if err := s.writePrincipalsAtomic(principals, usersPath,
			fmt.Sprintf("feat(users): %s self-unenrolled project %s", email, project)); err != nil {
			http.Error(w, fmt.Sprintf("write error: %v", err), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	http.Error(w, "caller not found in user directory", http.StatusInternalServerError)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// parseEnrollBody decodes the JSON request body and returns the project name.
// Writes a 400 response and returns ("", false) on any failure.
func (s *CloudServer) parseEnrollBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Project string `json:"project"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return "", false
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return "", false
	}
	return project, true
}

// callerEmail returns the authenticated caller's email from the request context.
// Uses a structural interface assertion on s.auth (same pattern as handlePushChunk).
// Returns "" when s.auth does not implement Attribution or no principal is in ctx.
func (s *CloudServer) callerEmail(ctx context.Context) string {
	if s.auth == nil {
		return ""
	}
	type attrProvider interface {
		Attribution(ctx context.Context) cloudstore.Attribution
	}
	if ap, ok := s.auth.(attrProvider); ok {
		return strings.ToLower(strings.TrimSpace(ap.Attribution(ctx).UserEmail))
	}
	return ""
}

// enrollWriteSetup validates that the server has the necessary components for
// a self-enroll write and returns (lister, usersPath, true) on success.
// Writes a 500 response and returns (nil, "", false) when a component is missing.
func (s *CloudServer) enrollWriteSetup(w http.ResponseWriter) (listableUserDirectory, string, bool) {
	lister, ok := s.authLoader.(listableUserDirectory)
	if !ok {
		http.Error(w, "user directory not configured", http.StatusInternalServerError)
		return nil, "", false
	}
	usersPath := s.resolveUsersFilePath()
	if usersPath == "" {
		http.Error(w, "users file path not configured", http.StatusInternalServerError)
		return nil, "", false
	}
	return lister, usersPath, true
}

// writePrincipalsAtomic writes principals atomically to disk, commits via git
// (non-fatal — logged on failure), then triggers an in-process reload (also
// non-fatal). Returns an error only if the write itself fails.
func (s *CloudServer) writePrincipalsAtomic(principals []users.Principal, usersPath, commitMsg string) error {
	data, err := users.MarshalPrincipals(principals)
	if err != nil {
		return fmt.Errorf("marshal principals: %w", err)
	}
	if err := users.WriteAtomic(usersPath, data, users.ValidatorForPath()); err != nil {
		return fmt.Errorf("write atomic: %w", err)
	}
	repoPath := filepath.Dir(usersPath)
	if err := users.RunLocalGitCommit(repoPath, usersPath, commitMsg); err != nil {
		log.Printf("[engram-cloud] self-enroll git commit failed (non-fatal): %v", err)
	}
	if s.userReloadFn != nil {
		if err := s.userReloadFn(); err != nil {
			log.Printf("[engram-cloud] self-enroll users reload failed (non-fatal): %v", err)
		}
	}
	return nil
}
