package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromReleaseModuleAndSourceBuilds(t *testing.T) {
	for _, test := range []struct {
		info           *debug.BuildInfo
		override, want string
	}{
		{nil, "", "dev"}, {nil, "v0.1.0-rc.1", "v0.1.0-rc.1"},
		{&debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "", "v0.1.0"},
		{&debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "custom", "custom"},
		{&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "", "dev"},
		{&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef1234567890"}, {Key: "vcs.modified", Value: "true"}}}, "", "dev-abcdef123456-dirty"},
		{&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef1234567890"}}}, "", "dev-abcdef123456"},
	} {
		if got := versionFrom(test.info, test.override); got != test.want {
			t.Fatalf("%q != %q", got, test.want)
		}
	}
}
