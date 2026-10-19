//go:build unix

package cdp

import (
	"os"
	"os/exec"
	"syscall"
)

// processTree is the handle Close uses to take down every process the
// browser started, not only the one exec started. On unix the browser leads
// a process group of its own, which its zygote, renderer, GPU and utility
// children inherit, as does the child of a launcher script that stays in
// front of the browser instead of exec'ing it.
type processTree struct {
	process *os.Process
}

// startProcessTree starts cmd as the leader of a new process group.
func startProcessTree(cmd *exec.Cmd) (*processTree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processTree{process: cmd.Process}, nil
}

// kill sends SIGKILL to the whole process group and returns at once. The
// group id is the leader's pid, which cannot be reused before wait reaps
// the leader, so the signal cannot stray to an unrelated group.
func (t *processTree) kill() {
	if err := syscall.Kill(-t.process.Pid, syscall.SIGKILL); err != nil {
		_ = t.process.Kill()
	}
}

// wait reaps the browser process; the rest of the group is reparented to
// init, which reaps it. Nothing else is held on unix.
func (t *processTree) wait() {
	_, _ = t.process.Wait()
}
