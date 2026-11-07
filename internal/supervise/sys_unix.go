//go:build !windows

package supervise

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func childAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// tree signals the agent's whole process group (child pid == pgid thanks to Setpgid).
type tree struct{ pgid int }

func newTree(p *os.Process) *tree { return &tree{pgid: p.Pid} }

func (t *tree) close() {}

// kill sends SIGTERM to the group, waits grace for done, then SIGKILLs the group.
func (t *tree) kill(done <-chan struct{}, grace time.Duration) {
	if done != nil && grace > 0 {
		syscall.Kill(-t.pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(grace):
		}
	}
	// always finish the group: members that trap SIGTERM outlive the leader otherwise (ESRCH ignored)
	syscall.Kill(-t.pgid, syscall.SIGKILL)
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// procStart returns the process start time as printed by ps (works on Linux and macOS).
func procStart(pid int) (string, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	s := strings.TrimSpace(string(out))
	return s, err == nil && s != ""
}

func exitInfo(ps *os.ProcessState) (int, *string) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		s := ws.Signal().String()
		switch ws.Signal() {
		case syscall.SIGTERM:
			s = "SIGTERM"
		case syscall.SIGKILL:
			s = "SIGKILL"
		case syscall.SIGINT:
			s = "SIGINT"
		}
		return -1, &s
	}
	return ps.ExitCode(), nil
}

// forceKill is the hub-side fallback: SIGKILL the agent's process group.
func forceKill(in *info) { syscall.Kill(-in.ChildPID, syscall.SIGKILL) }

func lastResort(pid int) { syscall.Kill(pid, syscall.SIGKILL) }
