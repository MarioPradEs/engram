package obsidian

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func strPtr(s string) *string { return &s }

func TestObservationToMarkdown(t *testing.T) {
	t.Run("all fields populated — frontmatter, body, and wikilinks", func(t *testing.T) {
		topicKey := "auth/jwt"
		sessionID := "abc123"
		project := "eng"
		obs := store.Observation{
			ID:            1,
			Type:          "bugfix",
			Title:         "Fixed the bug",
			Content:       "The fix was simple.",
			Project:       &project,
			Scope:         "project",
			TopicKey:      &topicKey,
			SessionID:     sessionID,
			RevisionCount: 2,
			CreatedAt:     "2026-01-01T10:00:00Z",
			UpdatedAt:     "2026-01-02T10:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		// Frontmatter block must open and close with ---
		if !strings.HasPrefix(got, "---\n") {
			t.Errorf("expected markdown to start with YAML frontmatter delimiter, got: %q", got[:min(len(got), 20)])
		}

		// All required frontmatter keys must be present
		for _, key := range []string{"id:", "type:", "project:", "scope:", "topic_key:", "session_id:", "created_at:", "updated_at:", "revision_count:"} {
			if !strings.Contains(got, key) {
				t.Errorf("frontmatter missing key %q", key)
			}
		}

		// Specific frontmatter values
		if !strings.Contains(got, "type: bugfix") {
			t.Errorf("frontmatter must contain 'type: bugfix'")
		}
		if !strings.Contains(got, "topic_key: auth/jwt") {
			t.Errorf("frontmatter must contain 'topic_key: auth/jwt'")
		}
		if !strings.Contains(got, "session_id: abc123") {
			t.Errorf("frontmatter must contain 'session_id: abc123'")
		}
		if !strings.Contains(got, "revision_count: 2") {
			t.Errorf("frontmatter must contain 'revision_count: 2'")
		}

		// Title as H1 heading
		if !strings.Contains(got, "# Fixed the bug") {
			t.Errorf("expected H1 heading with title, not found in output")
		}

		// Content body
		if !strings.Contains(got, "The fix was simple.") {
			t.Errorf("expected content body in output")
		}

		// Session wikilink
		if !strings.Contains(got, "[[session-abc123]]") {
			t.Errorf("expected session wikilink [[session-abc123]], not found")
		}

		// Topic wikilink — prefix = "auth" (first segment of "auth/jwt")
		if !strings.Contains(got, "[[topic-auth]]") {
			t.Errorf("expected topic wikilink [[topic-auth]], not found")
		}
	})

	t.Run("no topic_key — topic_key empty in frontmatter, no topic wikilink", func(t *testing.T) {
		project := "eng"
		obs := store.Observation{
			ID:        2,
			Type:      "decision",
			Title:     "Chose SQLite",
			Content:   "SQLite is the right choice.",
			Project:   &project,
			Scope:     "project",
			TopicKey:  nil,
			SessionID: "sess-001",
			CreatedAt: "2026-02-01T00:00:00Z",
			UpdatedAt: "2026-02-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		// topic_key must appear empty in frontmatter
		if !strings.Contains(got, `topic_key: ""`) {
			t.Errorf("expected 'topic_key: \"\"' in frontmatter for nil topic_key, got output:\n%s", got)
		}

		// No topic wikilink should be emitted
		if strings.Contains(got, "[[topic-") {
			t.Errorf("expected no topic wikilink when topic_key is nil, but found one")
		}

		// Session wikilink must still be present
		if !strings.Contains(got, "[[session-sess-001]]") {
			t.Errorf("expected session wikilink [[session-sess-001]]")
		}
	})

	t.Run("no session_id — no session wikilink", func(t *testing.T) {
		topicKey := "arch/db"
		project := "core"
		obs := store.Observation{
			ID:        3,
			Type:      "architecture",
			Title:     "DB Schema decision",
			Content:   "We chose normalized schema.",
			Project:   &project,
			Scope:     "project",
			TopicKey:  &topicKey,
			SessionID: "",
			CreatedAt: "2026-03-01T00:00:00Z",
			UpdatedAt: "2026-03-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		// No session wikilink when session_id is empty
		if strings.Contains(got, "[[session-]]") {
			t.Errorf("expected no empty session wikilink, but found [[session-]]")
		}

		// Topic wikilink must still be present — prefix = "arch"
		if !strings.Contains(got, "[[topic-arch]]") {
			t.Errorf("expected topic wikilink [[topic-arch]]")
		}
	})

	t.Run("multi-segment topic_key — prefix uses last slash part", func(t *testing.T) {
		topicKey := "sdd/obsidian-plugin/explore"
		project := "engram"
		obs := store.Observation{
			ID:        4,
			Type:      "architecture",
			Title:     "SDD Explore",
			Content:   "Content here.",
			Project:   &project,
			Scope:     "project",
			TopicKey:  &topicKey,
			SessionID: "s-99",
			CreatedAt: "2026-04-01T00:00:00Z",
			UpdatedAt: "2026-04-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		// Design says: wikilink prefix = topic_key split on LAST "/"
		// "sdd/obsidian-plugin/explore" → last segment = "explore"
		// But design also says prefix for _topics/ uses -- instead of /
		// Looking at design section 4: [[topic-sdd--obsidian-plugin]] where prefix = topic_key split on last "/"
		// Re-reading: "[[topic-{prefix}]] where prefix = topic_key split on last '/'"
		// For "sdd/obsidian-plugin/explore" → split on last "/" → prefix = "sdd/obsidian-plugin"
		// In the wikilink, "/" → "--" → [[topic-sdd--obsidian-plugin]]
		if !strings.Contains(got, "[[topic-sdd--obsidian-plugin]]") {
			t.Errorf("expected topic wikilink [[topic-sdd--obsidian-plugin]] for topic_key=%q, got:\n%s", topicKey, got)
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Brain-Graph-View Slice A — A1: frontmatter additions ────────────────────

// TestFrontmatterJuego covers A1.1 and A1.2.
func TestFrontmatterJuego(t *testing.T) {
	t.Run("A1.1: observation with juego tag emits juego in frontmatter", func(t *testing.T) {
		project := "general"
		obs := store.Observation{
			ID:        100,
			Type:      "manual",
			Title:     "Juego test",
			Content:   "content",
			Project:   strPtr(project),
			Scope:     "project",
			SessionID: "sess-x",
			Tags:      map[string]string{"juego": "spark"},
			CreatedAt: "2026-06-01T00:00:00Z",
			UpdatedAt: "2026-06-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		if !strings.Contains(got, "juego: spark") {
			t.Errorf("expected 'juego: spark' in frontmatter, got:\n%s", got)
		}
	})

	t.Run("A1.2: observation with no juego tag omits juego key entirely", func(t *testing.T) {
		project := "general"
		obs := store.Observation{
			ID:        101,
			Type:      "manual",
			Title:     "No juego",
			Content:   "content",
			Project:   strPtr(project),
			Scope:     "project",
			SessionID: "sess-y",
			Tags:      nil, // no tags at all
			CreatedAt: "2026-06-01T00:00:00Z",
			UpdatedAt: "2026-06-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		if strings.Contains(got, "juego:") {
			t.Errorf("expected no 'juego:' key when juego tag absent, got:\n%s", got)
		}
	})

	t.Run("A1.2b: observation with empty juego tag omits juego key", func(t *testing.T) {
		project := "general"
		obs := store.Observation{
			ID:        102,
			Type:      "manual",
			Title:     "Empty juego",
			Content:   "content",
			Project:   strPtr(project),
			Scope:     "project",
			SessionID: "sess-z",
			Tags:      map[string]string{"juego": ""},
			CreatedAt: "2026-06-01T00:00:00Z",
			UpdatedAt: "2026-06-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		if strings.Contains(got, "juego:") {
			t.Errorf("expected no 'juego:' key when juego tag is empty string, got:\n%s", got)
		}
	})
}

// TestFrontmatterCreatedByDepartment covers A1.3 and A1.4.
func TestFrontmatterCreatedByDepartment(t *testing.T) {
	t.Run("A1.3: observation with UserEmail and Department emits both in frontmatter", func(t *testing.T) {
		project := "general"
		obs := store.Observation{
			ID:         103,
			Type:       "manual",
			Title:      "Attributed obs",
			Content:    "content",
			Project:    strPtr(project),
			Scope:      "project",
			SessionID:  "sess-attr",
			UserEmail:  "alice@vivastudios.com",
			Department: "dev",
			CreatedAt:  "2026-06-01T00:00:00Z",
			UpdatedAt:  "2026-06-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		if !strings.Contains(got, "created_by: alice@vivastudios.com") {
			t.Errorf("expected 'created_by: alice@vivastudios.com' in frontmatter, got:\n%s", got)
		}
		if !strings.Contains(got, "department: dev") {
			t.Errorf("expected 'department: dev' in frontmatter, got:\n%s", got)
		}
	})

	t.Run("A1.4: legacy observation with empty UserEmail omits created_by and department", func(t *testing.T) {
		project := "general"
		obs := store.Observation{
			ID:         104,
			Type:       "manual",
			Title:      "Legacy obs",
			Content:    "content",
			Project:    strPtr(project),
			Scope:      "project",
			SessionID:  "sess-legacy",
			UserEmail:  "", // legacy — no attribution
			Department: "",
			CreatedAt:  "2026-06-01T00:00:00Z",
			UpdatedAt:  "2026-06-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		if strings.Contains(got, "created_by:") {
			t.Errorf("expected no 'created_by:' key for legacy obs, got:\n%s", got)
		}
		if strings.Contains(got, "department:") {
			t.Errorf("expected no 'department:' key for legacy obs, got:\n%s", got)
		}
	})
}

// TestFrontmatterSyncID covers A1.5.
func TestFrontmatterSyncID(t *testing.T) {
	t.Run("A1.5a: non-empty SyncID is written to frontmatter", func(t *testing.T) {
		project := "general"
		obs := store.Observation{
			ID:        105,
			SyncID:    "obs-aabbcc112233",
			Type:      "manual",
			Title:     "Sync obs",
			Content:   "content",
			Project:   strPtr(project),
			Scope:     "project",
			SessionID: "sess-sync",
			CreatedAt: "2026-06-01T00:00:00Z",
			UpdatedAt: "2026-06-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		if !strings.Contains(got, "sync_id: obs-aabbcc112233") {
			t.Errorf("expected 'sync_id: obs-aabbcc112233' in frontmatter, got:\n%s", got)
		}
	})

	t.Run("A1.5b: empty SyncID omits sync_id key", func(t *testing.T) {
		project := "general"
		obs := store.Observation{
			ID:        106,
			SyncID:    "", // empty
			Type:      "manual",
			Title:     "No sync id",
			Content:   "content",
			Project:   strPtr(project),
			Scope:     "project",
			SessionID: "sess-nosync",
			CreatedAt: "2026-06-01T00:00:00Z",
			UpdatedAt: "2026-06-01T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		if strings.Contains(got, "sync_id:") {
			t.Errorf("expected no 'sync_id:' key when SyncID empty, got:\n%s", got)
		}
	})
}

// TestFrontmatterExistingFieldsUnchanged covers A1.6.
func TestFrontmatterExistingFieldsUnchanged(t *testing.T) {
	t.Run("A1.6: existing frontmatter fields unchanged when new fields added", func(t *testing.T) {
		topicKey := "auth/jwt"
		project := "general"
		obs := store.Observation{
			ID:            107,
			SyncID:        "obs-existing",
			Type:          "bugfix",
			Title:         "Preserve existing",
			Content:       "The fix.",
			Project:       strPtr(project),
			Scope:         "project",
			TopicKey:      &topicKey,
			SessionID:     "sess-preserve",
			RevisionCount: 3,
			Tags:          map[string]string{"juego": "tower-battle"},
			UserEmail:     "bob@vivastudios.com",
			Department:    "qa",
			CreatedAt:     "2026-05-01T00:00:00Z",
			UpdatedAt:     "2026-05-02T00:00:00Z",
		}

		got := ObservationToMarkdown(obs)

		// Verify all original fields still present
		for _, want := range []string{
			"id: 107",
			"type: bugfix",
			"project: general",
			"scope: project",
			"topic_key: auth/jwt",
			"session_id: sess-preserve",
			"revision_count: 3",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("expected existing field %q still present, got:\n%s", want, got)
			}
		}

		// And verify new fields also present
		if !strings.Contains(got, "juego: tower-battle") {
			t.Errorf("expected 'juego: tower-battle' in frontmatter")
		}
		if !strings.Contains(got, "created_by: bob@vivastudios.com") {
			t.Errorf("expected 'created_by: bob@vivastudios.com' in frontmatter")
		}
		if !strings.Contains(got, "department: qa") {
			t.Errorf("expected 'department: qa' in frontmatter")
		}
		if !strings.Contains(got, "sync_id: obs-existing") {
			t.Errorf("expected 'sync_id: obs-existing' in frontmatter")
		}
	})
}
