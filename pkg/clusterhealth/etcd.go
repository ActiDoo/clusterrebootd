package clusterhealth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// EtcdManagerOptions configures the etcd-backed cluster health manager.
type EtcdManagerOptions struct {
	Endpoints   []string
	DialTimeout time.Duration
	Namespace   string
	Prefix      string
	TLS         *tls.Config
	NodeName    string
	Clock       func() time.Time
}

// EtcdManager persists cluster health state in etcd so nodes can coordinate.
type EtcdManager struct {
	client   *clientv3.Client
	prefix   string
	node     string
	now      func() time.Time
	nodePath string
}

type recordPayload struct {
	Node       string `json:"node"`
	Healthy    bool   `json:"healthy"`
	Stage      string `json:"stage,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ReportedAt string `json:"reported_at"`
}

// NewEtcdManager constructs a cluster health manager backed by etcd.
func NewEtcdManager(opts EtcdManagerOptions) (*EtcdManager, error) {
	if len(opts.Endpoints) == 0 {
		return nil, errors.New("cluster health manager requires at least one etcd endpoint")
	}
	trimmedPrefix := strings.TrimSpace(opts.Prefix)
	if trimmedPrefix == "" {
		trimmedPrefix = "cluster_health"
	}
	node := strings.TrimSpace(opts.NodeName)
	if node == "" {
		return nil, errors.New("cluster health manager requires a node name")
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

	normalizedPrefix := strings.TrimRight(applyNamespace(opts.Namespace, trimmedPrefix), "/")
	nodePath := path.Join(normalizedPrefix, node)

	return &EtcdManager{
		client:   client,
		prefix:   normalizedPrefix,
		node:     node,
		now:      clock,
		nodePath: nodePath,
	}, nil
}

// Close releases underlying client resources.
func (m *EtcdManager) Close() error {
	if m == nil {
		return nil
	}
	return m.client.Close()
}

// ReportHealthy implements Manager.
func (m *EtcdManager) ReportHealthy(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	payload := recordPayload{
		Node:       m.node,
		Healthy:    true,
		ReportedAt: m.now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	ctx = clientv3.WithRequireLeader(ctx)
	_, err = m.client.Put(ctx, m.nodePath, string(encoded))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("store cluster health entry: %w", err)
	}
	return nil
}

// ReportUnhealthy implements Manager.
func (m *EtcdManager) ReportUnhealthy(ctx context.Context, report Report) error {
	if ctx == nil {
		ctx = context.Background()
	}

	payload := recordPayload{
		Node:       m.node,
		Healthy:    false,
		Stage:      strings.TrimSpace(report.Stage),
		Reason:     strings.TrimSpace(report.Reason),
		ReportedAt: m.now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	ctx = clientv3.WithRequireLeader(ctx)
	_, err = m.client.Put(ctx, m.nodePath, string(encoded))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("store cluster health entry: %w", err)
	}
	return nil
}

// Status implements Manager.
func (m *EtcdManager) Status(ctx context.Context) ([]Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	ctx = clientv3.WithRequireLeader(ctx)
	prefix := m.prefix
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	resp, err := m.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("list cluster health entries: %w", err)
	}

	records := make([]Record, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var payload recordPayload
		if err := json.Unmarshal(kv.Value, &payload); err != nil {
			return nil, fmt.Errorf("parse cluster health payload: %w", err)
		}
		reportedAt, err := time.Parse(time.RFC3339Nano, payload.ReportedAt)
		if err != nil {
			return nil, fmt.Errorf("parse cluster health timestamp: %w", err)
		}
		node := payload.Node
		if node == "" {
			node = strings.TrimPrefix(string(kv.Key), prefix)
		}
		records = append(records, Record{
			Node:       node,
			Healthy:    payload.Healthy,
			Stage:      payload.Stage,
			Reason:     payload.Reason,
			ReportedAt: reportedAt,
		})
	}

	return records, nil
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
