package childprocess

import (
	"errors"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var processJobs sync.Map
var resumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// Start assigns the suspended process to a private job before any of its code
// runs. Every descendant inherits the job; cancellation closes its last handle.
func Start(cmd *exec.Cmd) error {
	configurePlatformCommand(cmd)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return err
	}
	processJobs.Store(cmd, job)
	status, _, _ := resumeProcess.Call(uintptr(process))
	if status != 0 {
		closeJob(cmd)
		cmd.Wait()
		return errors.New("could not resume job process")
	}
	return nil
}
func closeJob(cmd *exec.Cmd) bool {
	if job, ok := processJobs.LoadAndDelete(cmd); ok {
		windows.CloseHandle(job.(windows.Handle))
		return true
	}
	return false
}
func Wait(cmd *exec.Cmd) error { defer closeJob(cmd); return cmd.Wait() }
func Run(cmd *exec.Cmd) error {
	if err := Start(cmd); err != nil {
		return err
	}
	return Wait(cmd)
}
