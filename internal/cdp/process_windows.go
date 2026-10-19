//go:build windows

package cdp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree is the handle Close uses to take down every process the
// browser started, not only the one exec started. On Windows the browser
// runs inside a job object; every process it starts is born into the job
// (Chromium nests its own sandbox jobs inside it), and the job terminates
// them all at once. The job is also set to kill its processes when its last
// handle closes, so a server that dies takes its browser with it.
type processTree struct {
	process *os.Process
	job     windows.Handle
}

// startProcessTree starts cmd inside a fresh job object. The process starts
// suspended and is resumed only once it belongs to the job, so not even its
// first child can be born outside it.
func startProcessTree(cmd *exec.Cmd) (*processTree, error) {
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	tree := &processTree{process: cmd.Process, job: job}

	if err := tree.adopt(); err != nil {
		tree.kill()
		go tree.wait()
		return nil, fmt.Errorf("placing the browser in its job object: %w", err)
	}
	if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
		tree.kill()
		go tree.wait()
		return nil, fmt.Errorf("resuming the browser: %w", err)
	}
	return tree, nil
}

// newKillOnCloseJob creates a job object whose processes die with its last
// handle.
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("creating the browser's job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("configuring the browser's job object: %w", err)
	}
	return job, nil
}

// adopt assigns the (still suspended) browser process to the job.
func (t *processTree) adopt() error {
	// os.Process keeps its own handle private; assignment needs one with
	// quota and terminate rights, opened by pid while the process is alive.
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(t.process.Pid))
	if err != nil {
		return fmt.Errorf("opening the browser process: %w", err)
	}
	defer windows.CloseHandle(handle)
	return windows.AssignProcessToJobObject(t.job, handle)
}

// resumeProcess resumes the initial thread of a process started with
// CREATE_SUSPENDED. os/exec closes the thread handle CreateProcess returned,
// so the thread is found again through a system snapshot.
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("listing threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	resumed := false
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("opening thread %d: %w", entry.ThreadID, err)
		}
		_, err = windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if err != nil {
			return fmt.Errorf("resuming thread %d: %w", entry.ThreadID, err)
		}
		resumed = true
	}
	if !resumed {
		return errors.New("the process has no thread to resume")
	}
	return nil
}

// kill terminates every process in the job and returns at once.
func (t *processTree) kill() {
	if err := windows.TerminateJobObject(t.job, 1); err != nil {
		_ = t.process.Kill()
	}
}

// wait reaps the browser process, then closes the job handle, which
// terminates anything in the job that is somehow still alive.
func (t *processTree) wait() {
	_, _ = t.process.Wait()
	_ = windows.CloseHandle(t.job)
}
