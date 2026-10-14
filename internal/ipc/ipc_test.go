package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestPrivateAuthenticatedOneUseExchange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	var mu sync.Mutex
	used := false
	s, e := Listen(dir, "startup", func(action string) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if used || action != "start" {
			return nil, errors.New("consumed")
		}
		used = true
		return map[string]string{"snapshot": "in-memory-only"}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	path := filepath.Join(dir, "registration.json")
	if e = WriteJSON(path, s.Endpoint); e != nil {
		t.Fatal(e)
	}
	var ep Endpoint
	if e = ReadJSON(path, &ep); e != nil {
		t.Fatal(e)
	}
	bad := ep
	bad.Token = "wrong"
	if e = Call(bad, "start", nil); e == nil {
		t.Fatal("unauthenticated IPC accepted")
	}
	var result map[string]string
	if e = Call(ep, "start", &result); e != nil || result["snapshot"] != "in-memory-only" {
		t.Fatal(result, e)
	}
	if e = Call(ep, "start", nil); e == nil {
		t.Fatal("startup snapshot exchanged twice")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("registration permissions")
		}
		info, _ = os.Stat(ep.Address)
		if info.Mode().Perm() != 0600 {
			t.Fatal("socket permissions")
		}
	}
}
func TestRejectUnsafeRegistrationAndRemoteEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unsafe.json")
	if e := os.WriteFile(path, []byte(`{"network":"tcp","address":"127.0.0.1:1234","token":"value"}`), 0644); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS != "windows" {
		var ep Endpoint
		if e := ReadJSON(path, &ep); e == nil {
			t.Fatal("public registration accepted")
		}
	}
	if e := Call(Endpoint{Network: "tcp", Address: "203.0.113.1:1234"}, "start", nil); e == nil {
		t.Fatal("remote endpoint accepted")
	}
	if e := Call(Endpoint{Network: "udp", Address: "127.0.0.1:1234"}, "start", nil); e == nil {
		t.Fatal("unsupported IPC transport accepted")
	}
}
