//go:build windows

package supervise

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
}

func childAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// tree is a Job Object with KILL_ON_JOB_CLOSE owned by the supervisor: if the supervisor dies the
// OS kills the agent and all its descendants.
type tree struct{ job windows.Handle }

func newTree(p *os.Process) *tree {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return &tree{}
	}
	var li windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	li.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&li)), uint32(unsafe.Sizeof(li))); err != nil {
		windows.CloseHandle(job)
		return &tree{}
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err == nil {
		windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	return &tree{job: job}
}

func (t *tree) close() {
	if t.job != 0 {
		windows.CloseHandle(t.job)
	}
}

// kill terminates the whole job at once (there is no graceful signal for console trees).
func (t *tree) kill(_ <-chan struct{}, _ time.Duration) {
	if t.job != 0 {
		windows.TerminateJobObject(t.job, 1)
	}
}

func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}

// procStart returns the process creation time (FILETIME) as a string.
func procStart(pid int) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return "", false
	}
	return strconv.FormatInt(c.Nanoseconds(), 10), true
}

func exitInfo(ps *os.ProcessState) (int, *string) { return ps.ExitCode(), nil }

// forceKill is the hub-side fallback: kill the supervisor so its job handle closes and the OS
// kills the agent tree.
func forceKill(in *info) {
	if in.PID <= 0 || !pidAlive(in.PID) {
		return
	}
	if in.SupStart != "" {
		if st, ok := procStart(in.PID); !ok || st != in.SupStart {
			return // pid reused
		}
	}
	if p, err := os.FindProcess(in.PID); err == nil {
		p.Kill()
	}
}

func lastResort(pid int) {
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
