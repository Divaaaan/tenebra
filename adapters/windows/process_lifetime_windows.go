//go:build windows

package windows

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	win "golang.org/x/sys/windows"
)

var procEngineThreadOwner = win.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

type jobChild struct {
	cmd       *exec.Cmd
	job       win.Handle
	closeOnce sync.Once
	closeErr  error
}

func startOwnedCommand(cmd *exec.Cmd) (func() error, error) {
	return startWithLifetime(&jobChild{cmd: cmd})
}

func (c *jobChild) Prepare() error {
	job, err := win.CreateJobObject(nil, nil) // unnamed, non-inheritable, owned only by core
	if err != nil {
		return fmt.Errorf("create engine lifetime job: %w", err)
	}
	c.job = job
	limits := win.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = win.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := win.SetInformationJobObject(job, win.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("set engine lifetime job: %w", err)
	}
	return nil
}

func (c *jobChild) StartSuspended() error {
	attr := new(syscall.SysProcAttr)
	if c.cmd.SysProcAttr != nil {
		*attr = *c.cmd.SysProcAttr
	}
	attr.CreationFlags |= win.CREATE_SUSPENDED | win.CREATE_NO_WINDOW
	attr.HideWindow = true
	c.cmd.SysProcAttr = attr
	return c.cmd.Start()
}

func (c *jobChild) Assign() error {
	// Cmd retains its process handle until Wait, preventing reuse of this PID.
	process, err := win.OpenProcess(win.PROCESS_SET_QUOTA|win.PROCESS_TERMINATE, false, uint32(c.cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open suspended engine: %w", err)
	}
	defer win.CloseHandle(process)
	if err := win.AssignProcessToJobObject(c.job, process); err != nil {
		return fmt.Errorf("assign engine lifetime job: %w", err)
	}
	return nil
}

func (c *jobChild) Resume() error {
	pid := uint32(c.cmd.Process.Pid)
	snapshot, err := win.CreateToolhelp32Snapshot(win.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer win.CloseHandle(snapshot)
	entry := win.ThreadEntry32{Size: uint32(unsafe.Sizeof(win.ThreadEntry32{}))}
	err = win.Thread32First(snapshot, &entry)
	var ids []uint32
	for err == nil {
		if entry.OwnerProcessID == pid {
			ids = append(ids, entry.ThreadID)
		}
		err = win.Thread32Next(snapshot, &entry)
	}
	if !errors.Is(err, win.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("enumerate suspended engine thread: %w", err)
	}
	if len(ids) != 1 {
		return fmt.Errorf("suspended engine has %d threads; startup refused", len(ids))
	}
	thread, err := win.OpenThread(win.THREAD_SUSPEND_RESUME|win.THREAD_QUERY_LIMITED_INFORMATION, false, ids[0])
	if err != nil {
		return err
	}
	defer win.CloseHandle(thread)
	owner, _, ownerErr := procEngineThreadOwner.Call(uintptr(thread))
	if owner == 0 {
		return fmt.Errorf("verify engine thread owner: %w", ownerErr)
	}
	if uint32(owner) != pid {
		return errors.New("suspended engine thread identity changed")
	}
	previous, err := win.ResumeThread(thread)
	if err != nil {
		return fmt.Errorf("resume owned engine: %w", err)
	}
	if previous != 1 {
		return fmt.Errorf("unexpected engine thread suspension count %d", previous)
	}
	return nil
}

func (c *jobChild) Kill() error { return c.cmd.Process.Kill() }
func (c *jobChild) Wait() error { return c.cmd.Wait() }
func (c *jobChild) Close() error {
	c.closeOnce.Do(func() {
		if c.job != 0 {
			c.closeErr = win.CloseHandle(c.job)
			c.job = 0
		}
	})
	return c.closeErr
}
