package cloudstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Unit tests: redactSecrets ────────────────────────────────────────────────

// TestRedactSecretsPositiveCases verifies that each supported secret type is
// detected and replaced with the [REDACTED-SECRET] placeholder, and that
// found == true is returned.
func TestRedactSecretsPositiveCases(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name: "pem_rsa_private_key",
			content: `Here is my key:
-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEA0Z3VS5JJcds3xHn/ygWep4PAtEsHADqeHhkVt2pcxQS4sM9h
ghHgSKD8fJU/HKGS6f3lHRV6A1RHvJmB0hAeXjKGJwYMJ5FxHQIDAQAB
-----END RSA PRIVATE KEY-----
done.`,
		},
		{
			name:    "pem_ec_private_key",
			content: "-----BEGIN EC PRIVATE KEY-----\nMHQCAQEEIK7o5V\n-----END EC PRIVATE KEY-----",
		},
		{
			name:    "aws_access_key_id",
			content: "My AWS key is AKIAIOSFODNN7EXAMPLE stored here",
		},
		{
			name:    "github_pat",
			content: "export GITHUB_TOKEN=ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		},
		{
			name:    "github_oauth_token",
			content: "token: gho_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		},
		{
			name:    "openai_key",
			content: "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuv123456",
		},
		{
			name:    "openai_proj_key",
			content: "sk-proj-abcdefghijklmnopqrstuv123456 is the project key",
		},
		{
			name:    "anthropic_key",
			content: "api_key = sk-ant-api03-abcdefghijklmnopqrstuvwxyz12",
		},
		{
			// AIza + exactly 35 url-safe alphanum/underscore/dash chars = 39 total
			name:    "google_api_key",
			content: "GOOGLE_KEY=AIzaSyD-random35charsXXXXXXXXXXXXXXXXXX",
		},
		{
			name:    "slack_bot_token",
			content: "SLACK_TOKEN=xoxb-EXAMPLE-NOT-A-REAL-SLACK-TOKEN-000",
		},
		{
			name:    "slack_app_token",
			content: "xoxp-EXAMPLE-NOT-A-REAL-SLACK-TOKEN-000 is here",
		},
		{
			name:    "jwt",
			content: "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyMSJ9.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
		},
		{
			name:    "url_with_credentials",
			content: "database: postgres://myuser:supersecretpassword@db.host.com:5432/mydb",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redacted, found := redactSecrets(tc.content)
			if !found {
				t.Errorf("expected found=true for %q, got false; content=%q", tc.name, tc.content)
			}
			if !strings.Contains(redacted, redactedPlaceholder) {
				t.Errorf("expected [REDACTED-SECRET] in output for %q, got: %q", tc.name, redacted)
			}
			// The original secret substring must not survive in the output.
			// We check that the output differs from the input.
			if redacted == tc.content {
				t.Errorf("output identical to input — secret was not redacted for %q", tc.name)
			}
		})
	}
}

