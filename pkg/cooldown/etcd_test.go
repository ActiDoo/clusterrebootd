package cooldown

import (
	"context"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/internal/testutil"
)

func TestEtcdManagerStatusAndStart(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		Key:       "cooldown",
		Namespace: "clusterrebootd",
		NodeName:  "node-a",
	})
	if err != nil {
		t.Fatalf("failed to create cooldown manager: %v", err)
	}
	defer manager.Close()

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected status error: %v", err)
	}
	if status.Active {
		t.Fatalf("expected cooldown to be inactive")
	}

	duration := 2 * time.Second
	if err := manager.Start(context.Background(), duration); err != nil {
		t.Fatalf("failed to start cooldown: %v", err)
	}

	status, err = manager.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected status error: %v", err)
	}
	if !status.Active {
		t.Fatalf("expected cooldown to be active")
	}
	if status.Node != "node-a" {
		t.Fatalf("expected node-a, got %s", status.Node)
	}
	if status.Remaining <= 0 {
		t.Fatalf("expected positive remaining duration, got %s", status.Remaining)
	}

	time.Sleep(duration + 500*time.Millisecond)

	status, err = manager.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected status error: %v", err)
	}
	if status.Active {
		t.Fatalf("expected cooldown to expire")
	}
}

func TestEtcdManagerStartClearsWhenDurationZero(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		Key:       "cooldown",
		Namespace: "clusterrebootd",
		NodeName:  "node-a",
	})
	if err != nil {
		t.Fatalf("failed to create cooldown manager: %v", err)
	}
	defer manager.Close()

	if err := manager.Start(context.Background(), 3*time.Second); err != nil {
		t.Fatalf("failed to start cooldown: %v", err)
	}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected status error: %v", err)
	}
	if !status.Active {
		t.Fatalf("expected cooldown to be active")
	}

	if err := manager.Start(context.Background(), 0); err != nil {
		t.Fatalf("failed to clear cooldown: %v", err)
	}

	status, err = manager.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected status error: %v", err)
	}
	if status.Active {
		t.Fatalf("expected cooldown to be cleared")
	}
}
