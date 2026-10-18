package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEffectivePathsAndMissingPrerequisiteBrowsing(t *testing.T) {
	d := t.TempDir()
	t.Setenv("HOME", d)
	t.Setenv("USERPROFILE", d)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(d, "custom-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(d, "custom-creds"))
	t.Setenv("PATH", d)
	p, e := paths(options{State: filepath.Join(d, "state"), CLI: filepath.Join(d, "missing-cli")})
	if e != nil {
		t.Fatal(e)
	}
	if p.Ready || p.ConfigPath != filepath.Join(d, "custom-config") || p.CredentialsPath != filepath.Join(d, "custom-creds") || len(p.Problems) < 2 {
		t.Fatal(p)
	}
	if _, e = os.Stat(p.StateDir); !os.IsNotExist(e) {
		t.Fatal("read-only diagnostics created application state")
	}
}
func TestDiagnosticsReportSharedAWSDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics")
	}
	d := t.TempDir()
	aws := filepath.Join(d, ".aws")
	os.Mkdir(aws, 0755)
	t.Setenv("HOME", d)
	t.Setenv("AWS_CONFIG_FILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	t.Setenv("PATH", d)
	p, e := paths(options{State: filepath.Join(d, "state"), CLI: filepath.Join(d, "missing-cli")})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, problem := range p.Problems {
		found = found || (strings.Contains(problem, aws) && strings.Contains(problem, "chmod 700"))
	}
	if !found {
		t.Fatal("shared AWS directory not reported:", p.Problems)
	}
}
func TestVersionAndArgumentValidation(t *testing.T) {
	if e := run([]string{"version"}); e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"--help"}); e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"internal-helper"}); e == nil {
		t.Fatal("helper accepted missing run parameters")
	}
	if e := run([]string{"unexpected"}); e == nil {
		t.Fatal("unexpected args accepted")
	}
}
func TestVersionChecksDoNotAcceptWrongTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	d := t.TempDir()
	f := filepath.Join(d, "tool")
	os.WriteFile(f, []byte("#!/bin/sh\nprintf 'aws-cli/1.0\\n'\n"), 0700)
	if _, ok := probeVersionInfo(f, "aws-cli/2."); ok {
		t.Fatal("CLI v1 accepted")
	}
	os.WriteFile(f, []byte("#!/bin/sh\nprintf 'aws-cli/2.37.0 TEST\\n'\n"), 0700)
	if version, ok := probeVersionInfo(f, "aws-cli/2."); !ok || version != "2.37.0" {
		t.Fatal("wrong displayed version", version, ok)
	}
	os.WriteFile(f, []byte("#!/bin/sh\nprintf '1.2.707.0\\n'\n"), 0700)
	if version, ok := probeVersionInfo(f, ""); !ok || version != "1.2.707.0" {
		t.Fatal("wrong plugin version", version, ok)
	}
}
