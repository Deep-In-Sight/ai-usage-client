// mkicon writes the program icon (winres/icon.ico: icon.Program drawn at
// every icon.ProgramIconSizes). Run it in agent/: go run ./tools/mkicon
package main

import (
	"flag"
	"log"
	"os"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
)

func run(outPath string) error {
	return os.WriteFile(outPath, icon.ProgramICO(), 0o644)
}

func main() {
	out := flag.String("out", "winres/icon.ico", "ICO to write")
	flag.Parse()
	if err := run(*out); err != nil {
		log.Fatal(err)
	}
}
