package main

import (
	"runtime/debug"
	"testing"
)

func TestGoInstalledVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Path: "github.com/ThamalDilanka/easy-sso", Version: "v1.2.3"}}
	v, c, d := buildVersion(info)
	if v != "v1.2.3" || c != "module" || d != "go" {
		t.Fatalf("%s %s %s", v, c, d)
	}
	info.Settings = []debug.BuildSetting{{Key: "vcs", Value: "git"}}
	v, _, d = buildVersion(info)
	if v != version || d != "development" {
		t.Fatal("local VCS build misidentified as Go module installation")
	}
	info.Settings = nil
	info.Main.Version = "(devel)"
	v, _, d = buildVersion(info)
	if v != version || d != "development" {
		t.Fatal("development build misidentified")
	}
}
