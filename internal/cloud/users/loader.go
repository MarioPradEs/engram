// Package users provides a YAML-backed user directory loader with live-reload
// support and last-good-state retention.
//
// Usage:
//
//	loader, err := users.NewYAMLLoader("/etc/engram/users.yaml")
//	p, ok := loader.Lookup("alice@vivastudios.com")
//
// On SIGHUP the process should call loader.Reload() to pick up changes.
// If the new file is invalid the previous directory remains active.
package users

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

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

var validDepartments = map[string]bool{
	"engineering": true,
	"product":     true,
	"design":      true,
	"marketing":   true,
	"sales":       true,
	"operations":  true,
	"finance":     true,
	"hr":          true,
	"legal":       true,
	"leadership":  true,
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
