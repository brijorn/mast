package main

import (
	"log"

	"github.com/alecthomas/kong"
	mastcli "github.com/brijorn/mast/internal/cli"
)

var cli struct {
	Start   mastcli.StartCmd   `cmd:""`
	Service mastcli.ServiceCmd `cmd:""`
	Config  mastcli.ConfigCmd  `cmd:""`
	Version mastcli.VersionCmd `cmd:""`
	Peer    mastcli.PeerCmd    `cmd:""`
}

func main() {

	ctx := kong.Parse(&cli)
	err := ctx.Run()
	if err != nil {
		log.Fatal(err)
	}

}
