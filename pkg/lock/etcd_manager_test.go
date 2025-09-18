package lock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/internal/testutil"
)

func TestEtcdManagerAcquireAndRelease(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		LockKey:   "/cluster/reboot",
		TTL:       3 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create etcd manager: %v", err)
	}
	defer manager.Close()

	lease, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatalf("expected acquire to succeed, got %v", err)
	}
	if lease == nil {
		t.Fatal("expected lease to be non-nil")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("expected release to succeed, got %v", err)
	}
}

func TestEtcdManagerContention(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		LockKey:   "/cluster/reboot",
		TTL:       3 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create etcd manager: %v", err)
	}
	defer manager.Close()

	lease1, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatalf("expected first acquire to succeed, got %v", err)
	}
	if lease1 == nil {
		t.Fatal("expected first lease to be non-nil")
	}

	if _, err := manager.Acquire(context.Background()); !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("expected ErrNotAcquired when lock held, got %v", err)
	}

	if err := lease1.Release(context.Background()); err != nil {
		t.Fatalf("expected release to succeed, got %v", err)
	}

	lease2, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatalf("expected second acquire to succeed, got %v", err)
	}
	if err := lease2.Release(context.Background()); err != nil {
		t.Fatalf("expected second release to succeed, got %v", err)
	}
}

func TestEtcdManagerAcquireContextCancelled(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		LockKey:   "/cluster/reboot",
		TTL:       3 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create etcd manager: %v", err)
	}
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}

func TestEtcdManagerNamespaceApplied(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		LockKey:   "lock",
		Namespace: "env/prod",
		TTL:       3 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create etcd manager: %v", err)
	}
	defer manager.Close()

	lease, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	internal, ok := lease.(*etcdLease)
	if !ok {
		t.Fatalf("expected lease to be etcdLease, got %T", lease)
	}
	key := internal.mutex.Key()
	if !strings.HasPrefix(key, "/env/prod/lock/") {
		t.Fatalf("expected key to include namespace prefix, got %s", key)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("failed to release lease: %v", err)
	}
}
