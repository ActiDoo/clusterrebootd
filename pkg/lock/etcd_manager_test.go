package lock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/internal/testutil"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestEtcdManagerAcquireAndRelease(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		LockKey:   "/cluster/reboot",
		TTL:       3 * time.Second,
		NodeName:  "node-a",
		ProcessID: 4242,
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
		NodeName:  "node-a",
		ProcessID: 4242,
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
		NodeName:  "node-a",
		ProcessID: 4242,
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
		NodeName:  "node-a",
		ProcessID: 4242,
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

func TestEtcdManagerAnnotatesLease(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	acquiredAt := time.Date(2024, time.March, 7, 11, 45, 12, 123000000, time.UTC)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		LockKey:   "/cluster/reboot",
		TTL:       5 * time.Second,
		NodeName:  "node-observer",
		ProcessID: 9001,
		Clock: func() time.Time {
			return acquiredAt
		},
	})
	if err != nil {
		t.Fatalf("failed to create etcd manager: %v", err)
	}
	defer manager.Close()

	lease, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   cluster.Endpoints,
		DialTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create etcd client: %v", err)
	}
	defer etcdClient.Close()

	internal, ok := lease.(*etcdLease)
	if !ok {
		t.Fatalf("expected lease to be etcdLease, got %T", lease)
	}

	key := internal.mutex.Key()
	resp, err := etcdClient.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("expected metadata entry, got %d", len(resp.Kvs))
	}

	var annotation struct {
		Node       string `json:"node"`
		PID        int    `json:"pid"`
		AcquiredAt string `json:"acquired_at"`
	}
	if err := json.Unmarshal(resp.Kvs[0].Value, &annotation); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}

	if annotation.Node != "node-observer" {
		t.Fatalf("expected node-observer, got %q", annotation.Node)
	}
	if annotation.PID != 9001 {
		t.Fatalf("expected PID 9001, got %d", annotation.PID)
	}
	expectedTimestamp := acquiredAt.UTC().Format(time.RFC3339Nano)
	if annotation.AcquiredAt != expectedTimestamp {
		t.Fatalf("expected timestamp %s, got %s", expectedTimestamp, annotation.AcquiredAt)
	}

	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("failed to release lock: %v", err)
	}

	resp, err = etcdClient.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("failed to read metadata after release: %v", err)
	}
	if len(resp.Kvs) != 0 {
		t.Fatalf("expected metadata to be cleared after release, still found %d entries", len(resp.Kvs))
	}
}
