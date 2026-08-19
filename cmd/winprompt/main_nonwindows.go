//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("winprompt is built for Windows and launched under Wine")
}
