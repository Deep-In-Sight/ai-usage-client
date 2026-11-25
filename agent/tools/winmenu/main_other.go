//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "winmenu: Windows only")
	os.Exit(2)
}
