package clusterhealth

import (
	"context"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/internal/testutil"
)

func TestEtcdManagerTracksNodeStatuses(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	manager, err := NewEtcdManager(EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		Namespace: "clusterrebootd",
		Prefix:    "cluster_health",
		NodeName:  "node-a",
	})
	if err != nil {
		t.Fatalf("failed to create cluster health manager: %v", err)
	}
	defer manager.Close()

	ctx := context.Background()

	records, err := manager.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected status query error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no node records, got %d", len(records))
	}

	report := Report{Stage: "pre-lock", Reason: "exit 1"}
	if err := manager.ReportUnhealthy(ctx, report); err != nil {
		t.Fatalf("failed to report unhealthy: %v", err)
	}

	records, err = manager.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected status query error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one node record, got %d", len(records))
	}
	rec := records[0]
	if rec.Node != "node-a" {
		t.Fatalf("expected node-a, got %s", rec.Node)
	}
	if rec.Healthy {
		t.Fatalf("expected node to be marked unhealthy")
	}
	if rec.Stage != report.Stage {
		t.Fatalf("expected stage %s, got %s", report.Stage, rec.Stage)
	}
	if rec.Reason != report.Reason {
		t.Fatalf("expected reason %s, got %s", report.Reason, rec.Reason)
	}
	if time.Since(rec.ReportedAt) > time.Minute {
		t.Fatalf("expected recent reported timestamp, got %s", rec.ReportedAt)
	}

	if err := manager.ReportHealthy(ctx); err != nil {
		t.Fatalf("failed to record healthy entry: %v", err)
	}

	records, err = manager.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected status query error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one node record after healthy update, got %d", len(records))
	}
	rec = records[0]
	if !rec.Healthy {
		t.Fatalf("expected node to be marked healthy")
	}
	if rec.Stage != "" || rec.Reason != "" {
		t.Fatalf("expected healthy record to clear stage/reason, got stage=%q reason=%q", rec.Stage, rec.Reason)
	}
}

func TestEtcdManagerIgnoresMissingNodeName(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	_, err := NewEtcdManager(EtcdManagerOptions{Endpoints: cluster.Endpoints})
	if err == nil {
		t.Fatal("expected error when node name is missing")
	}
}
