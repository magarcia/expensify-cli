//go:build !windows

package browserauth

import (
	"os/exec"
	"syscall"
	"time"
)

type processControl struct{}

func configureProcess(command *exec.Cmd) (*processControl, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processControl{}, nil
}

func (*processControl) started(*exec.Cmd) error { return nil }
func (*processControl) close()                  {}

func (*processControl) stop(command *exec.Cmd, done <-chan struct{}) {
	// A private process group also covers Chromium subprocesses if the main
	// process exits before its renderers and storage processes.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	<-done
}
