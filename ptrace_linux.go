package watchmaker

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"slices"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

// If it's on 64-bit platform, `^uintptr(0)` will get a 64-bit number full of one.
// After shifting right for 63-bit, only 1 will be left. Than we got 8 here.
// If it's on 32-bit platform, After shifting nothing will be left. Than we got 4 here.
const ptrSize = 4 << (^uintptr(0) >> 63)

var threadRetryLimit = 10

// TracedProgram is a program traced by ptrace
type TracedProgram struct {
	pid     int
	tids    []int
	Entries []Entry

	backupRegs *unix.PtraceRegs
	backupCode []byte
	// Preserve an unexpected signal-delivery stop until registers/code have
	// been restored and the main thread is detached.
	detachSignal int
}

// Pid return the pid of traced program
func (p *TracedProgram) Pid() int {
	return p.pid
}

func waitStopped(pid int) (unix.WaitStatus, error) {
	for {
		var status unix.WaitStatus
		wpid, err := unix.Wait4(pid, &status, unix.WALL, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if wpid != pid || !status.Stopped() {
			return status, fmt.Errorf("thread %d did not stop: waitpid=%d status=%#x", pid, wpid, uint32(status))
		}
		return status, nil
	}
}

// A signal-delivery stop can race with PTRACE_INTERRUPT. Deliver it before
// changing registers, then wait for the interrupt stop rather than losing the
// signal or executing its handler with the temporary syscall registers.
func interruptThread(tid int) (int, error) {
	if err := unix.PtraceInterrupt(tid); err != nil {
		return 0, err
	}
	for attempts := 0; ; attempts++ {
		status, err := waitStopped(tid)
		if err != nil {
			return 0, err
		}
		event := uint32(status) >> 16
		if event == unix.PTRACE_EVENT_STOP {
			return 0, nil
		}
		if event != 0 {
			return 0, fmt.Errorf("unexpected ptrace event while stopping thread %d: %#x", tid, uint32(status))
		}
		signal := int(status.StopSignal())
		if attempts == 127 {
			// Leave this thread stopped so cleanup can detach and deliver the
			// signal with its original siginfo, even under a signal storm.
			return signal, fmt.Errorf("too many signal stops while interrupting thread %d", tid)
		}
		if err := unix.PtraceCont(tid, signal); err != nil {
			return signal, err
		}
		if err := unix.PtraceInterrupt(tid); err != nil {
			return 0, err
		}
	}
}

func detachThread(tid, signal int) error {
	_, _, errno := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_DETACH, uintptr(tid), 0, uintptr(signal), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// Trace ptrace all threads of a process
func Trace(pid int) (*TracedProgram, error) {
	traceSuccess := false

	tidMap := make(map[int]bool)
	var attachedTids []int
	pendingSignals := make(map[int]int)

	// 定义统一清理逻辑：如果 traceSuccess 未被置为 true，则对所有已 attach 的线程进行 detach
	defer func() {
		if !traceSuccess {
			for _, tid := range attachedTids {
				if err := detachThread(tid, pendingSignals[tid]); err != nil && !errors.Is(err, unix.ESRCH) {
					log.Println("detach failed", "tid", tid, "error", err)
				}
			}
		}
	}()

	// 循环遍历线程组，直到线程数稳定不再增加
	for {
		threads, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
		if err != nil {
			return nil, err
		}

		subset := true
		tids := make(map[int]bool)
		for _, thread := range threads {
			var tid64 int64
			tid64, err = strconv.ParseInt(thread.Name(), 10, 32)
			if err != nil {
				return nil, err
			}
			tid := int(tid64)

			if tidMap[tid] {
				tids[tid] = true
				continue
			}
			subset = false

			err = unix.PtraceSeize(tid)
			if err != nil {
				return nil, err
			}

			// Record the seized thread even if interrupting it subsequently fails.
			attachedTids = append(attachedTids, tid)

			pendingSignals[tid], err = interruptThread(tid)
			if err != nil {
				return nil, err
			}

			log.Println("attach successfully, process task id", tid)
			tids[tid] = true
			tidMap[tid] = true
		}

		if subset {
			tidMap = tids
			break
		}
	}

	// 将 tidMap 中的 key 转换为 slice
	var tidsList []int
	for tid := range tidMap {
		tidsList = append(tidsList, tid)
	}

	slices.Sort(tidsList)

	entries, err := ReadMaps(pid)
	if err != nil {
		return nil, err
	}

	program := &TracedProgram{
		pid:        pid,
		tids:       tidsList,
		Entries:    entries,
		backupRegs: &unix.PtraceRegs{},
		backupCode: make([]byte, unixInstrSize),
	}

	traceSuccess = true

	return program, nil
}

// Detach detaches from all threads of the processes
func (p *TracedProgram) Detach() error {
	var detachErrors []error
	for _, tid := range p.tids {
		log.Println("detaching, process task id", tid)
		signal := 0
		if tid == p.pid {
			signal = p.detachSignal
		}
		err := detachThread(tid, signal)

		if err != nil && !errors.Is(err, unix.ESRCH) {
			detachErrors = append(detachErrors, fmt.Errorf("detach thread %d: %w", tid, err))
		}
	}
	if len(detachErrors) != 0 {
		return errors.Join(detachErrors...)
	}
	log.Println("Successfully detach and rerun process, pid", p.pid)
	return nil
}

// Protect will backup regs and rip into fields
func (p *TracedProgram) Protect() error {
	err := getRegs(p.pid, p.backupRegs)
	if err != nil {
		return err
	}

	_, err = unix.PtracePeekData(p.pid, getIp(p.backupRegs), p.backupCode)
	if err != nil {
		return err
	}

	return nil
}

// Restore will restore regs and rip from fields
func (p *TracedProgram) Restore() error {
	_, codeErr := unix.PtracePokeData(p.pid, getIp(p.backupRegs), p.backupCode)
	return errors.Join(codeErr, setRegs(p.pid, p.backupRegs))
}

// Linux uses a 64-bit kernel sigset_t on both supported architectures, rather
// than libc's larger sigset_t. Pending signals must remain queued while the
// tracee runs a remote syscall with its original code/registers overwritten.
func ptraceSignalMask(request int, pid int, mask *uint64) error {
	_, _, errno := unix.Syscall6(unix.SYS_PTRACE, uintptr(request), uintptr(pid),
		unsafe.Sizeof(*mask), uintptr(unsafe.Pointer(mask)), 0, 0)
	if errno != 0 {
		return fmt.Errorf("ptrace signal mask for thread %d: %w", pid, errno)
	}
	return nil
}

func (p *TracedProgram) protectSyscall() (func() error, error) {
	if err := p.Protect(); err != nil {
		return nil, err
	}
	var savedMask uint64
	if err := ptraceSignalMask(unix.PTRACE_GETSIGMASK, p.pid, &savedMask); err != nil {
		return nil, err
	}
	blockedMask := ^uint64(0)
	if err := ptraceSignalMask(unix.PTRACE_SETSIGMASK, p.pid, &blockedMask); err != nil {
		return nil, err
	}
	return func() error {
		return errors.Join(p.Restore(), ptraceSignalMask(unix.PTRACE_SETSIGMASK, p.pid, &savedMask))
	}, nil
}

// Wait waits until the process stops
func (p *TracedProgram) Wait() error {
	_, err := waitStopped(p.pid)
	return err
}

// Step moves one step forward
func (p *TracedProgram) Step() error {
	err := unix.PtraceSingleStep(p.pid)
	if err != nil {
		return err
	}

	status, err := waitStopped(p.pid)
	if err != nil {
		return err
	}
	if status.StopSignal() != unix.SIGTRAP || status.TrapCause() != 0 {
		if uint32(status)>>16 == 0 {
			p.detachSignal = int(status.StopSignal())
		}
		return fmt.Errorf("thread %d stopped before completing single step: status=%#x", p.pid, uint32(status))
	}
	return nil
}

// tryMmap reports whether the syscall completed. A ptrace failure must not be
// retried: the thread may be waiting for a signal that detach needs to deliver.
func (p *TracedProgram) tryMmap(addr, length, prot, flags, fd, offset uint64) (uint64, bool, error) {
	log.Printf("[MMAP DEBUG] attempting mmap(): len=%d, prot=%#x, flags=%#x, fd=%d, offset=%d, syscall_nr=%d", length, prot, flags, fd, offset, unix.SYS_MMAP)

	result, err := p.Syscall(unix.SYS_MMAP, addr, length, prot, flags, fd, offset)
	log.Printf("[MMAP DEBUG] mmap syscall returned: result=%#x, err=%v\n", result, err)
	if err != nil {
		return 0, false, err
	}

	// check if result indicates error
	if result == 0 {
		return 0, true, fmt.Errorf("mmap returned NULL address")
	}

	if result > 0xFFFFFFFF00000000 { // likely an error code (negative values in 64-bit)
		return 0, true, fmt.Errorf("mmap returned error code: 0x%x", result)
	}

	return result, true, nil
}

// Mmap runs mmap syscall with fallback strategies for arm64
func (p *TracedProgram) Mmap(length uint64, fd uint64) (uint64, error) {
	pageSize := uint64(os.Getpagesize())
	alignedLength := (length + pageSize - 1) & ^(pageSize - 1) // round up to page boundary

	log.Printf("[MMAP DEBUG] using aligned len=%d instead of original %d", alignedLength, length)

	// Strategy 1: standard mmap call (size aligned)
	result, completed, err := p.tryMmap(0, alignedLength, unix.PROT_READ|unix.PROT_WRITE|unix.PROT_EXEC, unix.MAP_ANON|unix.MAP_PRIVATE, fd, 0)
	if !completed {
		return 0, err
	}
	if err == nil && result != 0 {
		log.Printf("[MMAP DEBUG] strategy 1 (standard) succeeded: address=%#x", result)
		return result, nil
	}
	log.Printf("[MMAP DEBUG] strategy 1 failed: %v, result=0x%x", err, result)
	firstErr := err

	// Strategy 2: larger allocation (2 pages minimum)
	largerLength := alignedLength
	if largerLength < 2*pageSize {
		largerLength = 2 * pageSize
	}
	result, _, err = p.tryMmap(0, largerLength, unix.PROT_READ|unix.PROT_WRITE|unix.PROT_EXEC, unix.MAP_ANON|unix.MAP_PRIVATE, fd, 0)
	if err == nil && result != 0 {
		log.Printf("[MMAP DEBUG] strategy 2 (larger allocation) succeeded: address=%#x, allocated=%d", result, largerLength)
		return result, nil
	}
	log.Printf("[MMAP DEBUG] strategy 2 failed: %v, result=0x%x", err, result)

	// TODO: Strategy 3: map without EXEC and enable it with MPROTECT?

	// all strategies failed
	return 0, fmt.Errorf("all mmap strategies failed: %w", errors.Join(firstErr, err))
}

// ReadSlice reads from addr and return a slice
func (p *TracedProgram) ReadSlice(addr uint64, size uint64) (*[]byte, error) {
	buffer := make([]byte, size)

	// constructs a local iovec (a description of a memory buffer)
	localIov := []unix.Iovec{
		{
			Base: &buffer[0],
			Len:  size,
		},
	}

	// construct remote iovec (target process memory address and length)
	remoteIov := []unix.RemoteIovec{
		{
			Base: uintptr(addr),
			Len:  int(size),
		},
	}

	// calling the ProcessVMReadv system call
	_, err := unix.ProcessVMReadv(p.pid, localIov, remoteIov, 0)
	if err != nil {
		return nil, err
	}

	return &buffer, nil
}

// WriteSlice writes a buffer into addr
func (p *TracedProgram) WriteSlice(addr uint64, buffer []byte) error {
	size := len(buffer)

	// construct a local iovec (pointing to the data we want to write)
	localIov := []unix.Iovec{
		{
			Base: &buffer[0],
			Len:  uint64(size),
		},
	}

	// construct a remote iovec (the memory area to be written in the target process)
	remoteIov := []unix.RemoteIovec{
		{
			Base: uintptr(addr),
			Len:  size,
		},
	}

	// call ProcessVMWritev to write data to the target process memory
	_, err := unix.ProcessVMWritev(p.pid, localIov, remoteIov, 0)
	if err != nil {
		return err
	}

	return nil
}

func alignBuffer(buffer []byte) []byte {
	if buffer == nil {
		return nil
	}

	alignedSize := (len(buffer) / ptrSize) * ptrSize
	if alignedSize < len(buffer) {
		alignedSize += ptrSize
	}
	clonedBuffer := make([]byte, alignedSize)
	copy(clonedBuffer, buffer)

	return clonedBuffer
}

// PtraceWriteSlice uses ptrace rather than process_vm_write to write a buffer into addr
func (p *TracedProgram) PtraceWriteSlice(addr uint64, buffer []byte) error {
	wroteSize := 0

	buffer = alignBuffer(buffer)

	for wroteSize+ptrSize <= len(buffer) {
		_addr := uintptr(addr + uint64(wroteSize))
		data := buffer[wroteSize : wroteSize+ptrSize]

		_, err := unix.PtracePokeData(p.pid, _addr, data)
		if err != nil {
			return fmt.Errorf("%T write to addr %x with %+v failed", err, addr, data)
		}

		wroteSize += ptrSize
	}

	return nil
}

// GetLibBuffer reads an entry
func (p *TracedProgram) GetLibBuffer(entry *Entry) (*[]byte, error) {
	if entry.PaddingSize > 0 {
		return nil, fmt.Errorf("entry with padding size is not supported")
	}

	size := entry.EndAddress - entry.StartAddress

	return p.ReadSlice(entry.StartAddress, size)
}

// MmapSlice mmaps a slice and return it's addr
func (p *TracedProgram) MmapSlice(slice []byte) (*Entry, error) {
	size := uint64(len(slice))

	addr, err := p.Mmap(size, 0)
	if err != nil {
		return nil, err
	}

	err = p.WriteSlice(addr, slice)
	if err != nil {
		return nil, err
	}

	return &Entry{
		StartAddress: addr,
		EndAddress:   addr + size,
		Privilege:    "rwxp",
		PaddingSize:  0,
		Path:         "",
	}, nil
}

// FindSymbolInEntry finds symbol in entry through parsing elf
func (p *TracedProgram) FindSymbolInEntry(symbolName string, entry *Entry) (uint64, uint64, error) {
	log.Printf("[SYMBOL DEBUG] looking for symbol '%s'", symbolName)

	libBuffer, err := p.GetLibBuffer(entry)
	if err != nil {
		return 0, 0, err
	}

	reader := bytes.NewReader(*libBuffer)
	vdsoElf, err := elf.NewFile(reader)
	if err != nil {
		return 0, 0, err
	}

	loadOffset := uint64(0)

	for _, prog := range vdsoElf.Progs {
		if prog.Type == elf.PT_LOAD {
			loadOffset = prog.Vaddr - prog.Off
			log.Printf("[SYMBOL DEBUG] loadOffset=%#x", loadOffset)

			// break here is enough for vdso
			break
		}
	}

	symbols, err := vdsoElf.DynamicSymbols()
	if err != nil {
		return 0, 0, err
	}
	for _, symbol := range symbols {
		offset := symbol.Value
		location := entry.StartAddress + (offset - loadOffset)
		log.Printf("[SYMBOL DEBUG] seeing '%s' with len=%d and offset=%#x at %#x", symbol.Name, symbol.Size, offset, location)

		// try direct match first
		if symbol.Name == symbolName {
			log.Printf("[SYMBOL DEBUG] found '%s' at %#x", symbol.Name, location)
			return location, symbol.Size, nil
		}

		// on arm64 try with "__kernel_" prefix
		if runtime.GOARCH == "arm64" {
			targetSymbol := "__kernel_" + symbolName
			if symbol.Name == targetSymbol {
				log.Printf("[SYMBOL DEBUG] found '%s' as '%s' at %#x", symbolName, symbol.Name, location)
				return location, symbol.Size, nil
			}
		}

		/*
			TODO: should we try "__vdso_" prefix as well? On amd64 you could get e.g. this:
			2025/06/07 08:57:15 ptrace_linux.go:395: [SYMBOL DEBUG] looking for symbol 'gettimeofday'
			2025/06/07 08:57:15 ptrace_linux.go:413: [SYMBOL DEBUG] loadOffset=0x0
			2025/06/07 08:57:15 ptrace_linux.go:425: [SYMBOL DEBUG] found 'clock_gettime' with len=5 at 0xe40
			2025/06/07 08:57:15 ptrace_linux.go:425: [SYMBOL DEBUG] found '__vdso_gettimeofday' with len=5 at 0xe00
			2025/06/07 08:57:15 ptrace_linux.go:425: [SYMBOL DEBUG] found 'clock_getres' with len=117 at 0xe50
			2025/06/07 08:57:15 ptrace_linux.go:425: [SYMBOL DEBUG] found '__vdso_clock_getres' with len=117 at 0xe50
			2025/06/07 08:57:15 ptrace_linux.go:425: [SYMBOL DEBUG] found 'gettimeofday' with len=5 at 0xe00
		*/
	}
	return 0, 0, fmt.Errorf("cannot find symbol '%s'", symbolName)
}

// WriteUint64ToAddr writes uint64 to addr
func (p *TracedProgram) WriteUint64ToAddr(addr uint64, value uint64) error {
	valueSlice := make([]byte, 8)
	endian.PutUint64(valueSlice, value)
	err := p.WriteSlice(addr, valueSlice)
	return err
}
