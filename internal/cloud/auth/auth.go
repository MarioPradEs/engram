package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/store"
)

var ErrSecretTooShort = errors.New("jwt secret must be at least 32 bytes")
var ErrBearerTokenNotConfigured = errors.New("cloud bearer token is not configured")
var ErrInvalidDashboardSessionToken = errors.New("invalid dashboard session token")
var ErrProjectNotAllowed = errors.New("project is not allowed for this token")

type Service struct {
	store         *cloudstore.CloudStore
	expectedToken string
	dashboardAuth map[string]struct{}
	allowed       map[string]struct{}
	allowedAll    bool
	jwtSecret     []byte
	now           func() time.Time
}

type ProjectScopeAuthorizer struct {
	allowed    map[string]struct{}
	allowedAll bool
}

func NewService(store *cloudstore.CloudStore, jwtSecret string) (*Service, error) {
	if len(jwtSecret) < 32 {
		return nil, ErrSecretTooShort
	}
	return &Service{store: store, jwtSecret: []byte(jwtSecret), now: time.Now}, nil
}

func NewProjectScopeAuthorizer(projects []string) *ProjectScopeAuthorizer {
	a := &ProjectScopeAuthorizer{allowed: make(map[string]struct{})}
	a.SetAllowedProjects(projects)
	return a
}

type dashboardSessionClaims struct {
	TokenHash string `json:"token_hash"`
	Exp       int64  `json:"exp"`
	Iat       int64  `json:"iat"`
}

// serviceEnvelope returns the sessionEnvelope keyed by s.jwtSecret.
// Used by MintDashboardSession / ParseDashboardSession to share the one
// HMAC sign/verify code path with HeaderAuthenticator.
func (s *Service) serviceEnvelope() sessionEnvelope {
	return sessionEnvelope{secret: s.jwtSecret}
}

// MintDashboardSession returns a signed dashboard session token.
// The token is opaque to clients and validated by ParseDashboardSession.
func (s *Service) MintDashboardSession(bearerToken string) (string, error) {
	bearerToken = strings.TrimSpace(bearerToken)
	if bearerToken == "" {
		return "", fmt.Errorf("bearer token is required")
	}
	issuedAt := s.now().UTC()
	claims := dashboardSessionClaims{
		TokenHash: s.dashboardTokenHash(bearerToken),
		Iat:       issuedAt.Unix(),
		Exp:       issuedAt.Add(8 * time.Hour).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	return s.serviceEnvelope().seal(payload), nil
}

// ParseDashboardSession verifies and decodes a signed dashboard session token.
func (s *Service) ParseDashboardSession(sessionToken string) (string, error) {
	payload, err := s.serviceEnvelope().open(sessionToken)
	if err != nil {
		return "", err
	}
	var claims dashboardSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	if strings.TrimSpace(claims.TokenHash) == "" {
		return "", ErrInvalidDashboardSessionToken
	}
	if claims.Exp <= s.now().UTC().Unix() {
		return "", ErrInvalidDashboardSessionToken
	}
	expectedToken := strings.TrimSpace(s.expectedToken)
	if expectedToken == "" {
		return "", ErrBearerTokenNotConfigured
	}
	if hmac.Equal([]byte(claims.TokenHash), []byte(s.dashboardTokenHash(expectedToken))) {
		return expectedToken, nil
	}
	for token := range s.dashboardAuth {
		token = strings.TrimSpace(token)
		if token == "" || token == expectedToken {
			continue
		}
		if hmac.Equal([]byte(claims.TokenHash), []byte(s.dashboardTokenHash(token))) {
			return token, nil
		}
	}
	return "", ErrInvalidDashboardSessionToken
}

func (s *Service) sign(payloadPart string) []byte {
	mac := hmac.New(sha256.New, s.jwtSecret)
	_, _ = mac.Write([]byte(payloadPart))
	return mac.Sum(nil)
}

func (s *Service) dashboardTokenHash(token string) string {
	mac := hmac.New(sha256.New, s.jwtSecret)
	_, _ = mac.Write([]byte("dashboard:"))
	_, _ = mac.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) SetBearerToken(token string) {
	s.expectedToken = strings.TrimSpace(token)
}

func (s *Service) SetDashboardSessionTokens(tokens []string) {
	s.dashboardAuth = make(map[string]struct{})
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		s.dashboardAuth[token] = struct{}{}
	}
}

