// Package state manages persistent state stores for the AgentClaw pipeline.
//
// ScopeStore tracks the scope manifest for each agent — what it owns,
// what it must not touch, what it depends on, and what it interfaces with.
// Each manifest is stored as an atomic JSON file under state/scope/.
//
// Directory layout (relative to stateBaseDir):
//
//	scope/
//	  <agent-id>.json  — ScopeManifest per agent (written atomically)
//
// Constraints:
//   - No external dependencies: only encoding/json, errors, io/fs, os, regexp, sort, sync.
//   - Each agent file is written atomically: write to <id>.tmp.json, then os.Rename.
//   - A single sync.RWMutex guards the in-memory cache.
//   - AgentID must match ^[a-zA-Z0-9_-]+$ — validated before any path construction.
//   - UpdateFocus holds the lock across the full read-modify-write cycle (no TOCTOU).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// agentIDRe is the allowlist for agent IDs used in file-system paths.
var agentIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ScopeManifest describes the operational scope of a single agent.
type ScopeManifest struct {
	AgentID        string            `json:"agent_id"`
	Owns           []string          `json:"owns"`            // files / packages this agent is responsible for
	DependsOn      []string          `json:"depends_on"`      // agent IDs this agent waits for
	MustNotTouch   []string          `json:"must_not_touch"`  // paths / packages this agent must not modify
	InterfacesWith map[string]string `json:"interfaces_with"` // agent-id → description of the interface
	CurrentFocus   string            `json:"current_focus"`   // short description of what the agent is working on now
	BlockedBy      string            `json:"blocked_by"`      // agent-id or reason if currently blocked, else ""
	UpdatedAt      time.Time         `json:"updated_at"`
}

// ScopeStore is the file-backed agent scope manifest store.
// All exported methods are safe for concurrent use.
type ScopeStore struct {
	dir   string // absolute path to state/scope/
	mu    sync.RWMutex
	cache map[string]ScopeManifest // in-memory cache keyed by AgentID
}

// NewScopeStore creates a ScopeStore rooted at stateBaseDir/scope/.
// It creates the directory if it does not exist.
func NewScopeStore(stateBaseDir string) (*ScopeStore, error) {
	dir := filepath.Join(stateBaseDir, "scope")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("state: create scope dir: %w", err)
	}
	return &ScopeStore{
		dir:   dir,
		cache: make(map[string]ScopeManifest),
	}, nil
}

// validateAgentID returns an error if id contains characters that could be
// used to escape the scope directory via path traversal.
func validateAgentID(id string) error {
	if id == "" {
		return fmt.Errorf("state: scope: agent_id must not be empty")
	}
	if !agentIDRe.MatchString(id) {
		return fmt.Errorf("state: scope: agent_id %q contains invalid characters (allowed: a-z A-Z 0-9 _ -)", id)
	}
	return nil
}

// ─── Write ────────────────────────────────────────────────────────────────────

// Write atomically persists the manifest to scope/<agent-id>.json and
// updates the in-memory cache. UpdatedAt is set to time.Now().
func (ss *ScopeStore) Write(m ScopeManifest) error {
	if err := validateAgentID(m.AgentID); err != nil {
		return err
	}
	m.UpdatedAt = time.Now()

	// Marshal outside the lock — CPU-bound work should not block readers.
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("state: scope: marshal manifest for %q: %w", m.AgentID, err)
	}

	// Hold the lock across file I/O + cache update to prevent TOCTOU
	// when concurrent Write() calls target the same AgentID (#58).
	ss.mu.Lock()
	defer ss.mu.Unlock()

	tmp := filepath.Join(ss.dir, m.AgentID+".tmp.json")
	dst := filepath.Join(ss.dir, m.AgentID+".json")

	if err := os.WriteFile(tmp, data, 0640); err != nil {
		return fmt.Errorf("state: scope: write tmp file for %q: %w", m.AgentID, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		// Best-effort cleanup of orphaned tmp file.
		_ = os.Remove(tmp)
		return fmt.Errorf("state: scope: rename tmp file for %q: %w", m.AgentID, err)
	}

	ss.cache[m.AgentID] = m

	return nil
}

