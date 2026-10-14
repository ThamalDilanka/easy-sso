// Package ipc implements private, nonce-authenticated local helper exchanges.
// Snapshots are exchanged in memory only; they are never written to registrations.
package ipc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type Endpoint struct {
	Network string `json:"network"`
	Address string `json:"address"`
	Token   string `json:"token"`
}
type request struct {
	Token  string `json:"token"`
	Action string `json:"action"`
}
type reply struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}
type Server struct {
	Endpoint   Endpoint
	l          net.Listener
	once       sync.Once
	done       chan struct{}
	acceptDone chan struct{}
	workers    sync.WaitGroup
	socketDir  string
}

func PrivateDir(path string) error { return store.EnsureDir(path) }
func WriteJSON(path string, data any) error {
	b, e := json.Marshal(data)
	if e != nil {
		return e
	}
	return store.AtomicWrite(path, b)
}
func ReadJSON(path string, out any) error {
	if e := store.CheckPath(path); e != nil {
		return e
	}
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe IPC registration file")
	}
	if e = store.ValidatePrivate(path); e != nil {
		return e
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func Listen(dir, name string, handler func(string) (any, error)) (*Server, error) {
	if e := PrivateDir(dir); e != nil {
		return nil, e
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return nil, e
	}
	ep := Endpoint{Network: "unix", Address: filepath.Join(dir, "s-"+hex.EncodeToString(b[:8])+".sock"), Token: hex.EncodeToString(b)}
	// Local TCP is used only on Windows, with a cryptographic nonce stored in a
	// current-user ACL protected registration. Unix uses a permissioned socket.
	if runtime.GOOS == "windows" {
		ep.Network = "tcp"
		ep.Address = "127.0.0.1:0"
	}
	socketDir := ""
	if ep.Network == "unix" && len(ep.Address) > 100 {
		base, e := filepath.EvalSymlinks(os.TempDir())
		if e != nil {
			return nil, e
		}
		if len(base) > 60 {
			base, e = filepath.EvalSymlinks("/tmp")
			if e != nil {
				return nil, e
			}
		}
		socketDir, e = os.MkdirTemp(base, "easy-sso-ipc-")
		if e != nil {
			return nil, e
		}
		if e = secure(socketDir, true); e != nil {
			os.Remove(socketDir)
			return nil, e
		}
		ep.Address = filepath.Join(socketDir, "s")
	}
	l, e := net.Listen(ep.Network, ep.Address)
	if e != nil {
		if socketDir != "" {
			os.Remove(socketDir)
		}
		return nil, e
	}
	if ep.Network == "tcp" {
		ep.Address = l.Addr().String()
	} else if e = secure(ep.Address, false); e != nil {
		l.Close()
		return nil, e
	}
	s := &Server{Endpoint: ep, l: l, done: make(chan struct{}), acceptDone: make(chan struct{}), socketDir: socketDir}
	go func() {
		defer close(s.acceptDone)
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(15 * time.Second))
				var req request
				if json.NewDecoder(io.LimitReader(c, 65537)).Decode(&req) != nil {
					return
				}
				if subtle.ConstantTimeCompare([]byte(req.Token), []byte(ep.Token)) != 1 {
					_ = json.NewEncoder(c).Encode(reply{Error: "IPC authentication failed"})
					return
				}
				data, e := handler(req.Action)
				r := reply{}
				if e != nil {
					r.Error = e.Error()
				} else {
					r.Data, _ = json.Marshal(data)
				}
				_ = json.NewEncoder(c).Encode(r)
			}()
		}
	}()
	return s, nil
}
func (s *Server) Close() error {
	var e error
	s.once.Do(func() {
		close(s.done)
		e = s.l.Close()
		<-s.acceptDone
		s.workers.Wait()
		if s.Endpoint.Network == "unix" {
			_ = os.Remove(s.Endpoint.Address)
			if s.socketDir != "" {
				_ = os.Remove(s.socketDir)
			}
		}
	})
	return e
}
func Call(ep Endpoint, action string, out any) error {
	if ep.Network != "unix" && ep.Network != "tcp" {
		return errors.New("invalid IPC endpoint")
	}
	if ep.Network == "tcp" {
		host, _, e := net.SplitHostPort(ep.Address)
		if e != nil || host != "127.0.0.1" {
			return errors.New("IPC must be local")
		}
	}
	c, e := net.DialTimeout(ep.Network, ep.Address, 2*time.Second)
	if e != nil {
		return errors.New("helper unavailable; session status unknown")
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if e = json.NewEncoder(c).Encode(request{Token: ep.Token, Action: action}); e != nil {
		return e
	}
	var r reply
	if e = json.NewDecoder(io.LimitReader(c, 65537)).Decode(&r); e != nil {
		return e
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	if out != nil {
		return json.Unmarshal(r.Data, out)
	}
	return nil
}
