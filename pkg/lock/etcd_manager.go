package lock

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// EtcdManagerOptions configures the etcd-backed lock manager.
type EtcdManagerOptions struct {
	Endpoints   []string
	DialTimeout time.Duration
	LockKey     string
	Namespace   string
	TTL         time.Duration
	TLS         *tls.Config
	NodeName    string
	ProcessID   int
	Clock       func() time.Time
}

// EtcdManager coordinates lock acquisition via etcd mutexes.
type EtcdManager struct {
	client     *clientv3.Client
	key        string
	ttlSeconds int
	identity   leaseIdentity
	now        func() time.Time
}

type leaseIdentity struct {
	nodeName string
	pid      int
}

type leaseAnnotation struct {
	Node       string `json:"node"`
	PID        int    `json:"pid"`
	AcquiredAt string `json:"acquired_at"`
}

// NewEtcdManager builds a lock manager backed by etcd.
func NewEtcdManager(opts EtcdManagerOptions) (*EtcdManager, error) {
	if len(opts.Endpoints) == 0 {
		return nil, errors.New("etcd lock manager requires at least one endpoint")
	}
	trimmedKey := strings.TrimSpace(opts.LockKey)
	if trimmedKey == "" {
		return nil, errors.New("etcd lock manager requires a non-empty lock key")
	}
	if opts.TTL <= 0 {
		return nil, errors.New("etcd lock manager requires a positive TTL")
	}

	nodeName := strings.TrimSpace(opts.NodeName)
	if nodeName == "" {
		return nil, errors.New("etcd lock manager requires a non-empty node name for metadata")
	}

	pid := opts.ProcessID
	if pid <= 0 {
		pid = os.Getpid()
	}

	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}

	ttlSeconds := int(math.Ceil(opts.TTL.Seconds()))
	if ttlSeconds <= 0 {
		return nil, errors.New("etcd lock manager TTL must be at least 1 second")
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

	manager := &EtcdManager{
		client:     client,
		key:        applyNamespace(opts.Namespace, trimmedKey),
		ttlSeconds: ttlSeconds,
		identity: leaseIdentity{
			nodeName: nodeName,
			pid:      pid,
		},
		now: clock,
	}

	return manager, nil
}

// Close releases underlying client resources.
func (m *EtcdManager) Close() error {
	if m == nil {
		return nil
	}
	return m.client.Close()
}

// Acquire attempts to obtain the distributed lock.
func (m *EtcdManager) Acquire(ctx context.Context) (Lease, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	linearizableCtx := clientv3.WithRequireLeader(ctx)

	session, err := concurrency.NewSession(m.client, concurrency.WithTTL(m.ttlSeconds))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("create session: %w", err)
	}

	mutex := concurrency.NewMutex(session, m.key)
	if err := mutex.TryLock(linearizableCtx); err != nil {
		_ = session.Close()
		if errors.Is(err, concurrency.ErrLocked) {
			return nil, ErrNotAcquired
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("try lock: %w", err)
	}

	if err := m.annotateLease(linearizableCtx, session, mutex); err != nil {
		cleanupBase := clientv3.WithRequireLeader(context.Background())
		cleanupCtx, cancel := context.WithTimeout(cleanupBase, 5*time.Second)
		_ = mutex.Unlock(cleanupCtx)
		cancel()
		_ = session.Close()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("annotate lock: %w", err)
	}

	return &etcdLease{session: session, mutex: mutex}, nil
}

var _ Manager = (*EtcdManager)(nil)

type etcdLease struct {
	session *concurrency.Session
	mutex   *concurrency.Mutex
}

func (l *etcdLease) Release(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	ctx = clientv3.WithRequireLeader(ctx)

	unlockErr := l.mutex.Unlock(ctx)
	closeErr := l.session.Close()

	if unlockErr != nil && !errors.Is(unlockErr, concurrency.ErrLockReleased) {
		if errors.Is(unlockErr, context.Canceled) || errors.Is(unlockErr, context.DeadlineExceeded) {
			return unlockErr
		}
		return fmt.Errorf("unlock: %w", unlockErr)
	}
	if closeErr != nil {
		if errors.Is(closeErr, context.Canceled) || errors.Is(closeErr, context.DeadlineExceeded) {
			return closeErr
		}
		return fmt.Errorf("close session: %w", closeErr)
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

func (m *EtcdManager) annotateLease(ctx context.Context, session *concurrency.Session, mutex *concurrency.Mutex) error {
	annotation := leaseAnnotation{
		Node:       m.identity.nodeName,
		PID:        m.identity.pid,
		AcquiredAt: m.now().UTC().Format(time.RFC3339Nano),
	}

	payload, err := json.Marshal(annotation)
	if err != nil {
		return err
	}

	_, err = session.Client().Put(ctx, mutex.Key(), string(payload), clientv3.WithLease(session.Lease()))
	return err
}
