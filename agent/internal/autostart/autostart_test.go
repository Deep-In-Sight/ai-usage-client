package autostart

import "testing"

func TestRunValueQuotesPath(t *testing.T) {
	exe := "C:\\Users\\\ud64d \uae38\ub3d9\\AppData\\Local\\Programs\\AIUsage\\ai-usage.exe"
	eq(t, RunValue(exe), `"`+exe+`" run`)
}
