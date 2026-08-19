//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	stdInputHandle  = ^uintptr(9)  // (DWORD)-10
	stdOutputHandle = ^uintptr(10) // (DWORD)-11
	keyEvent        = 0x0001
	virtualKeyEnter = 0x0d
)

type coord struct {
	x int16
	y int16
}

type smallRect struct {
	left   int16
	top    int16
	right  int16
	bottom int16
}

type consoleScreenBufferInfo struct {
	size              coord
	cursorPosition    coord
	attributes        uint16
	window            smallRect
	maximumWindowSize coord
}

type keyEventRecord struct {
	keyDown        int32
	repeatCount    uint16
	virtualKeyCode uint16
	virtualScan    uint16
	char           uint16
	controlState   uint32
}

type inputRecord struct {
	eventType uint16
	padding   uint16
	key       keyEventRecord
}

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	getStdHandle                   = kernel32.NewProc("GetStdHandle")
	getConsoleScreenBufferInfo     = kernel32.NewProc("GetConsoleScreenBufferInfo")
	readConsoleOutputCharacterWide = kernel32.NewProc("ReadConsoleOutputCharacterW")
	writeConsoleInputWide          = kernel32.NewProc("WriteConsoleInputW")
)

func consoleHandle(which uintptr) syscall.Handle {
	handle, _, _ := getStdHandle.Call(which)
	return syscall.Handle(handle)
}

func screenContains(output syscall.Handle, prompt string) bool {
	var info consoleScreenBufferInfo
	ok, _, _ := getConsoleScreenBufferInfo.Call(uintptr(output), uintptr(unsafe.Pointer(&info)))
	if ok == 0 || info.size.x <= 0 || info.size.y <= 0 {
		return false
	}
	cells := int(info.size.x) * int(info.size.y)
	buffer := make([]uint16, cells)
	var read uint32
	origin := uintptr(uint16(0)) | uintptr(uint32(uint16(0)))<<16
	ok, _, _ = readConsoleOutputCharacterWide.Call(
		uintptr(output),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(cells),
		origin,
		uintptr(unsafe.Pointer(&read)),
	)
	return ok != 0 && strings.Contains(syscall.UTF16ToString(buffer[:read]), prompt)
}

func inputKey(char, virtualKey uint16, down bool) inputRecord {
	keyDown := int32(0)
	if down {
		keyDown = 1
	}
	return inputRecord{
		eventType: keyEvent,
		key: keyEventRecord{
			keyDown:        keyDown,
			repeatCount:    1,
			virtualKeyCode: virtualKey,
			char:           char,
		},
	}
}

func inject(input syscall.Handle, value string) error {
	characters, err := syscall.UTF16FromString(value)
	if err != nil {
		return err
	}
	characters = characters[:len(characters)-1]
	records := make([]inputRecord, 0, len(characters)*2+2)
	for _, char := range characters {
		records = append(records, inputKey(char, char, true), inputKey(char, char, false))
	}
	records = append(records,
		inputKey('\r', virtualKeyEnter, true),
		inputKey('\r', virtualKeyEnter, false),
	)
	var written uint32
	ok, _, callErr := writeConsoleInputWide.Call(
		uintptr(input),
		uintptr(unsafe.Pointer(&records[0])),
		uintptr(len(records)),
		uintptr(unsafe.Pointer(&written)),
	)
	if ok == 0 {
		return fmt.Errorf("WriteConsoleInputW: %w", callErr)
	}
	if written != uint32(len(records)) {
		return fmt.Errorf("WriteConsoleInputW wrote %d of %d records", written, len(records))
	}
	return nil
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: winprompt <value> <prompt> <program> [args...]")
		os.Exit(2)
	}

	program := os.Args[3]
	if !strings.ContainsAny(program, `\/`) {
		program = `.\` + program
	}
	command := exec.Command(program, os.Args[4:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "winprompt: start program: %v\n", err)
		os.Exit(1)
	}
	input := consoleHandle(stdInputHandle)
	output := consoleHandle(stdOutputHandle)
	deadline := time.Now().Add(2 * time.Minute)
	injected := false
	for time.Now().Before(deadline) {
		if screenContains(output, os.Args[2]) {
			if err := inject(input, os.Args[1]); err != nil {
				fmt.Fprintf(os.Stderr, "winprompt: inject input: %v\n", err)
				_ = command.Process.Kill()
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "winprompt: supplied configured input after prompt")
			injected = true
			break
		}
		if command.ProcessState != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !injected {
		fmt.Fprintf(os.Stderr, "winprompt: prompt %q did not appear\n", os.Args[2])
		_ = command.Process.Kill()
		os.Exit(1)
	}
	if err := command.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "winprompt: wait: %v\n", err)
		os.Exit(1)
	}
}
