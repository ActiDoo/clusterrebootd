package testutil

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"go.etcd.io/etcd/server/v3/embed"
)

type EmbeddedEtcd struct {
	Server    *embed.Etcd
	Endpoints []string
}

func StartEmbeddedEtcd(t testing.TB) *EmbeddedEtcd {
	t.Helper()

	cfg := embed.NewConfig()
	cfg.Dir = t.TempDir()
	cfg.Logger = "zap"
	cfg.LogLevel = "error"
	cfg.EnableGRPCGateway = false
	peerURL := mustURL(t, "http://127.0.0.1:0")
	clientURL := mustURL(t, "http://127.0.0.1:0")
	cfg.InitialCluster = fmt.Sprintf("%s=%s", cfg.Name, peerURL.String())
	cfg.ListenPeerUrls = []url.URL{peerURL}
	cfg.AdvertisePeerUrls = []url.URL{peerURL}
	cfg.ListenClientUrls = []url.URL{clientURL}
	cfg.AdvertiseClientUrls = []url.URL{clientURL}

	e, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatalf("failed to start embedded etcd: %v", err)
	}

	select {
	case <-e.Server.ReadyNotify():
	case <-time.After(15 * time.Second):
		e.Server.Stop()
		<-e.Server.StopNotify()
		t.Fatalf("embedded etcd did not start within timeout")
	}

	endpoints := make([]string, 0, len(e.Clients))
	for _, listener := range e.Clients {
		endpoints = append(endpoints, listener.Addr().String())
	}

	t.Cleanup(func() {
		e.Close()
		select {
		case <-e.Server.StopNotify():
		case <-time.After(5 * time.Second):
		}
	})

	return &EmbeddedEtcd{Server: e, Endpoints: endpoints}
}

func mustURL(t testing.TB, raw string) url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("failed to parse url %q: %v", raw, err)
	}
	return *parsed
}
