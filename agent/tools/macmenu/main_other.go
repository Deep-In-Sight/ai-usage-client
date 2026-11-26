//go:build !darwin || !cgo

package main

import (
	"fmt"
	"os"
)

func main() {
	if !allowed(os.Getenv) {
		fmt.Fprintln(os.Stderr, "macmenu: refusing to run outside GitHub Actions (GITHUB_ACTIONS=true, RUNNER_TEMP set)")
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "macmenu: macOS only")
	os.Exit(2)
}
