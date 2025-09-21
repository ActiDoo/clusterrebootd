package cooldown

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// EtcdManagerOptions configures the etcd-backed cooldown coordinator.
type EtcdManagerOptions struct {
	Endpoints   []string
	DialTimeout time.Duration
	Namespace   string
	Key         string
	TLS         *tls.Config
	NodeName    string
	Clock       func() time.Time
}

// EtcdManager persists cooldown windows in etcd using a short-lived lease.
type EtcdManager struct {
	client *clientv3.Client
	key    string
	node   string
	now    func() time.Time
}

type cooldownRecord struct {
	Node        string `json:"node"`
	StartedAt   string `json:"started_at"`
	DurationSec int64  `json:"duration_sec"`
}

// NewEtcdManager constructs a cooldown manager backed by etcd.
func NewEtcdManager(opts EtcdManagerOptions) (*EtcdManager, error) {
	if len(opts.Endpoints) == 0 {
		return nil, errors.New("cooldown etcd manager requires at least one endpoint")
	}
	trimmedKey := strings.TrimSpace(opts.Key)
	if trimmedKey == "" {
		return nil, errors.New("cooldown etcd manager requires a key")
	}
	node := strings.TrimSpace(opts.NodeName)
	if node == "" {
		return nil, errors.New("cooldown etcd manager requires a node name")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	dialTimeout := opts.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}

	cfg := clientv3.Config{
		Endpoints:           opts.Endpoints,
		DialTimeout:         dialTimeout,
		TLS:                 opts.TLS,
		RejectOldCluster:    true,
		PermitWithoutStream: true,
	}
	client, err := clientv3.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("create etcd client: %w", err)
	}

	return &EtcdManager{
		client: client,
		key:    applyNamespace(opts.Namespace, trimmedKey),
		node:   node,
		now:    clock,
	}, nil
}

// Close releases underlying client resources.
func (m *EtcdManager) Close() error {
	if m == nil {
		return nil
	}
	return m.client.Close()
}

// Status implements Manager.
func (m *EtcdManager) Status(ctx context.Context) (Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	linearizableCtx := clientv3.WithRequireLeader(ctx)

	resp, err := m.client.Get(linearizableCtx, m.key)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Status{}, err
		}
		return Status{}, fmt.Errorf("read cooldown key: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return Status{}, nil
	}
	kv := resp.Kvs[0]
	var record cooldownRecord
	if err := json.Unmarshal(kv.Value, &record); err != nil {
		return Status{}, fmt.Errorf("parse cooldown payload: %w", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, record.StartedAt)
	if err != nil {
		return Status{}, fmt.Errorf("parse cooldown start timestamp: %w", err)
	}
	status := Status{
		Active:    true,
		Node:      record.Node,
		StartedAt: startedAt,
	}
	if record.DurationSec > 0 {
		status.ExpiresAt = startedAt.Add(time.Duration(record.DurationSec) * time.Second)
	}
	if kv.Lease != 0 {
		ttlCtx := clientv3.WithRequireLeader(ctx)
		ttlResp, ttlErr := m.client.TimeToLive(ttlCtx, clientv3.LeaseID(kv.Lease))
		if ttlErr != nil {
			if errors.Is(ttlErr, context.Canceled) || errors.Is(ttlErr, context.DeadlineExceeded) {
				return Status{}, ttlErr
			}
			return Status{}, fmt.Errorf("query cooldown ttl: %w", ttlErr)
		}
		if ttlResp.TTL <= 0 {
			status.Active = false
			status.Remaining = 0
		} else {
			status.Remaining = time.Duration(ttlResp.TTL) * time.Second
			if status.Remaining < 0 {
				status.Remaining = 0
			}
		}
	}
	if !status.Active {
		return Status{}, nil
	}
	if !status.ExpiresAt.IsZero() {
		remaining := time.Until(status.ExpiresAt)
		if remaining > 0 {
			status.Remaining = remaining
		}
	}
	return status, nil
}

// Start implements Manager.
func (m *EtcdManager) Start(ctx context.Context, duration time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if duration <= 0 {
		_, err := m.client.Delete(ctx, m.key)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("clear cooldown key: %w", err)
		}
		return err
	}

	seconds := int64(math.Ceil(duration.Seconds()))
	if seconds <= 0 {
		seconds = 1
	}
	lease, err := m.client.Grant(ctx, seconds)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("grant cooldown lease: %w", err)
	}

	record := cooldownRecord{
		Node:        m.node,
		StartedAt:   m.now().UTC().Format(time.RFC3339Nano),
		DurationSec: seconds,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}

	_, err = m.client.Put(ctx, m.key, string(payload), clientv3.WithLease(lease.ID))
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = m.client.Revoke(cleanupCtx, lease.ID)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("store cooldown payload: %w", err)
	}

	return nil
}

func applyNamespace(namespace, key string) string {
	normalizedKey := "/" + strings.TrimLeft(key, "/")
	trimmedNamespace := strings.Trim(namespace, "/")
	if trimmedNamespace == "" {
		return normalizedKey
	}
	return "/" + trimmedNamespace + normalizedKey
}

var _ Manager = (*EtcdManager)(nil)
