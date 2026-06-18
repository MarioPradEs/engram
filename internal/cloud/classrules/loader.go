package classrules

import "sync"

// ClassrulesLoader is a thread-safe, hot-reloadable holder for a *Config loaded
// from classification-rules.yaml.
//
// It mirrors the users.YAMLLoader design: Reload() performs an atomic pointer
// swap; Current() returns the live pointer under a read lock; last-good
// retention means a failed Reload() leaves the previous valid config in place.
//
// The caller owns only two entry points:
//   - NewClassrulesLoader(path) — initialises and performs the first load.
//   - loader.Reload()           — re-reads the file; non-fatal on absent/parse error.
//   - loader.Current()          — returns the current *Config (may be nil for absent file).
type ClassrulesLoader struct {
	path    string
	mu      sync.RWMutex
	current *Config
}

// NewClassrulesLoader constructs a ClassrulesLoader and performs the initial
// load from path. If the file is absent, current is nil (graceful). If the
// file is present but invalid, an error is returned.
func NewClassrulesLoader(path string) (*ClassrulesLoader, error) {
	l := &ClassrulesLoader{path: path}
	cfg, err := LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	l.current = cfg
	return l, nil
}

// Reload re-reads classification-rules.yaml from disk and atomically swaps
// the in-memory config. On parse failure the previous valid config is retained
// (last-good retention). Absent file is treated as nil config, not an error.
func (l *ClassrulesLoader) Reload() error {
	cfg, err := LoadFromFile(l.path)
	if err != nil {
		// Last-good retained — do not update l.current.
		return err
	}
	l.mu.Lock()
	l.current = cfg
	l.mu.Unlock()
	return nil
}

// Current returns the current *Config under a read lock. The returned pointer
// is the live internal pointer — callers MUST NOT modify the pointed-at struct
// (treat it as immutable). Returns nil when the file is absent or was never
// loaded successfully.
func (l *ClassrulesLoader) Current() *Config {
	l.mu.RLock()
	cfg := l.current
	l.mu.RUnlock()
	if cfg == nil {
		return nil
	}
	// Return a shallow copy so caller mutations to top-level fields (e.g.
	// appending to cfg.Games) do not corrupt the internal state.
	// Deep slice copy for Games since that is the writable field in tests.
	copy := *cfg
	if cfg.Games != nil {
		gamesCopy := make([]string, len(cfg.Games))
		copySlice(gamesCopy, cfg.Games)
		copy.Games = gamesCopy
	}
	return &copy
}

// copySlice copies src into dst.
func copySlice(dst, src []string) {
	for i, s := range src {
		dst[i] = s
	}
}
