package watchmaker

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

var endian = binary.LittleEndian

const unixInstrSize = 2

func getIp(regs *unix.PtraceRegs) uintptr {
	return uintptr(regs.Rip)
}

func getRegs(pid int, regsout *unix.PtraceRegs) error {
	err := unix.PtraceGetRegs(pid, regsout)
	if err != nil {
		return fmt.Errorf("get registers of process %d: %w", pid, err)
	}

	return nil
}

func setRegs(pid int, regs *unix.PtraceRegs) error {
	err := unix.PtraceSetRegs(pid, regs)
	if err != nil {
		return fmt.Errorf("set registers of process %d: %w", pid, err)
	}

	return nil
}

// Syscall runs a syscall at main thread of process
func (p *TracedProgram) Syscall(number uint64, args ...uint64) (result uint64, err error) {
	if len(args) > 6 {
		return 0, fmt.Errorf("too many arguments for a syscall")
	}
	// save the original registers and the current instructions
	restore, err := p.protectSyscall()
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, restore()) }()

	regs := *p.backupRegs
	// set the registers according to the syscall convention. Learn more about
	// it in `man 2 syscall`. In x86_64 the syscall nr is stored in rax
	// register, and the arguments are stored in rdi, rsi, rdx, r10, r8, r9 in
	// order
	regs.Rax = number
	for index, arg := range args {
		// All these registers are hard coded for x86 platform
		if index == 0 {
			regs.Rdi = arg
		} else if index == 1 {
			regs.Rsi = arg
		} else if index == 2 {
			regs.Rdx = arg
		} else if index == 3 {
			regs.R10 = arg
		} else if index == 4 {
			regs.R8 = arg
		} else if index == 5 {
			regs.R9 = arg
		}
	}
	err = setRegs(p.pid, &regs)
	if err != nil {
		return 0, err
	}

	instruction := make([]byte, unixInstrSize)
	ip := getIp(p.backupRegs)

	// set the current instruction (the ip register points to) to the `syscall`
	// instruction. In x86_64, the `syscall` instruction is 0x050f.
	binary.LittleEndian.PutUint16(instruction, 0x050f)
	_, err = unix.PtracePokeData(p.pid, ip, instruction)
	if err != nil {
		return 0, fmt.Errorf("writing data %v to %x: %w", instruction, ip, err)
	}

	// run one instruction, and stop
	err = p.Step()
	if err != nil {
		return 0, err
	}

	// read registers, the return value of syscall is stored inside rax register
	err = getRegs(p.pid, &regs)
	if err != nil {
		return 0, err
	}

	if getIp(&regs) != ip+unixInstrSize {
		return 0, fmt.Errorf("syscall instruction did not complete: ip=%#x, expected=%#x", getIp(&regs), ip+unixInstrSize)
	}
	return regs.Rax, nil
}

// JumpToFakeFunc writes jmp instruction to jump to fake function
func (p *TracedProgram) JumpToFakeFunc(originAddr uint64, targetAddr uint64) error {
	instructions := make([]byte, 16)

	// mov rax, targetAddr;
	// jmp rax ;
	instructions[0] = 0x48
	instructions[1] = 0xb8
	endian.PutUint64(instructions[2:10], targetAddr)
	instructions[10] = 0xff
	instructions[11] = 0xe0

	return p.PtraceWriteSlice(originAddr, instructions)
}
