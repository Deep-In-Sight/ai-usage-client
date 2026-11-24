// Package backlog keeps fetch results that the server has not accepted yet,
// in one JSON file, so they survive restarts (7 days and 1000 items per
// provider at most).
package backlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/atomicfile"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

const (
	MaxAge   = 7 * 24 * time.Hour
	MaxItems = 1000 // per provider
)

// DefaultProvider is the provider of items a v0.7/v0.8 agent stored
// (without one).
const DefaultProvider = "claude"

// Item is one fetch result waiting for the server. On disk the account
// key keeps the v0.8 name account_uuid, so a v0.8 file reads as it is.
type Item struct {
	Provider          string          `json:"provider"`
	AccountKey        string          `json:"account_uuid"`
	FetchedAt         protocol.Time   `json:"fetched_at"`
	HTTPStatus        int             `json:"http_status"`
	Usage             json.RawMessage `json:"usage"` // nil is stored as null
	RetryAfterSeconds *int            `json:"retry_after_seconds,omitempty"`
	Source            string          `json:"source,omitempty"`
	CLIVersion        string          `json:"cli_version,omitempty"`
	Detail            string          `json:"detail,omitempty"`
}

// V1 is the item as a schema 1 result (Claude only has no more fields).
func (it Item) V1() protocol.Result {
	return protocol.Result{AccountUUID: it.AccountKey, FetchedAt: it.FetchedAt, HTTPStatus: it.HTTPStatus, Usage: it.Usage}
}

// Store is the backlog. It is safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	path  string
	items []Item // ordered by fetched_at, oldest first
}

type key struct {
	provider, account string
	at                int64
}

func keyOf(r Item) key { return key{r.Provider, r.AccountKey, r.FetchedAt.UnixMilli()} }

// Open loads the backlog at path. A missing file is an empty backlog; a
// broken one is moved to <path>.bad and the backlog starts empty. Items
// without a provider (a v0.7/v0.8 file) are Claude's.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.items); err != nil {
		s.items = nil
		if rerr := os.Rename(path, path+".bad"); rerr != nil {
			return nil, rerr
		}
		return s, nil
	}
	for i := range s.items {
		if s.items[i].Provider == "" {
			s.items[i].Provider = DefaultProvider
		}
		if string(s.items[i].Usage) == "null" {
			s.items[i].Usage = nil
		}
	}
	s.sortLocked()
	return s, nil
}

func (s *Store) sortLocked() {
	sort.SliceStable(s.items, func(i, j int) bool { return s.items[i].FetchedAt.Before(s.items[j].FetchedAt.Time) })
}

func (s *Store) saveLocked() error {
	items := s.items
	if items == nil {
		items = []Item{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, b, 0o600)
}

// Add stores r unless a result with the same provider, account and
// fetched_at exists.
func (s *Store) Add(r Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Provider == "" {
		r.Provider = DefaultProvider
	}
	k := keyOf(r)
	for _, it := range s.items {
		if keyOf(it) == k {
			return nil
		}
	}
	r.Usage = append(json.RawMessage(nil), r.Usage...)
	if len(r.Usage) == 0 {
		r.Usage = nil
	}
	i := sort.Search(len(s.items), func(i int) bool { return s.items[i].FetchedAt.After(r.FetchedAt.Time) })
	s.items = append(s.items, Item{})
	copy(s.items[i+1:], s.items[i:])
	s.items[i] = r
	return s.saveLocked()
}

// Pending returns up to limit results, oldest first, as copies: those of
// the given providers, or all when none is given.
func (s *Store) Pending(limit int, providers ...string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.items {
		if len(out) >= limit {
			break
		}
		if len(providers) > 0 && !slices.Contains(providers, it.Provider) {
			continue
		}
		if it.Usage != nil {
			it.Usage = append(json.RawMessage(nil), it.Usage...)
		}
		out = append(out, it)
	}
	return out
}

// Remove deletes the given results (those the server accepted).
func (s *Store) Remove(sent []Item) error {
	if len(sent) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	drop := make(map[key]bool, len(sent))
	for _, r := range sent {
		drop[keyOf(r)] = true
	}
	kept := s.items[:0]
	for _, it := range s.items {
		if !drop[keyOf(it)] {
			kept = append(kept, it)
		}
	}
	if len(kept) == len(s.items) {
		return nil
	}
	s.items = kept
	return s.saveLocked()
}

// Prune drops results older than MaxAge, then each provider's oldest
// beyond MaxItems.
func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := map[string]int{}
	for _, it := range s.items {
		if now.Sub(it.FetchedAt.Time) <= MaxAge {
			count[it.Provider]++
		}
	}
	kept := make([]Item, 0, len(s.items))
	for _, it := range s.items { // oldest first: drop while a provider has too many
		if now.Sub(it.FetchedAt.Time) > MaxAge {
			continue
		}
		if count[it.Provider] > MaxItems {
			count[it.Provider]--
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(s.items) {
		return nil
	}
	s.items = kept
	return s.saveLocked()
}

// Len is the number of stored results.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}
