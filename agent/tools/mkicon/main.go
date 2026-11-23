// mkicon writes the application icon (winres/icon.png, 256 px) from the
// GNOME extension's Clawd drawing. Run it in agent/: go run ./tools/mkicon
package main

import (
	"flag"
	"log"
	"os"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/icon"
)

func run(svgPath, outPath string) error {
	svg, err := os.ReadFile(svgPath)
	if err != nil {
		return err
	}
	img, err := icon.Clawd(svg, 256)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, icon.PNG(img), 0o644)
}

func main() {
	svg := flag.String("svg", "../claude-usage@localhost/clawd.svg", "Clawd SVG (read only)")
	out := flag.String("out", "winres/icon.png", "PNG to write")
	flag.Parse()
	if err := run(*svg, *out); err != nil {
		log.Fatal(err)
	}
}
