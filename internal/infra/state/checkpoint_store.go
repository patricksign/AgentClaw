package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check: CheckpointStore implements port.CheckpointStore.
var _ port.CheckpointStore = (*CheckpointStore)(nil)

// CheckpointStore persists phase checkpoints as JSON files on disk.
// Each task gets one checkpoint file: <baseDir>/checkpoints/<taskID>.json.
// Uses atomic write (tmp+rename) and an in-memory cache for fast reads.
type CheckpointStore struct {
	mu    sync.Mutex
	dir   string
	cache map[string]*domain.PhaseCheckpoint
}

// NewCheckpointStore creates a CheckpointStore rooted at baseDir/checkpoints/.
func NewCheckpointStore(baseDir string) (*CheckpointStore, error) {
	dir := filepath.Join(baseDir, "checkpoints")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("checkpoint: mkdir %s: %w", dir, err)
	}
	return &CheckpointStore{
		dir:   dir,
		cache: make(map[string]*domain.PhaseCheckpoint),
	}, nil
}

// Save persists a checkpoint, overwriting any existing one for the same taskID.
func (s *CheckpointStore) Save(cp *domain.PhaseCheckpoint) error {
	if cp.TaskID == "" {
		return fmt.Errorf("checkpoint: empty task ID")
	}
	if err := validateAgentID(cp.TaskID); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}

	cp.SavedAt = time.Now()

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: marshal %s: %w", cp.TaskID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, cp.TaskID+".json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("checkpoint: write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("checkpoint: rename %s: %w", path, err)
	}

	// Store a copy in cache to avoid aliasing.
	cached := *cp
	s.cache[cp.TaskID] = &cached
	return nil
}

// Load retrieves the checkpoint for a task. Returns nil, nil if none exists.
func (s *CheckpointStore) Load(taskID string) (*domain.PhaseCheckpoint, error) {
	if taskID == "" {
		return nil, fmt.Errorf("checkpoint: empty task ID")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.cache[taskID]; ok {
		cp := *cached
		return &cp, nil
	}

	path := filepath.Join(s.dir, taskID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint: read %s: %w", path, err)
	}

	var cp domain.PhaseCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint: unmarshal %s: %w", path, err)
	}

	cached := cp
	s.cache[taskID] = &cached
	return &cp, nil
}

// Delete removes the checkpoint for a task (called after phase completes or task is done).
func (s *CheckpointStore) Delete(taskID string) error {
	if taskID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.cache, taskID)

	path := filepath.Join(s.dir, taskID+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("checkpoint: remove %s: %w", path, err)
	}
	return nil
}
