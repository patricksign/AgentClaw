package infra

import (
	"context"
	"io"
	"time"
)

type Connection interface {
	io.Closer
	// Ping checks if the connection is alive
	Ping(ctx context.Context) error
	// IsHealthy performs a health check with timeout
	IsHealthy(ctx context.Context) bool
	// Name returns the identifier for this connection
	Name() string
}

// =============================================================================
// Cache-Specific Contracts
// =============================================================================

// CacheStore extends Connection with cache operations.
// clean-arch: Port for cache operations
type CacheStore interface {
	Connection
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// =============================================================================
// Database-Specific Contracts
// =============================================================================

// TransactionalDB extends Connection with transaction support.
// clean-arch: Port for transactional database operations
type TransactionalDB interface {
	Connection
	BeginTx(ctx context.Context) (Transaction, error)
	Stats() ConnectionStats
}

// Transaction represents a database transaction.
type Transaction interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// ConnectionStats represents connection pool statistics.
type ConnectionStats struct {
	MaxConnections      int32 `json:"max_connections"`
	CurrentConnections  int32 `json:"current_connections"`
	IdleConnections     int32 `json:"idle_connections"`
	AcquiredConnections int32 `json:"acquired_connections"`
}

// =============================================================================
// Health Check Contracts
// =============================================================================

// HealthChecker defines the contract for health checking.
type HealthChecker interface {
	Check(ctx context.Context) HealthStatus
}

// HealthStatus represents the health status of a component.
type HealthStatus struct {
	Name      string        `json:"name"`
	Status    Status        `json:"status"`
	Latency   time.Duration `json:"latency_ms"`
	Message   string        `json:"message,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

// Status represents the health status enum.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusUnhealthy Status = "unhealthy"
	StatusDegraded  Status = "degraded"
)