// TestRedactSecretsNegativeCases verifies that ordinary natural-language text,
// normal technical prose, and benign code snippets do NOT trigger redaction.
// Each of these cases historically caused false positives in looser scan rules.
func TestRedactSecretsNegativeCases(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "bearer_auth_mention",
			content: "We use Bearer auth in the Authorization header.",
		},
		{
			name:    "akia_prefix_mention",
			content: "AWS access keys always start with the AKIA prefix.",
		},
		{
			name:    "sk_as_word_part",
			content: "The word 'task' contains sk as a suffix, not a secret.",
		},
		{
			name:    "sk_dash_short",
			content: "The flag -sk-v was deprecated in v2 of the CLI.",
		},
		{
			name:    "normal_url_no_password",
			content: "See https://docs.example.com/api for details.",
		},
		{
			name:    "normal_url_with_user_no_password",
			content: "Connect via ftp://anonymous@ftp.example.com",
		},
		{
			name:    "short_base64_not_jwt",
			content: "The token eyJhbGciOiJub25lIn0 is just a header without segments.",
		},
		{
			name:    "code_comment_with_sk",
			content: "// sk is the abbreviation for 'skip' in this context",
		},
		{
			name:    "github_mention_short",
			content: "Our GitHub org is at github.com/myorg — no token here.",
		},
		{
			name:    "slack_url_not_token",
			content: "Join us on xoxo-style messaging at slack.example.com",
		},
		{
			name:    "google_key_mention_no_value",
			content: "Google API keys start with AIza — never commit them.",
		},
		{
			name:    "anthropic_sk_mention",
			content: "Anthropic API keys look like sk-ant-... but this is just documentation.",
		},
		{
			name:    "jwt_like_but_only_two_segments",
			content: "The string eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyMSJ9 has only two segments.",
		},
		{
			name:    "aws_akia_too_short",
			content: "AKIAIOSFODNN is only 12 chars after AKIA — not a real key.",
		},
		{
			name:    "pem_public_key_not_private",
			content: "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkq\n-----END PUBLIC KEY-----",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redacted, found := redactSecrets(tc.content)
			if found {
				t.Errorf("false positive: found=true for %q; redacted=%q", tc.name, redacted)
			}
			if redacted != tc.content {
				t.Errorf("content changed despite found=false for %q: got %q", tc.name, redacted)
			}
		})
	}
}

// TestRedactSecretsInObservationPayloadContent verifies that
// redactSecretsInObservationPayload patches the "content" and "title" fields.
func TestRedactSecretsInObservationPayloadContent(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{
		"sync_id":    "test-obs",
		"session_id": "sess",
		"type":       "decision",
		"title":      "Config update AKIAIOSFODNN7EXAMPLE key",
		"content":    "Set AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE in env",
		"scope":      "project",
	})

	out, found := redactSecretsInObservationPayload(payload)
	if !found {
		t.Fatal("expected found=true")
	}

	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	if strings.Contains(m["content"].(string), "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret still present in content: %q", m["content"])
	}
	if !strings.Contains(m["content"].(string), redactedPlaceholder) {
		t.Errorf("placeholder missing from content: %q", m["content"])
	}
	if strings.Contains(m["title"].(string), "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret still present in title: %q", m["title"])
	}
	if !strings.Contains(m["title"].(string), redactedPlaceholder) {
		t.Errorf("placeholder missing from title: %q", m["title"])
	}
	// Other fields must be preserved unchanged.
	if m["scope"] != "project" {
		t.Errorf("scope field changed: %v", m["scope"])
	}
}

// TestRedactSecretsInObservationPayloadNoSecret verifies that a clean payload
// is returned unchanged with found=false.
func TestRedactSecretsInObservationPayloadNoSecret(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{
		"sync_id": "obs-clean",
		"content": "This is normal prose with no secrets.",
		"title":   "Normal title",
		"scope":   "project",
	})
	out, found := redactSecretsInObservationPayload(payload)
	if found {
		t.Errorf("false positive: found=true on clean payload")
	}
	if string(out) != string(payload) {
		t.Errorf("clean payload was modified")
	}
}

// ─── Integration tests (Postgres-gated) ──────────────────────────────────────
//
// Both tests use openTestDB (skips when CLOUDSTORE_TEST_DSN is not set) and
// verify that an observation containing a secret lands in cloud_mutations with
// the secret REDACTED — for BOTH the chunk path and the mutation-batch path.

