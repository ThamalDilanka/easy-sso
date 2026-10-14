package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type ownedProcess interface{ OwnsPID(int) bool }

// probeListener checks ownership as well as address. A socket merely accepting
// on the requested port is insufficient evidence: another process may win a race.
func probeListener(port int, child domain.Child) (bool, error) {
	owner, ok := child.(ownedProcess)
	if !ok {
		return false, errors.New("forwarding listener ownership cannot be verified on this backend")
	}
	switch runtime.GOOS {
	case "linux":
		return linuxListener(port, owner)
	case "windows":
		return windowsListener(port, owner)
	case "darwin":
		return macListener(port, owner)
	default:
		return false, errors.New("forwarding listener verification unsupported on this platform")
	}
}
func loopback(address string) bool {
	ip := net.ParseIP(strings.Trim(address, "[]"))
	return ip != nil && ip.IsLoopback()
}
func linuxListener(port int, owner ownedProcess) (bool, error) {
	inodes := map[string]bool{}
	for _, name := range []string{"tcp", "tcp6"} {
		b, e := os.ReadFile("/proc/net/" + name)
		if e != nil {
			return false, errors.New("cannot inspect forwarding listener addresses")
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 10 || f[3] != "0A" {
				continue
			}
			a := strings.Split(f[1], ":")
			if len(a) != 2 {
				continue
			}
			p, _ := strconv.ParseInt(a[1], 16, 32)
			if int(p) != port {
				continue
			}
			host := a[0]
			isLoop := false
			if name == "tcp" {
				v, _ := strconv.ParseUint(host, 16, 32)
				isLoop = (v & 255) == 127
			} else {
				isLoop = host == "00000000000000000000000001000000" || strings.HasPrefix(host, "0000000000000000FFFF0000") && strings.HasSuffix(host, "7F")
			}
			if !isLoop {
				return false, errors.New("unsafe forwarding listener address; public binding refused")
			}
			inodes[f[9]] = false
		}
	}
	if len(inodes) == 0 {
		return false, nil
	}
	entries, e := os.ReadDir("/proc")
	if e != nil {
		return false, e
	}
	for _, en := range entries {
		pid, e := strconv.Atoi(en.Name())
		if e != nil || !owner.OwnsPID(pid) {
			continue
		}
		fds, _ := os.ReadDir(filepath.Join("/proc", en.Name(), "fd"))
		for _, fd := range fds {
			link, _ := os.Readlink(filepath.Join("/proc", en.Name(), "fd", fd.Name()))
			for inode := range inodes {
				if link == "socket:["+inode+"]" {
					inodes[inode] = true
				}
			}
		}
	}
	for _, owned := range inodes {
		if !owned {
			return false, errors.New("port race: listener is owned by another process")
		}
	}
	return true, nil
}
func windowsListener(port int, owner ownedProcess) (bool, error) {
	script := fmt.Sprintf(`@(Get-NetTCPConnection -State Listen -LocalPort %d -ErrorAction SilentlyContinue | Select-Object LocalAddress,OwningProcess) | ConvertTo-Json -Compress`, port)
	b, e := inspectOutput("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if e != nil {
		return false, errors.New("cannot inspect Windows listener ownership")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return false, nil
	}
	var rows []struct {
		LocalAddress  string
		OwningProcess int
	}
	if e = json.Unmarshal(b, &rows); e != nil {
		var one struct {
			LocalAddress  string
			OwningProcess int
		}
		if e = json.Unmarshal(b, &one); e != nil {
			return false, errors.New("cannot parse Windows listener metadata")
		}
		rows = append(rows, one)
	}
	for _, r := range rows {
		if !loopback(r.LocalAddress) {
			return false, errors.New("unsafe forwarding listener address; public binding refused")
		}
		if !owner.OwnsPID(r.OwningProcess) {
			return false, errors.New("port race: listener is owned by another process")
		}
	}
	return len(rows) > 0, nil
}
func macListener(port int, owner ownedProcess) (bool, error) {
	b, e := inspectOutput("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpn")
	if e != nil {
		if x, ok := e.(*exec.ExitError); ok && x.ExitCode() == 1 {
			return false, nil
		}
		return false, errors.New("lsof is required to inspect macOS listener ownership")
	}
	pid := 0
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(line[1:])
		}
		if strings.HasPrefix(line, "n") {
			host, _, e := net.SplitHostPort(line[1:])
			if e != nil || !loopback(host) {
				return false, errors.New("unsafe forwarding listener address; public binding refused")
			}
			if !owner.OwnsPID(pid) {
				return false, errors.New("port race: listener is owned by another process")
			}
			found = true
		}
	}
	return found, nil
}

type inspectionBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *inspectionBuffer) Len() int      { return b.buffer.Len() }
func (b *inspectionBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *inspectionBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 65536 - b.Len()
	if n > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
func inspectOutput(path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var b inspectionBuffer
	cmd.Stdout = &b
	cmd.Stderr = &inspectionBuffer{}
	e := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("listener inspection timed out")
	}
	if b.exceeded {
		return nil, errors.New("listener inspection exceeded output limit")
	}
	return b.Bytes(), e
}
