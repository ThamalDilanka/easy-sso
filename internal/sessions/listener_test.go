package sessions

import (
	"os/exec"
	"testing"
	"time"
)

func TestListenerInspectionBoundsExternalOutputAndTime(t *testing.T) {
	if _, e := exec.LookPath("python3"); e != nil {
		t.Skip("python3 prerequisite")
	}
	if _, e := inspectOutput("python3", "-c", `print("X"*100000)`); e == nil {
		t.Fatal("external inspection output limit bypassed")
	}
	start := time.Now()
	if _, e := inspectOutput("python3", "-c", `import time; time.sleep(30)`); e == nil {
		t.Fatal("hung inspector accepted")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("inspection deadline failed")
	}
}
