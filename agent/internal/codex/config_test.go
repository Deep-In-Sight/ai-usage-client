package codex

import (
	"path/filepath"
	"strings"
	"testing"
)

// config.toml is read for two keys only, line by line (design B2, ported
// from CodexBar): "#" starts a comment, quotes are dropped, a commented
// setting cannot shadow a real one after it.
func TestReadConfigComments(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), strings.Join([]string{
		`# chatgpt_base_url = "x"`,
		`   # chatgpt_base_url = "https://indented.example/"`,
		`model = "gpt-5" # not ours`,
		`chatgpt_base_url = "https://chatgpt.com/backend-api/" # c`,
		`chatgpt_base_url = "https://second.example/"`,
		`cli_auth_credentials_store = 'keyring'`,
		``,
	}, "\n"))
	eq(t, ReadConfig(home), Config{ChatGPTBaseURL: "https://chatgpt.com/backend-api/", CredentialsStore: "keyring"})
}

// Only top-level keys count: a table's keys are not Codex's own settings.
func TestReadConfigTopLevelOnly(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), "model = \"x\"\r\n[profiles.work]\r\nchatgpt_base_url = \"https://evil.example/\"\r\ncli_auth_credentials_store = \"keyring\"\r\n")
	eq(t, ReadConfig(home), Config{})
	writeFile(t, filepath.Join(home, "config.toml"), "chatgpt_base_url=https://chat.openai.com\n[tui]\n")
	eq(t, ReadConfig(home), Config{ChatGPTBaseURL: "https://chat.openai.com"})
}

func TestReadConfigMissingOrHuge(t *testing.T) {
	eq(t, ReadConfig(t.TempDir()), Config{})
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), strings.Repeat("# filler\n", 40000)+"chatgpt_base_url = \"https://late.example/\"\n")
	eq(t, ReadConfig(home), Config{}) // beyond the first 256 KiB
}
