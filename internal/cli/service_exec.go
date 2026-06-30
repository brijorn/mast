package cli

import "os/exec"

var runServiceCommand = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}
