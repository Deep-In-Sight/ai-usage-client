// Package backlog keeps fetch results that the server has not accepted yet,
// in one JSON file, so they survive restarts (7 days, 1000 items at most).
package backlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/atomicfile"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/protocol"
)

const (
	MaxAge   = 7 * 24 * time.Hour
	MaxItems = 1000
)

// Store is the backlog. It is safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	path  string
	items []protocol.Result // ordered by fetched_at, oldest first
}

type key struct {
	account string
	at      int64
}

func keyOf(r protocol.Result) key { return key{r.AccountUUID, r.FetchedAt.UnixMilli()} }

// Open loads the backlog at path. A missing file is an empty backlog; a
// broken one is moved to <path>.bad and the backlog starts empty.
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
	s.sortLocked()
	return s, nil
}

func (s *Store) sortLocked() {
	sort.SliceStable(s.items, func(i, j int) bool { return s.items[i].FetchedAt.Before(s.items[j].FetchedAt.Time) })
}

func (s *Store) saveLocked() error {
	items := s.items
	if items == nil {
		items = []protocol.Result{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, b, 0o600)
}

// Add stores r unless a result with the same account and fetched_at exists.
func (s *Store) Add(r protocol.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.items = append(s.items, protocol.Result{})
	copy(s.items[i+1:], s.items[i:])
	s.items[i] = r
	return s.saveLocked()
}

// Pending returns up to limit results, oldest first, as copies.
func (s *Store) Pending(limit int) []protocol.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > len(s.items) {
		limit = len(s.items)
	}
	if limit <= 0 {
		return nil
	}
	out := make([]protocol.Result, limit)
	copy(out, s.items[:limit])
	for i := range out {
		if out[i].Usage != nil {
			out[i].Usage = append(json.RawMessage(nil), out[i].Usage...)
		}
	}
	return out
}

// Remove deletes the given results (those the server accepted).
func (s *Store) Remove(sent []protocol.Result) error {
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

// Prune drops results older than MaxAge, then the oldest beyond MaxItems.
func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.items)
	start := 0
	for start < len(s.items) && now.Sub(s.items[start].FetchedAt.Time) > MaxAge {
		start++
	}
	if len(s.items)-start > MaxItems {
		start = len(s.items) - MaxItems
	}
	if start == 0 {
		return nil
	}
	s.items = append([]protocol.Result(nil), s.items[start:]...)
	if len(s.items) == n {
		return nil
	}
	return s.saveLocked()
}

// Len is the number of stored results.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}
