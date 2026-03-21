package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check: StateStore implements port.StateStore.
var _ port.StateStore = (*StateStore)(nil)

// validAgentID matches only safe agent ID characters (alphanumeric, hyphens, underscores, dots).
var validAgentID = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// StateStore persists per-agent runtime state as JSON files on disk.
type StateStore struct {
	mu      sync.Mutex
	baseDir string
	cache   map[string]port.AgentState
}

// NewStateStore creates a StateStore rooted at baseDir/agents/.
func NewStateStore(baseDir string) (*StateStore, error) {
	dir := filepath.Join(baseDir, "agents")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("state: mkdir %s: %w", dir, err)
	}
	return &StateStore{
		baseDir: dir,
		cache:   make(map[string]port.AgentState),
	}, nil
}

// validateAgentID ensures the agentID is safe for use in file paths.
func validateAgentID(agentID string) error {
	if agentID == "" || !validAgentID.MatchString(agentID) {
		return fmt.Errorf("state: invalid agent ID: %q", agentID)
	}
	return nil
}

// WriteState persists the agent state to disk and updates the in-memory cache.
// The entire operation is serialized to prevent concurrent file corruption.
func (s *StateStore) WriteState(agentID string, state port.AgentState) error {
	if err := validateAgentID(agentID); err != nil {
		return err
	}

	state.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal %s: %w", agentID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Write to temp file and rename for atomicity.
	path := filepath.Join(s.baseDir, agentID+".json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("state: write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("state: rename %s: %w", path, err)
	}

	s.cache[agentID] = state
	return nil
}

// ReadState returns the agent state from the in-memory cache, falling back to disk.
func (s *StateStore) ReadState(agentID string) (*port.AgentState, error) {
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.cache[agentID]; ok {
		return &cached, nil
	}

	path := filepath.Join(s.baseDir, agentID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}

	var state port.AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("state: unmarshal %s: %w", path, err)
	}

	s.cache[agentID] = state
	return &state, nil
}

// ReadAllStates returns all agent states by scanning the agents directory.
// Uses readStateLocked to avoid re-acquiring the mutex per file.
func (s *StateStore) ReadAllStates() ([]port.AgentState, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("state: readdir %s: %w", s.baseDir, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	states := make([]port.AgentState, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		agentID := entry.Name()[:len(entry.Name())-5] // strip .json
		st, err := s.readStateLocked(agentID)
		if err != nil || st == nil {
			continue
		}
		states = append(states, *st)
	}
	return states, nil
}

// readStateLocked reads a single agent state. Caller must hold s.mu.
func (s *StateStore) readStateLocked(agentID string) (*port.AgentState, error) {
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}

	if cached, ok := s.cache[agentID]; ok {
		return &cached, nil
	}

	path := filepath.Join(s.baseDir, agentID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}

	var state port.AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("state: unmarshal %s: %w", path, err)
	}

	s.cache[agentID] = state
	return &state, nil
}
