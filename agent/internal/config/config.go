// Package config reads and writes the agent's files in its data folder:
// config.json (written by the installer), state.json and device-id.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/atomicfile"
)

// Config is config.json. Unknown keys are ignored.
type Config struct {
	ServerURLs      []string `json:"server_urls"`
	IngestKey       string   `json:"ingest_key"`
	ClaudeConfigDir string   `json:"claude_config_dir,omitempty"`
	KeychainService string   `json:"keychain_service,omitempty"`
}

const (
	maxURLs   = 10
	maxKeyLen = 256
)

// Parse reads config.json content (a leading UTF-8 BOM is allowed),
// normalizes the server URLs and validates the result. Error texts never
// contain the ingest key.
func Parse(b []byte) (Config, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(b))
	var c Config
	if err := dec.Decode(&c); err != nil {
		return Config{}, errors.New("config: not a valid JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, errors.New("config: unexpected data after the JSON object")
	}
	for i, u := range c.ServerURLs {
		n, err := NormalizeURL(u)
		if err != nil {
			return Config{}, fmt.Errorf("config: server_urls[%d]: %w", i, err)
		}
		c.ServerURLs[i] = n
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks the rules of the shared installer interface.
func (c Config) Validate() error {
	if len(c.ServerURLs) < 1 || len(c.ServerURLs) > maxURLs {
		return fmt.Errorf("config: server_urls must have 1 to %d entries", maxURLs)
	}
	for i, u := range c.ServerURLs {
		n, err := NormalizeURL(u)
		if err != nil {
			return fmt.Errorf("config: server_urls[%d]: %w", i, err)
		}
		if n != u {
			return fmt.Errorf("config: server_urls[%d] is not normalized", i)
		}
	}
	if c.IngestKey == "" {
		return errors.New("config: ingest_key is empty")
	}
	if len(c.IngestKey) > maxKeyLen {
		return fmt.Errorf("config: ingest_key is longer than %d characters", maxKeyLen)
	}
	for _, r := range c.IngestKey {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == unicode.ReplacementChar {
			return errors.New("config: ingest_key contains whitespace or control characters")
		}
	}
	if c.ClaudeConfigDir != "" && !filepath.IsAbs(c.ClaudeConfigDir) {
		return errors.New("config: claude_config_dir must be an absolute path")
	}
	return nil
}

// NormalizeURL accepts http(s)://host[:port] with an optional trailing "/"
// and returns scheme://host[:port].
func NormalizeURL(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", errors.New("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("host is missing")
	}
	if u.User != nil {
		return "", errors.New("user info is not allowed")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(s, "?#") {
		return "", errors.New("query and fragment are not allowed")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("a path is not allowed")
	}
	return u.Scheme + "://" + u.Host, nil
}

// Load reads and validates a config file.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return Parse(b)
}

// Save writes the config privately (file 0600, folder 0700).
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// State is what the agent learns while running (state.json).
type State struct {
	LastGoodURL string     `json:"last_good_url,omitempty"`
	ServerURLs  []string   `json:"server_urls,omitempty"`
	BatchLimit  int        `json:"batch_limit,omitempty"`
	FetchLimit  FetchLimit `json:"fetch_limit,omitzero"`
}

// FetchLimit is the agent's own record of recent usage fetches, kept so that
// a restart does not reset the local limit.
type FetchLimit struct {
	Attempts       []time.Time `json:"attempts,omitempty"`
	NotBefore      time.Time   `json:"not_before,omitzero"`
	Consecutive429 int         `json:"consecutive_429,omitempty"`
}

// IsZero lets omitzero drop an empty record.
func (f FetchLimit) IsZero() bool {
	return len(f.Attempts) == 0 && f.NotBefore.IsZero() && f.Consecutive429 == 0
}

// LoadState returns the zero State when the file is missing or broken.
func LoadState(path string) State {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var s State
	if json.Unmarshal(b, &s) != nil {
		return State{}
	}
	return s
}

// SaveState writes state.json.
func SaveState(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// DeviceID returns the id stored at path, creating a random UUID v4 there
// when the file is missing or empty.
func DeviceID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		return "", err
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
	if err := atomicfile.Write(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}
