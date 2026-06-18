package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// cmdGames is the entry point for the `engram games` command.
// Currently supports the `sync` subcommand.
func cmdGames(cfg store.Config) {
	if len(os.Args) < 3 || os.Args[2] == "--help" || os.Args[2] == "-h" || os.Args[2] == "help" {
		fmt.Println("usage: engram games <subcommand>")
		fmt.Println("supported subcommands: sync")
		return
	}

	switch os.Args[2] {
	case "sync":
		cmdGamesSync(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown games command: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, "supported subcommands: sync")
		exitFunc(1)
	}
}

// cmdGamesSync implements `engram games sync`.
// Fetches the canonical games vocabulary from the cloud server and writes it
// to the local classification-rules.yaml. The file is updated atomically so
// all other sections (departments, conventions, rules) are preserved.
// On success, prints a concise line: "synced N games".
func cmdGamesSync(cfg store.Config) {
	// Resolve cloud runtime config (server URL + token).
	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read cloud config: %v\n", err)
		exitFunc(1)
		return
	}
	if cc == nil || strings.TrimSpace(cc.ServerURL) == "" {
		fmt.Fprintln(os.Stderr, "error: cloud server is not configured — run `engram cloud config --server <url>` first")
		exitFunc(1)
		return
	}
	serverURL := strings.TrimSuffix(strings.TrimSpace(cc.ServerURL), "/")
	token := strings.TrimSpace(cc.Token)

	// Resolve local classrules path from environment.
	classrulesPath := strings.TrimSpace(os.Getenv("ENGRAM_CLASSIFICATION_RULES"))
	if classrulesPath == "" {
		fmt.Fprintln(os.Stderr, "error: ENGRAM_CLASSIFICATION_RULES is not set — cannot sync games vocabulary")
		exitFunc(1)
		return
	}

	n, err := syncGames(serverURL, token, classrulesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: games sync failed: %v\n", err)
		exitFunc(1)
		return
	}
	fmt.Printf("synced %d games\n", n)
}

// syncGames fetches the canonical games vocabulary from serverURL/classrules/games,
// writes the result to classrulesPath using classrules.WriteGames (which preserves
// all non-games sections), and returns the count of synced games.
//
// The function is extracted from cmdGamesSync so it can be tested against an
// httptest.Server without needing to wire environment variables or the full
// CLI dispatch chain.
func syncGames(serverURL, token, classrulesPath string) (int, error) {
	url := strings.TrimSuffix(serverURL, "/") + "/classrules/games"

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("syncGames: build request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("syncGames: fetch games: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("syncGames: server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Games []string `json:"games"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload); err != nil {
		return 0, fmt.Errorf("syncGames: decode response: %w", err)
	}

	// WriteGames validates (non-empty, no duplicates), preserves other sections,
	// and performs an atomic rename. Pass nil loader — no in-process reload in
	// the CLI context; the refreshed file is picked up on the next MCP start.
	if err := classrules.WriteGames(classrulesPath, nil, payload.Games); err != nil {
		return 0, fmt.Errorf("syncGames: write games: %w", err)
	}

	return len(payload.Games), nil
}
