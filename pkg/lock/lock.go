package lock

import (
	"context"
	"errors"
)

var (
	// ErrNotAcquired indicates that the lock is currently held by someone else.
	ErrNotAcquired = errors.New("lock: not acquired")
)

// Manager coordinates access to a distributed lock.
type Manager interface {
	Acquire(ctx context.Context) (Lease, error)
}

// Lease represents a held lock that can be released.
type Lease interface {
	Release(ctx context.Context) error
}

// NoopManager returns an immediately acquired lease without performing any remote coordination.
type NoopManager struct{}

// NewNoopManager constructs a manager that always succeeds in acquiring the lock.
func NewNoopManager() *NoopManager {
	return &NoopManager{}
}

// Acquire implements Manager for NoopManager.
func (m *NoopManager) Acquire(ctx context.Context) (Lease, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return noopLease{}, nil
}

type noopLease struct{}

func (noopLease) Release(ctx context.Context) error { return nil }

var _ Manager = (*NoopManager)(nil)
var _ Lease = (*noopLease)(nil)
