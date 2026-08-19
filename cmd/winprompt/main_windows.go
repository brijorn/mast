//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const stdOutputHandle = ^uintptr(10) // (DWORD)-11

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

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	getStdHandle                   = kernel32.NewProc("GetStdHandle")
	getConsoleScreenBufferInfo     = kernel32.NewProc("GetConsoleScreenBufferInfo")
	readConsoleOutputCharacterWide = kernel32.NewProc("ReadConsoleOutputCharacterW")
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
	ok, _, _ = readConsoleOutputCharacterWide.Call(
		uintptr(output),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(cells),
		0,
		uintptr(unsafe.Pointer(&read)),
	)
	return ok != 0 && strings.Contains(syscall.UTF16ToString(buffer[:read]), prompt)
}

func main() {
	if len(os.Args) < 4 {
		os.Exit(2)
	}
	program := os.Args[3]
	if !strings.ContainsAny(program, `\/`) {
		program = `.\` + program
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		os.Exit(1)
	}
	defer reader.Close()
	defer writer.Close()
	command := exec.Command(program, os.Args[4:]...)
	command.Stdin = reader
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if command.Start() != nil {
		os.Exit(1)
	}
	output := consoleHandle(stdOutputHandle)
	deadline := time.Now().Add(2 * time.Minute)
	for !screenContains(output, os.Args[2]) {
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := io.WriteString(writer, os.Args[1]+"\r\n"); err != nil {
		_ = command.Process.Kill()
		os.Exit(1)
	}
	if err := command.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		os.Exit(1)
	}
}
