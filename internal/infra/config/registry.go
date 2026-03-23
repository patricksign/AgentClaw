package infra

import (
	"context"
	"fmt"
	"sync"
	"time"

	"log/slog"
)

// =============================================================================
// Infrastructure Registry
// clean-arch: Centralized registry for all infrastructure components
// =============================================================================

// Registry provides unified access to all infrastructure components.
// It manages connections lifecycle and health checking.
type Registry struct {
	mu          sync.RWMutex
	connections map[string]Connection
	cache       *RedisClient
}

// NewRegistry creates a new infrastructure registry.
func NewRegistry() *Registry {
	return &Registry{
		connections: make(map[string]Connection),
	}
}

// =============================================================================
// Registration Methods
// =============================================================================

// RegisterConnection registers a connection with the registry.
func (r *Registry) RegisterConnection(conn Connection) error {
	if conn == nil {
		return fmt.Errorf("connection is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := conn.Name()
	if _, exists := r.connections[name]; exists {
		return fmt.Errorf("connection %s already registered", name)
	}

	r.connections[name] = conn
	slog.Info("Registered infrastructure connection", "name", name)
	return nil
}

// RegisterCache registers the cache client.
func (r *Registry) RegisterCache(cache *RedisClient) error {
	if cache == nil {
		return fmt.Errorf("cache client is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.cache = cache
	r.connections[cache.Name()] = cache
	slog.Info("Registered cache connection")
	return nil
}

// Cache returns the cache client.
func (r *Registry) Cache() *RedisClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cache
}

// =============================================================================
// Health Check Methods
// =============================================================================

// HealthCheckAll performs health checks on all registered connections.
func (r *Registry) HealthCheckAll(ctx context.Context) []HealthStatus {
	r.mu.RLock()
	connections := make([]Connection, 0, len(r.connections))
	for _, conn := range r.connections {
		connections = append(connections, conn)
	}
	r.mu.RUnlock()

	results := make([]HealthStatus, 0, len(connections))

	for _, conn := range connections {
		status := r.checkConnection(ctx, conn)
		results = append(results, status)
	}

	return results
}

// HealthCheck performs a health check on a specific connection.
func (r *Registry) HealthCheck(ctx context.Context, name string) (HealthStatus, error) {
	r.mu.RLock()
	conn, ok := r.connections[name]
	r.mu.RUnlock()

	if !ok {
		return HealthStatus{}, fmt.Errorf("connection %s not found", name)
	}

	return r.checkConnection(ctx, conn), nil
}

func (r *Registry) checkConnection(ctx context.Context, conn Connection) HealthStatus {
	start := time.Now()
	healthy := conn.IsHealthy(ctx)
	latency := time.Since(start)

	status := StatusHealthy
	message := "OK"

	if !healthy {
		status = StatusUnhealthy
		message = "Health check failed"
	} else if latency > 100*time.Millisecond {
		status = StatusDegraded
		message = fmt.Sprintf("High latency: %v", latency)
	}

	return HealthStatus{
		Name:      conn.Name(),
		Status:    status,
		Latency:   latency,
		Message:   message,
		Timestamp: time.Now(),
	}
}

// IsHealthy returns true if all connections are healthy.
func (r *Registry) IsHealthy(ctx context.Context) bool {
	statuses := r.HealthCheckAll(ctx)
	for _, status := range statuses {
		if status.Status == StatusUnhealthy {
			return false
		}
	}
	return true
}

// =============================================================================
// Lifecycle Methods
// =============================================================================

// Close closes all registered connections.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error

	for name, conn := range r.connections {
		if err := conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close %s: %w", name, err))
			slog.Error("Failed to close connection", "name", name, "err", err)
		} else {
			slog.Info("Closed connection", "name", name)
		}
	}

	// Clear the registry
	r.connections = make(map[string]Connection)
	r.cache = nil

	if len(errs) > 0 {
		return fmt.Errorf("errors closing connections: %v", errs)
	}
	return nil
}
