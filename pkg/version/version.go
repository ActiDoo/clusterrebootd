package version

import (
	"runtime/debug"
	"strings"
)

const defaultVersion = "0.1.0-dev"

// Version holds the semantic version for the running binary.
// It defaults to a development marker but can be overridden at
// build time via -ldflags "-X github.com/clusterrebootd/clusterrebootd/pkg/version.Version=<value>".
var Version = defaultVersion

var readBuildInfo = debug.ReadBuildInfo

func init() {
	Version = deriveVersion(Version)
}

func deriveVersion(current string) string {
	if current != "" && current != defaultVersion {
		return current
	}

	info, ok := readBuildInfo()
	if !ok || info == nil {
		return current
	}

	if v := sanitizeModuleVersion(info.Main.Version); v != "" {
		return v
	}

	if v := deriveFromSettings(info.Settings); v != "" {
		return v
	}

	return current
}

func sanitizeModuleVersion(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "", "(devel)":
		return ""
	default:
		return v
	}
}

func deriveFromSettings(settings []debug.BuildSetting) string {
	var (
		revision string
		modified bool
	)

	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision == "" {
		return ""
	}

	if len(revision) > 12 {
		revision = revision[:12]
	}

	if modified {
		revision += "-dirty"
	}

	return "devel+" + revision
}