// ─── Read ─────────────────────────────────────────────────────────────────────

// Read returns the ScopeManifest for agentID.
// It checks the in-memory cache first; on miss it reads from disk.
// Returns an error if the manifest does not exist.
func (ss *ScopeStore) Read(agentID string) (*ScopeManifest, error) {
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}

	ss.mu.RLock()
	if m, ok := ss.cache[agentID]; ok {
		ss.mu.RUnlock()
		cp := m
		return &cp, nil
	}
	ss.mu.RUnlock()

	// Cache miss — load from disk.
	path := filepath.Join(ss.dir, agentID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("state: scope: manifest for %q not found", agentID)
		}
		return nil, fmt.Errorf("state: scope: read file for %q: %w", agentID, err)
	}

	var m ScopeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("state: scope: parse manifest for %q: %w", agentID, err)
	}

	ss.mu.Lock()
	ss.cache[agentID] = m
	ss.mu.Unlock()

	return &m, nil
}

// ─── ReadAll ──────────────────────────────────────────────────────────────────

// ReadAll returns every ScopeManifest currently on disk, sorted by AgentID.
func (ss *ScopeStore) ReadAll() ([]ScopeManifest, error) {
	entries, err := os.ReadDir(ss.dir)
	if err != nil {
		return nil, fmt.Errorf("state: scope: read dir: %w", err)
	}

	var manifests []ScopeManifest
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Only process <agent-id>.json; skip .tmp.json and any other files.
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp.json") {
			continue
		}

		agentID := name[:len(name)-5] // strip ".json"
		if err := validateAgentID(agentID); err != nil {
			// Skip files with unexpected names (e.g. left-over tooling files).
			continue
		}

		ss.mu.RLock()
		m, ok := ss.cache[agentID]
		ss.mu.RUnlock()

		if ok {
			manifests = append(manifests, m)
			continue
		}

		// Not in cache — read from disk.
		path := filepath.Join(ss.dir, name)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, fmt.Errorf("state: scope: read file %q: %w", name, rerr)
		}
		if jerr := json.Unmarshal(data, &m); jerr != nil {
			return nil, fmt.Errorf("state: scope: parse file %q: %w", name, jerr)
		}

		ss.mu.Lock()
		ss.cache[agentID] = m
		ss.mu.Unlock()

		manifests = append(manifests, m)
	}

	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].AgentID < manifests[j].AgentID
	})

	return manifests, nil
}

// ─── UpdateFocus ──────────────────────────────────────────────────────────────

// UpdateFocus updates the CurrentFocus and BlockedBy fields of an existing
// manifest and re-writes it atomically. The entire read-modify-write is
// performed under mu to prevent TOCTOU races with concurrent Write calls.
// Returns an error if the manifest for agentID does not exist yet.
func (ss *ScopeStore) UpdateFocus(agentID, focus, blockedBy string) error {
	if err := validateAgentID(agentID); err != nil {
		return err
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Read from cache or disk while holding the write lock.
	m, ok := ss.cache[agentID]
	if !ok {
		path := filepath.Join(ss.dir, agentID+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("state: scope: manifest for %q not found", agentID)
			}
			return fmt.Errorf("state: scope: read file for %q: %w", agentID, err)
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("state: scope: parse manifest for %q: %w", agentID, err)
		}
	}

	m.CurrentFocus = focus
	m.BlockedBy = blockedBy
	m.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("state: scope: marshal manifest for %q: %w", agentID, err)
	}

	tmp := filepath.Join(ss.dir, agentID+".tmp.json")
	dst := filepath.Join(ss.dir, agentID+".json")

	if err := os.WriteFile(tmp, data, 0640); err != nil {
		return fmt.Errorf("state: scope: write tmp file for %q: %w", agentID, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("state: scope: rename tmp file for %q: %w", agentID, err)
	}

	ss.cache[agentID] = m
	return nil
}
