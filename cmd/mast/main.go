package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/alecthomas/kong"
	mastcli "github.com/brijorn/mast/internal/cli"
)

var cli struct {
	Start   mastcli.StartCmd   `cmd:""`
	Service mastcli.ServiceCmd `cmd:""`
	Config  mastcli.ConfigCmd  `cmd:""`
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

	executable, err := os.Executable()
	if err != nil {
		return args
	}
	return trimAndroidExecutableArg(args, executable)
}

func samePath(a string, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func trimAndroidExecutableArg(args []string, executable string) []string {
	for len(args) > 0 && samePath(args[0], executable) {
		args = args[1:]
	}
	for len(args) > 0 && samePath(args[len(args)-1], executable) {
		args = args[:len(args)-1]
	}
	return args
}