// TestSecretScanChunkPath_RedactsBeforeWrite is a Postgres-gated integration test
// verifying that when WriteChunkWithAttribution processes a chunk that contains
// a secret in an observation's content field, the stored cloud_mutations row has
// [REDACTED-SECRET] instead of the real secret, and an audit log entry with
// outcome "redacted_secret" is written.
func TestSecretScanChunkPath_RedactsBeforeWrite(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	attr := Attribution{
		UserEmail:  "scanner@example.com",
		UserName:   "Scanner Test",
		Department: "security",
	}
	project := "scan-chunk-" + uniqueTestSuffix(t)

	// Observation whose content contains a real AWS access key.
	secretKey := "AKIAIOSFODNN7EXAMPLE" // 20 chars: AKIA + 16 uppercase
	rawChunk := `{
		"sessions":[{"id":"sess-scan-chunk","directory":"/tmp","started_at":"2026-06-05T10:00:00Z"}],
		"observations":[{
			"sync_id":"obs-with-secret",
			"session_id":"sess-scan-chunk",
			"type":"decision",
			"title":"AWS config",
			"content":"Set AWS_ACCESS_KEY_ID=` + secretKey + ` in your environment",
			"scope":"project",
			"created_at":"2026-06-05T10:01:00Z"
		}],
		"prompts":[]
	}`

	chunkPayload, err := chunkcodec.CanonicalizeForProject([]byte(rawChunk), project)
	if err != nil {
		t.Fatalf("canonicalize chunk: %v", err)
	}
	chunkID := chunkIDFromPayload(chunkPayload)

	if err := cs.WriteChunkWithAttribution(ctx, project, chunkID, "scanner", "", chunkPayload, attr); err != nil {
		t.Fatalf("WriteChunkWithAttribution: %v", err)
	}

	// Assert: the mutation is present but with [REDACTED-SECRET] in the payload.
	var storedPayloadStr string
	err = cs.db.QueryRowContext(ctx, `
		SELECT payload::text FROM cloud_mutations
		WHERE project = $1 AND entity = 'observation' AND entity_key = 'obs-with-secret'
	`, project).Scan(&storedPayloadStr)
	if err != nil {
		t.Fatalf("query stored observation: %v", err)
	}
	if strings.Contains(storedPayloadStr, secretKey) {
		t.Errorf("secret key %q must not be stored in cloud_mutations; payload=%q", secretKey, storedPayloadStr)
	}
	if !strings.Contains(storedPayloadStr, redactedPlaceholder) {
		t.Errorf("placeholder %q must appear in stored payload; payload=%q", redactedPlaceholder, storedPayloadStr)
	}

	// Assert: audit log entry for redacted_secret.
	var auditCount int
	if err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_sync_audit_log
		WHERE contributor = $1 AND outcome = 'redacted_secret' AND project = $2
	`, attr.UserEmail, project).Scan(&auditCount); err != nil {
		t.Fatalf("count redacted_secret audit: %v", err)
	}
	if auditCount < 1 {
		t.Errorf("expected audit log entry with outcome=redacted_secret, got %d", auditCount)
	}
}

// TestSecretScanMutationBatchPath_RedactsBeforeWrite is a Postgres-gated integration
// test verifying that InsertMutationBatch redacts secrets from observation payloads
// before inserting into cloud_mutations, and logs the redaction in the audit log.
func TestSecretScanMutationBatchPath_RedactsBeforeWrite(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	attr := Attribution{
		UserEmail:  "scanner-batch@example.com",
		UserName:   "Scanner Batch Test",
		Department: "security",
	}
	project := "scan-batch-" + uniqueTestSuffix(t)

	secretKey := "AKIAIOSFODNN7EXAMPLE"
	payload, _ := json.Marshal(map[string]interface{}{
		"sync_id":    "obs-batch-secret",
		"session_id": "sess-scan-batch",
		"type":       "decision",
		"title":      "Batch config",
		"content":    "export AWS_KEY=" + secretKey,
		"scope":      "project",
		"created_at": "2026-06-05T10:00:00Z",
	})
	entry := MutationEntry{
		Project:   project,
		Entity:    store.SyncEntityObservation,
		EntityKey: "obs-batch-secret",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(payload),
	}

	seqs, err := cs.InsertMutationBatch(ctx, []MutationEntry{entry}, attr)
	if err != nil {
		t.Fatalf("InsertMutationBatch: %v", err)
	}
	if len(seqs) != 1 || seqs[0] < 0 {
		t.Fatalf("expected 1 positive seq, got %v", seqs)
	}

	// Assert: stored payload does NOT contain the secret.
	var storedPayloadStr string
	if err := cs.db.QueryRowContext(ctx, `
		SELECT payload::text FROM cloud_mutations WHERE seq = $1
	`, seqs[0]).Scan(&storedPayloadStr); err != nil {
		t.Fatalf("query stored mutation: %v", err)
	}
	if strings.Contains(storedPayloadStr, secretKey) {
		t.Errorf("secret %q must not be stored in cloud_mutations; payload=%q", secretKey, storedPayloadStr)
	}
	if !strings.Contains(storedPayloadStr, redactedPlaceholder) {
		t.Errorf("placeholder must appear in stored payload; payload=%q", storedPayloadStr)
	}

	// Assert: audit log entry for redacted_secret.
	var auditCount int
	if err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_sync_audit_log
		WHERE contributor = $1 AND outcome = 'redacted_secret' AND project = $2
	`, attr.UserEmail, project).Scan(&auditCount); err != nil {
		t.Fatalf("count redacted_secret audit: %v", err)
	}
	if auditCount < 1 {
		t.Errorf("expected audit log entry with outcome=redacted_secret, got %d", auditCount)
	}
}

