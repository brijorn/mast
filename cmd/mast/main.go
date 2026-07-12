package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/alecthomas/kong"
	mastcli "github.com/brijorn/mast/internal/cli"
)

var cli struct {
	Start   mastcli.StartCmd   `cmd:""`
	Service mastcli.ServiceCmd `cmd:""`
	Config  mastcli.ConfigCmd  `cmd:""`
	Device  mastcli.DeviceCmd  `cmd:""`
	Version mastcli.VersionCmd `cmd:""`
	Peer    mastcli.PeerCmd    `cmd:""`
	Update  mastcli.UpdateCmd  `cmd:""`
}

func main() {

	parser, err := kong.New(&cli)
	if err != nil {
		log.Fatal(err)
	}
	ctx, err := parser.Parse(commandArgs())
	parser.FatalIfErrorf(err)
	err = ctx.Run()
	if err != nil {
		log.Fatal(err)
	}

}

func commandArgs() []string {
	args := os.Args[1:]
	if runtime.GOOS != "android" || len(args) == 0 {
		return args
	}

	paths := []string{os.Args[0]}
	if abs, err := filepath.Abs(os.Args[0]); err == nil {
		paths = append(paths, abs)
	}
	if resolved, err := exec.LookPath(os.Args[0]); err == nil {
		paths = append(paths, resolved)
		if abs, err := filepath.Abs(resolved); err == nil {
			paths = append(paths, abs)
		}
	}
	if executable, err := os.Executable(); err == nil {
		paths = append(paths, executable)
	}
	return trimAndroidExecutableArg(args, paths)
}

func samePath(a string, paths []string) bool {
	if a == "" {
		return false
	}
	clean := filepath.Clean(a)
	for _, path := range paths {
		if path == "" {
			continue
		}
		if clean == filepath.Clean(path) {
			return true
		}
	}
	return false
}

func trimAndroidExecutableArg(args []string, executablePaths []string) []string {
	for len(args) > 0 && samePath(args[0], executablePaths) {
		args = args[1:]
	}
	for len(args) > 0 && samePath(args[len(args)-1], executablePaths) {
		args = args[:len(args)-1]
	}
	return args
}
