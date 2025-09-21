package version

import (
	"runtime/debug"
	"testing"
)

func TestDeriveVersionPreservesOverride(t *testing.T) {
	t.Cleanup(func() { readBuildInfo = debug.ReadBuildInfo })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		t.Fatalf("unexpected call to readBuildInfo")
		return nil, false
	}

	const override = "1.2.3"
	if got := deriveVersion(override); got != override {
		t.Fatalf("expected override %q to be preserved, got %q", override, got)
	}
}

func TestDeriveVersionUsesModuleVersion(t *testing.T) {
	t.Cleanup(func() { readBuildInfo = debug.ReadBuildInfo })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v1.4.0"},
		}, true
	}

	if got := deriveVersion(defaultVersion); got != "v1.4.0" {
		t.Fatalf("expected module version to be used, got %q", got)
	}
}

func TestDeriveVersionUsesRevisionFallback(t *testing.T) {
	t.Cleanup(func() { readBuildInfo = debug.ReadBuildInfo })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abcdef1234567890"},
				{Key: "vcs.modified", Value: "true"},
			},
		}, true
	}

	if got := deriveVersion(defaultVersion); got != "devel+abcdef123456-dirty" {
		t.Fatalf("expected revision fallback, got %q", got)
	}
}
