package paths

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestLinuxDataDirIgnoresXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	d, err := For(func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return "/elsewhere"
		}
		return ""
	}, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if d.Data != "/h/.config/ai-usage" || d.InstallExe != "/h/.local/bin/ai-usage" || d.Home != "/h" {
		t.Fatalf("%+v", d)
	}
	if d.File(ConfigFile) != "/h/.config/ai-usage/config.json" {
		t.Fatal(d.File(ConfigFile))
	}
}

func TestWindowsDirs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	d, err := For(func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\L`
		}
		return ""
	}, `C:\Users\u`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Data != `C:\L\AIUsage` || d.InstallExe != `C:\L\Programs\AIUsage\ai-usage.exe` {
		t.Fatalf("%+v", d)
	}
	if _, err := For(func(string) string { return "" }, `C:\Users\u`); err == nil {
		t.Fatal("empty LOCALAPPDATA should fail")
	}
}

func TestMacDirs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only")
	}
	d, err := For(func(string) string { return "/elsewhere" }, "/Users/u")
	if err != nil {
		t.Fatal(err)
	}
	data := "/Users/u/Library/Application Support/AIUsage"
	if d.Data != data || d.InstallExe != filepath.Join(data, "ai-usage") {
		t.Fatalf("%+v", d)
	}
}

func TestCurrent(t *testing.T) {
	d, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(d.Data) || !filepath.IsAbs(d.InstallExe) {
		t.Fatalf("%+v", d)
	}
}
