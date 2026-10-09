package main

import (
	"runtime/debug"
	"strings"
)

// version is optionally set by release builds with -X main.version. go install
// builds instead carry their module version in Go's embedded build information.
var version string

func binaryVersion() string {
	info, _ := debug.ReadBuildInfo()
	return versionFrom(info, version)
}
func versionFrom(info *debug.BuildInfo, override string) string {
	if override != "" {
		return override
	}
	if info == nil {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision == "" {
		return "dev"
	}
	label := "dev-" + strings.TrimSpace(revision)
	if dirty {
		label += "-dirty"
	}
	return label
}
