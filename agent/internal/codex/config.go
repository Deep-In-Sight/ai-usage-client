package codex

import (
	"path/filepath"
	"strings"
)

// Config is what the agent reads of Codex's config.toml: two keys.
type Config struct {
	ChatGPTBaseURL   string // chatgpt_base_url ("" when not set)
	CredentialsStore string // cli_auth_credentials_store: file | keyring | auto ("" when not set)
}

const maxConfigFile = 256 << 10

// ReadConfig reads <home>/config.toml (its first 256 KiB). A missing or
// unreadable file is an empty Config.
func ReadConfig(home string) Config {
	b, err := readCapped(filepath.Join(home, "config.toml"), maxConfigFile)
	if err != nil {
		return Config{}
	}
	return parseConfig(string(b))
}

// parseConfig reads the two keys line by line, without a TOML parser.
//
// Ported from steipete/CodexBar (MIT) Sources/CodexBarCore/Providers/Codex/CodexOAuth/CodexOAuthUsageFetcher.swift
// parseChatGPTBaseURL @ a26ca57: "#" starts a comment (so a commented
// setting cannot shadow a real one after it), the line is key = value, a
// value's surrounding double or single quotes are dropped, the first
// setting counts. Our addition: only top-level keys count (reading stops
// at the first [table]), as Codex reads these two keys there.
func parseConfig(contents string) Config {
	var c Config
	seen := map[string]bool{}
	for _, raw := range strings.Split(contents, "\n") {
		line, _, _ := strings.Cut(raw, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			break // a table: its keys are not top-level settings
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		value = strings.TrimSpace(value)
		if seen[key] {
			continue
		}
		switch key {
		case "chatgpt_base_url":
			c.ChatGPTBaseURL, seen[key] = value, true
		case "cli_auth_credentials_store":
			c.CredentialsStore, seen[key] = value, true
		}
	}
	return c
}
