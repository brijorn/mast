package cli

import (
	"fmt"

	"github.com/brijorn/mast/internal/version"
)

type VersionCmd struct {
	Verbose bool `short:"v" long:"verbose" help:"Show verbose debug information"`
}

func (c *VersionCmd) Run() error {
	if c.Verbose {
		fmt.Printf("Version: %s\nCommit: %s\nDate :%s\n", version.Version, version.Commit, version.Date)
		return nil
	}

	fmt.Println(version.Version)
	return nil
}
