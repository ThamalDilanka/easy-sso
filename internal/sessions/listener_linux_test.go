//go:build linux

package sessions

import (
	"net"
	"os"
	"testing"
)

type testOwner struct{ pid int }

func (o testOwner) OwnsPID(pid int) bool { return o.pid == pid }
func TestActualLinuxLoopbackAndSocketOwnership(t *testing.T) {
	for _, x := range []struct {
		address string
		owner   int
		good    bool
	}{{"127.0.0.1:0", os.Getpid(), true}, {"127.0.0.1:0", 0, false}, {"0.0.0.0:0", os.Getpid(), false}} {
		l, e := net.Listen("tcp", x.address)
		if e != nil {
			t.Fatal(e)
		}
		ready, e := linuxListener(l.Addr().(*net.TCPAddr).Port, testOwner{x.owner})
		_ = l.Close()
		if x.good && (e != nil || !ready) {
			t.Fatal(ready, e)
		}
		if !x.good && e == nil {
			t.Fatal("unsafe/foreign listener accepted")
		}
	}
}
