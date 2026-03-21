package memory

import (
	"github.com/patricksign/AgentClaw/internal/domain"
	corememory "github.com/patricksign/AgentClaw/internal/memory"
	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check: SQLiteCheckpointStore implements port.CheckpointStore.
var _ port.CheckpointStore = (*SQLiteCheckpointStore)(nil)

// SQLiteCheckpointStore adapts memory.Store's checkpoint methods
// to satisfy the port.CheckpointStore interface.
type SQLiteCheckpointStore struct {
	store *corememory.Store
}

// NewSQLiteCheckpointStore creates a checkpoint store backed by SQLite.
func NewSQLiteCheckpointStore(store *corememory.Store) *SQLiteCheckpointStore {
	return &SQLiteCheckpointStore{store: store}
}

func (s *SQLiteCheckpointStore) Save(cp *domain.PhaseCheckpoint) error {
	return s.store.SaveCheckpoint(cp)
}

func (s *SQLiteCheckpointStore) Load(taskID string) (*domain.PhaseCheckpoint, error) {
	return s.store.LoadCheckpoint(taskID)
}

func (s *SQLiteCheckpointStore) Delete(taskID string) error {
	return s.store.DeleteCheckpoint(taskID)
}