// TestSecretScanDoesNotRedactCleanObservation verifies the happy path: an
// observation with no secrets is stored unchanged (no redaction, no audit entry).
// Exercises the InsertMutationBatch path.
func TestSecretScanDoesNotRedactCleanObservation(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	attr := Attribution{
		UserEmail:  "scanner-clean@example.com",
		UserName:   "Scanner Clean",
		Department: "security",
	}
	project := "scan-clean-" + uniqueTestSuffix(t)

	originalContent := "This is a normal observation with no secrets at all."
	payload, _ := json.Marshal(map[string]interface{}{
		"sync_id":    "obs-clean-scan",
		"session_id": "sess-clean-scan",
		"type":       "manual",
		"title":      "Normal note",
		"content":    originalContent,
		"scope":      "project",
		"created_at": "2026-06-05T10:00:00Z",
	})
	entry := MutationEntry{
		Project:   project,
		Entity:    store.SyncEntityObservation,
		EntityKey: "obs-clean-scan",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(payload),
	}

	seqs, err := cs.InsertMutationBatch(ctx, []MutationEntry{entry}, attr)
	if err != nil {
		t.Fatalf("InsertMutationBatch: %v", err)
	}

	var storedPayloadStr string
	if err := cs.db.QueryRowContext(ctx, `
		SELECT payload::text FROM cloud_mutations WHERE seq = $1
	`, seqs[0]).Scan(&storedPayloadStr); err != nil {
		t.Fatalf("query stored mutation: %v", err)
	}

	// Content must be stored verbatim — no placeholder injected.
	if strings.Contains(storedPayloadStr, redactedPlaceholder) {
		t.Errorf("clean observation must not have placeholder; payload=%q", storedPayloadStr)
	}
	if !strings.Contains(storedPayloadStr, originalContent) {
		t.Errorf("original content must survive unchanged; payload=%q", storedPayloadStr)
	}

	// No redacted_secret audit entry for this contributor+project.
	var auditCount int
	if err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_sync_audit_log
		WHERE contributor = $1 AND outcome = 'redacted_secret' AND project = $2
	`, attr.UserEmail, project).Scan(&auditCount); err != nil {
		t.Fatalf("count redacted_secret audit: %v", err)
	}
	if auditCount != 0 {
		t.Errorf("expected 0 redacted_secret audit entries for clean obs, got %d", auditCount)
	}
}
