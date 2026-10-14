//go:build windows

package process

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")
var createJob = kernel.NewProc("CreateJobObjectW")
var setJob = kernel.NewProc("SetInformationJobObject")
var assignJob = kernel.NewProc("AssignProcessToJobObject")
var terminateJob = kernel.NewProc("TerminateJobObject")
var isInJob = kernel.NewProc("IsProcessInJob")
var jobs sync.Map

type basicLimit struct {
	PerProcess, PerJob   int64
	Flags                uint32
	Min, Max             uintptr
	Active               uint32
	Affinity             uintptr
	Priority, Scheduling uint32
}
type ioCounters struct{ Read, Write, Other, ReadBytes, WriteBytes, OtherBytes uint64 }
type extendedLimit struct {
	Basic                                          basicLimit
	IO                                             ioCounters
	ProcessMemory, JobMemory, PeakProcess, PeakJob uintptr
}

func prepare(cmd *exec.Cmd) (func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	return nil, nil
}
func attached(cmd *exec.Cmd, _ func() error) (func() error, error) {
	job, _, e := createJob.Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("create process job: %w", e)
	}
	info := extendedLimit{}
	info.Basic.Flags = 0x2000
	ok, _, e := setJob.Call(job, 9, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, fmt.Errorf("configure process job: %w", e)
	}
	ph, e := syscall.OpenProcess(0x0100|0x0001, false, uint32(cmd.Process.Pid))
	if e != nil {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, e
	}
	defer syscall.CloseHandle(ph)
	ok, _, e = assignJob.Call(job, uintptr(ph))
	if ok == 0 {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, fmt.Errorf("assign process job: %w", e)
	}
	jobs.Store(cmd, syscall.Handle(job))
	if e = resumeMainThread(uint32(cmd.Process.Pid)); e != nil {
		return nil, e
	}
	return func() error {
		ok, _, e := terminateJob.Call(job, 1)
		if ok == 0 {
			return e
		}
		return nil
	}, nil
}

// CREATE_SUSPENDED prevents the CLI from spawning a plugin before it belongs
// to the cleanup Job. Only its suspended primary thread is resumed afterwards.
func resumeMainThread(pid uint32) error {
	snapshot, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if e = windows.Thread32First(snapshot, &entry); e != nil {
		return e
	}
	for {
		if entry.OwnerProcessID == pid {
			thread, e := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if e != nil {
				return e
			}
			defer windows.CloseHandle(thread)
			_, e = windows.ResumeThread(thread)
			return e
		}
		if e = windows.Thread32Next(snapshot, &entry); e != nil {
			break
		}
	}
	return errors.New("suspended CLI primary thread unavailable; launch blocked")
}
func release(cmd *exec.Cmd) {
	if j, ok := jobs.LoadAndDelete(cmd); ok {
		syscall.CloseHandle(j.(syscall.Handle))
	}
}
func ownsPID(cmd *exec.Cmd, pid int) bool {
	j, ok := jobs.Load(cmd)
	if !ok {
		return false
	}
	ph, e := syscall.OpenProcess(0x1000, false, uint32(pid))
	if e != nil {
		return false
	}
	defer syscall.CloseHandle(ph)
	var inside uint32
	okv, _, _ := isInJob.Call(uintptr(ph), uintptr(j.(syscall.Handle)), uintptr(unsafe.Pointer(&inside)))
	return okv != 0 && inside != 0
}
