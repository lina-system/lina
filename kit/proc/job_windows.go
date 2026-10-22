// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

// A job is inherited by every descendant, independently of console/process
// groups. Neither BREAKAWAY_OK nor SILENT_BREAKAWAY_OK is enabled. Start the
// leader suspended so no user code can fork before assignment succeeds.
type jobProcess struct {
	cmd         *exec.Cmd
	job, leader syscall.Handle
}

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	createJob    = kernel32.NewProc("CreateJobObjectW")
	setJob       = kernel32.NewProc("SetInformationJobObject")
	assignJob    = kernel32.NewProc("AssignProcessToJobObject")
	terminateJob = kernel32.NewProc("TerminateJobObject")
	queryJob     = kernel32.NewProc("QueryInformationJobObject")
	threadFirst  = kernel32.NewProc("Thread32First")
	threadNext   = kernel32.NewProc("Thread32Next")
	openThread   = kernel32.NewProc("OpenThread")
	resumeThread = kernel32.NewProc("ResumeThread")
)

type jobLimits struct {
	PerProcessUserTime, PerJobUserTime                         int64
	Flags                                                      uint32
	MinimumWorkingSet, MaximumWorkingSet                       uintptr
	ActiveProcessLimit                                         uint32
	Affinity                                                   uintptr
	PriorityClass, SchedulingClass                             uint32
	IOCounters                                                 [6]uint64
	ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory uintptr
}

type jobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime     int64
	PageFaultCount, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
}

type threadEntry struct {
	Size, Usage, ID, OwnerPID   uint32
	BasePriority, DeltaPriority int32
	Flags                       uint32
}

func newProcess(c Command) (process, error) {
	job, _, err := createJob.Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("create process job: %w", err)
	}
	p := &jobProcess{cmd: command(c), job: syscall.Handle(job)}
	limits := jobLimits{Flags: 0x2000} // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if ok, _, err := setJob.Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits)); ok == 0 {
		p.release()
		return nil, fmt.Errorf("set process job limits: %w", err)
	}
	p.cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x4} // CREATE_SUSPENDED
	return p, nil
}

func (p *jobProcess) start() error {
	if err := p.cmd.Start(); err != nil {
		return err
	}
	// Keep a distinct handle: exec.Wait closes os.Process's own handle.
	handle, err := syscall.OpenProcess(0x100001|0x100|0x400, false, uint32(p.cmd.Process.Pid))
	if err == nil {
		p.leader = handle
		if ok, _, callErr := assignJob.Call(uintptr(p.job), uintptr(handle)); ok == 0 {
			err = fmt.Errorf("assign process job: %w", callErr)
		}
	}
	if err == nil {
		err = resumeLeader(uint32(p.cmd.Process.Pid))
	}
	if err != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		return err
	}
	return nil
}

func resumeLeader(pid uint32) error {
	snapshot, err := syscall.CreateToolhelp32Snapshot(0x4, 0) // TH32CS_SNAPTHREAD
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(snapshot)
	entry := threadEntry{Size: uint32(unsafe.Sizeof(threadEntry{}))}
	ok, _, err := threadFirst.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.OwnerPID == pid {
			handle, _, err := openThread.Call(0x2, 0, uintptr(entry.ID)) // THREAD_SUSPEND_RESUME
			if handle == 0 {
				return err
			}
			defer syscall.CloseHandle(syscall.Handle(handle))
			previous, _, err := resumeThread.Call(handle)
			if uint32(previous) == 0xffffffff {
				return err
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, err = threadNext.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("find suspended process thread: %w", err)
}

func (p *jobProcess) kill() error {
	if ok, _, err := terminateJob.Call(uintptr(p.job), 1); ok == 0 {
		return err
	}
	return nil
}

func (p *jobProcess) wait() error {
	joined := make(chan error, 1)
	go func() {
		_, err := syscall.WaitForSingleObject(p.leader, syscall.INFINITE)
		// Kill remaining descendants before exec.Wait joins stdio copies.
		err = errors.Join(err, p.kill())
		if err == nil {
			for {
				var info jobAccounting
				if ok, _, callErr := queryJob.Call(uintptr(p.job), 1, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0); ok == 0 {
					err = callErr
					break
				}
				if info.ActiveProcesses == 0 {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		joined <- err
	}()
	err := p.cmd.Wait()
	return errors.Join(err, <-joined)
}

func (p *jobProcess) release() {
	if p.leader != 0 {
		_ = syscall.CloseHandle(p.leader)
	}
	if p.job != 0 {
		_ = syscall.CloseHandle(p.job)
	}
}
