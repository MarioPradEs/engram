// Package users provides a YAML-backed user directory loader with live-reload
// support and last-good-state retention.
//
// Usage:
//
//	loader, err := users.NewYAMLLoader("/etc/engram/users.yaml")
//	p, ok := loader.Lookup("alice@vivastudios.com")
//
// On SIGHUP the process should call loader.Reload() to pick up changes.
// Watch(ctx) provides automatic live reload via fsnotify without requiring SIGHUP.
// If the new file is invalid the previous directory remains active.
package users

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// watchDebounce is the interval used to collapse bursts of filesystem events
// (e.g., an atomic rename generates both Create and Rename events on some
// platforms) into a single Reload call.
const watchDebounce = 100 * time.Millisecond

// Principal holds the resolved attributes of a single user.
type Principal struct {
	Email      string
	Name       string
	Department string
	Role       string   // "admin" | "member"
	Enrolled   []string // Enrolled project keys.
	Status     string   // "active" | "offboarding" | "removed"
}

// ─── YAML schema ─────────────────────────────────────────────────────────────

type yamlUser struct {
	Email      string   `yaml:"email"`
	Name       string   `yaml:"name"`
	Department string   `yaml:"department"`
	Role       string   `yaml:"role"`
	Enrolled   []string `yaml:"enrolled"`
	Status     string   `yaml:"status"`
}

type yamlFile struct {
	Users []yamlUser `yaml:"users"`
}

// ─── Enums ───────────────────────────────────────────────────────────────────

// NOTE: Viva Studios departments. Hardcoded as a quick fix to unblock deploy;
// the canonical set should come from operator config (classification-rules.yaml)
// per design decision #805 — tracked as a follow-up.
var validDepartments = map[string]bool{
	"ceo":       true,
	"dev":       true,
	"art":       true,
	"qa":        true,
	"analytics": true,
	"marketing": true,
}

var validRoles = map[string]bool{
	"admin":  true,
	"member": true,
}

var validStatuses = map[string]bool{
	"active":      true,
	"offboarding": true,
	"removed":     true,
}

// ─── YAMLLoader ──────────────────────────────────────────────────────────────

// YAMLLoader is a thread-safe, reload-capable user directory backed by a YAML
// file. Call Reload() to refresh; on failure the last valid state is retained.
type YAMLLoader struct {
	mu      sync.RWMutex
	path    string
	current map[string]Principal // keyed by lowercase email
}

// NewYAMLLoader loads the YAML file at path, validates it, and returns a
// YAMLLoader ready to serve lookups. Returns an error if the initial load fails
// validation.
func NewYAMLLoader(path string) (*YAMLLoader, error) {
	dir, err := loadAndValidate(path)
	if err != nil {
		return nil, err
	}
	return &YAMLLoader{
		path:    path,
		current: dir,
	}, nil
}

// Lookup returns the Principal for email (case-insensitive).
// The second return value is false when the email is not found.
func (l *YAMLLoader) Lookup(email string) (Principal, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	p, ok := l.current[strings.ToLower(strings.TrimSpace(email))]
	return p, ok
}

// SoleAdmin returns the single admin Principal and true when the directory
// contains exactly one user with role "admin". Returns the zero value and false
// when the count is 0 or >1 — callers must treat those cases as errors for
// bypass-token configuration.
func (l *YAMLLoader) SoleAdmin() (Principal, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var admin Principal
	count := 0
	for _, p := range l.current {
		if strings.EqualFold(p.Role, "admin") {
			admin = p
			count++
		}
	}
	if count == 1 {
		return admin, true
	}
	return Principal{}, false
}

// List returns a snapshot of all currently loaded principals as a slice.
// The slice is a copy — callers may iterate it without holding a lock.
// The order of entries is unspecified (map-based iteration).
func (l *YAMLLoader) List() []Principal {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Principal, 0, len(l.current))
	for _, p := range l.current {
		out = append(out, p)
	}
	return out
}

// Reload re-reads and validates the YAML file. On success the directory is
// atomically replaced. On failure the last valid directory is retained and the
// error is returned.
func (l *YAMLLoader) Reload() error {
	dir, err := loadAndValidate(l.path)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.current = dir
	l.mu.Unlock()
	return nil
}

// Watch watches the parent directory of the loader's YAML file for filesystem
// events and calls Reload whenever the watched file is created, written, or
// renamed. It blocks until ctx is cancelled.
//
// The parent directory is watched (not the file itself) because admin writes
// use atomic os.Rename which fires events on the directory (Create / IN_MOVED_TO)
// rather than on the target file directly. Only events where the event path
// matches the loader's file path are acted on; unrelated sibling events are
// silently discarded.
//
// A 100 ms debounce collapses bursts (e.g., a rename generates both Create and
// Rename events on some platforms) into a single Reload call.
//
// Watch is safe to call concurrently with Lookup and Reload.
func (l *YAMLLoader) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("users: Watch: create watcher: %w", err)
	}
	defer watcher.Close()

	dir := filepath.Dir(l.path)
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("users: Watch: watch %q: %w", dir, err)
	}

	target := filepath.Clean(l.path)

	// debounce collapses burst events into one Reload call.
	// It is only accessed inside the select loop (single goroutine), so no
	// additional mutex is needed.
	var debounce *time.Timer

	scheduleReload := func() {
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(watchDebounce, func() {
			if err := l.Reload(); err != nil {
				log.Printf("[engram-cloud] Watch: reload %q: %v (retaining last-good state)", l.path, err)
			}
		})
	}

	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return ctx.Err()

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) || event.Has(fsnotify.Rename) {
				if filepath.Clean(event.Name) == target {
					scheduleReload()
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("[engram-cloud] Watch: fsnotify error: %v", err)
		}
	}
}

// ─── Internal ────────────────────────────────────────────────────────────────

func loadAndValidate(path string) (map[string]Principal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("users: read %q: %w", path, err)
	}

	var f yamlFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("users: parse %q: %w", path, err)
	}

	dir := make(map[string]Principal, len(f.Users))
	hasAdmin := false

	for i, u := range f.Users {
		email := strings.ToLower(strings.TrimSpace(u.Email))
		if email == "" {
			return nil, fmt.Errorf("users: entry %d: email is required", i)
		}
		if !strings.HasSuffix(email, "@vivastudios.com") {
			return nil, fmt.Errorf("users: entry %d: email %q must end with @vivastudios.com", i, email)
		}
		if _, dup := dir[email]; dup {
			return nil, fmt.Errorf("users: duplicate email %q", email)
		}

		dept := strings.ToLower(strings.TrimSpace(u.Department))
		if !validDepartments[dept] {
			return nil, fmt.Errorf("users: entry %q: invalid department %q (valid: %s)",
				email, u.Department, joinKeys(validDepartments))
		}

		role := strings.ToLower(strings.TrimSpace(u.Role))
		if !validRoles[role] {
			return nil, fmt.Errorf("users: entry %q: invalid role %q (valid: admin, member)", email, u.Role)
		}
		if role == "admin" {
			hasAdmin = true
		}

		status := strings.ToLower(strings.TrimSpace(u.Status))
		if !validStatuses[status] {
			return nil, fmt.Errorf("users: entry %q: invalid status %q (valid: active, offboarding, removed)", email, u.Status)
		}

		dir[email] = Principal{
			Email:      email,
			Name:       strings.TrimSpace(u.Name),
			Department: dept,
			Role:       role,
			Enrolled:   u.Enrolled,
			Status:     status,
		}
	}

	if !hasAdmin {
		return nil, fmt.Errorf("users: directory must have at least one admin")
	}

	return dir, nil
}

func joinKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}
