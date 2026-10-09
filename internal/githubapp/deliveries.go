package githubapp

import (
	"context"
	"sync"
)

// Deliveries deduplicates webhook deliveries by delivery ID, so GitHub
// retries return success without synchronizing twice
// (docs/design/04-dual-mode-and-sync.md). The production implementation is
// backed by SQLite in the workspace metadata store; MemoryDeliveries exists
// for tests and in-memory callers.
type Deliveries interface {
	// Seen reports whether deliveryID has already been recorded.
	Seen(ctx context.Context, deliveryID string) (bool, error)
	// Record stores deliveryID so later Seen calls report true.
	Record(ctx context.Context, deliveryID string) error
}

// MemoryDeliveries is a thread-safe in-memory Deliveries implementation.
type MemoryDeliveries struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewMemoryDeliveries returns an empty in-memory delivery ledger.
func NewMemoryDeliveries() *MemoryDeliveries {
	return &MemoryDeliveries{seen: make(map[string]struct{})}
}

// Seen implements Deliveries.
func (m *MemoryDeliveries) Seen(_ context.Context, deliveryID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.seen[deliveryID]
	return ok, nil
}

// Record implements Deliveries.
func (m *MemoryDeliveries) Record(_ context.Context, deliveryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[deliveryID] = struct{}{}
	return nil
}