func (s *Service) SetAllowedProjects(projects []string) {
	s.allowed = make(map[string]struct{})
	s.allowedAll = false
	for _, project := range projects {
		if strings.TrimSpace(project) == "*" {
			s.allowedAll = true
			return
		}
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		s.allowed[normalized] = struct{}{}
	}
}

func (s *Service) AuthorizeProject(_ context.Context, project string) error {
	if s.allowedAll {
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			return fmt.Errorf("project is required")
		}
		return nil
	}
	return authorizeProjectAgainstAllowlist(project, s.allowed)
}

// EnrolledProjects returns the sorted list of projects that this Service is
// authorized to serve. Used by cloudserver's mutation pull to filter mutations
// to the caller's enrolled projects (REQ-202). ctx is accepted to satisfy the
// cloudserver.EnrolledProjectsProvider interface; Service ignores it since
// enrollment is statically configured (not request-scoped).
//
// When the wildcard "*" is configured, nil is returned to signal "no project
// filter" — callers must treat nil as "allow all" (matching the ListMutationsSince
// nil-means-all contract).
func (s *Service) EnrolledProjects(_ context.Context) []string {
	if s.allowedAll {
		return nil
	}
	return sortedAllowlist(s.allowed)
}

func (a *ProjectScopeAuthorizer) SetAllowedProjects(projects []string) {
	a.allowed = make(map[string]struct{})
	a.allowedAll = false
	for _, project := range projects {
		if strings.TrimSpace(project) == "*" {
			a.allowedAll = true
			return
		}
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		a.allowed[normalized] = struct{}{}
	}
}

func (a *ProjectScopeAuthorizer) AuthorizeProject(_ context.Context, project string) error {
	if a.allowedAll {
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			return fmt.Errorf("project is required")
		}
		return nil
	}
	return authorizeProjectAgainstAllowlist(project, a.allowed)
}

// EnrolledProjects returns the sorted list of projects this authorizer allows.
// ctx is accepted to satisfy the cloudserver.EnrolledProjectsProvider interface;
// ProjectScopeAuthorizer ignores it since enrollment is statically configured.
//
// When the wildcard "*" is configured, nil is returned to signal "no project
// filter" (matching the ListMutationsSince nil-means-all contract).
func (a *ProjectScopeAuthorizer) EnrolledProjects(_ context.Context) []string {
	if a.allowedAll {
		return nil
	}
	return sortedAllowlist(a.allowed)
}

// sortedAllowlist returns a sorted slice of the map keys.
// Isolated to one spot so both Service and ProjectScopeAuthorizer behave
// identically and tests can pin ordering.
func sortedAllowlist(allowed map[string]struct{}) []string {
	if len(allowed) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(allowed))
	for project := range allowed {
		out = append(out, project)
	}
	sort.Strings(out)
	return out
}

func authorizeProjectAgainstAllowlist(project string, allowed map[string]struct{}) error {
	if len(allowed) == 0 {
		return fmt.Errorf("cloud project allowlist is not configured")
	}
	normalized, _ := store.NormalizeProject(project)
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return fmt.Errorf("project is required")
	}
	if _, ok := allowed[normalized]; ok {
		return nil
	}
	return fmt.Errorf("%w", ErrProjectNotAllowed)
}

func (s *Service) Authorize(r *http.Request) (*http.Request, error) {
	if strings.TrimSpace(s.expectedToken) == "" {
		return r, ErrBearerTokenNotConfigured
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return r, fmt.Errorf("missing authorization header")
	}
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return r, fmt.Errorf("authorization must use Bearer token")
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return r, fmt.Errorf("authorization must use Bearer token")
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return r, fmt.Errorf("bearer token is required")
	}
	if !hmac.Equal([]byte(token), []byte(s.expectedToken)) {
		return r, fmt.Errorf("invalid bearer token")
	}
	return r, nil
}
