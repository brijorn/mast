//go:build !windows

package node

import "os/exec"

func configureADBCommandCancellation(_ *exec.Cmd) {}
