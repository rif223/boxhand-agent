// Package activity records what was done through Boxhand (an audit log, kept as JSON lines).
package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one recorded action.
type Entry struct {
	At       time.Time `json:"at"`
	Source   string    `json:"source"` // "ui" or "schedule"
	Action   string    `json:"action"` // e.g. "box.stop", "project.up"
	Env      string    `json:"env,omitempty"`
	Target   string    `json:"target,omitempty"`
	OK       bool      `json:"ok"`
	Detail   string    `json:"detail,omitempty"`
	Remote   string    `json:"remote,omitempty"`
	User     string    `json:"user,omitempty"` // account that did it (empty without accounts)
	Duration string    `json:"duration,omitempty"`
}

// Log appends entries to a file and keeps the newest ones in memory.
type Log struct {
	path string
	mu   sync.Mutex
	ring []Entry
}

const keep = 2000

// Open loads the newest entries of the log file.
func Open(dir string) (*Log, error) {
	l := &Log{path: filepath.Join(dir, "activity.jsonl")}
	f, err := os.Open(l.path)
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				l.ring = append(l.ring, e)
				if len(l.ring) > keep {
					l.ring = l.ring[len(l.ring)-keep:]
				}
			}
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Rewrite a log that grew too large, keeping only the ring.
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > 8<<20 {
		l.rewrite()
	}
	return l, nil
}

func (l *Log) rewrite() {
	tmp := l.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	for _, e := range l.ring {
		_ = enc.Encode(e)
	}
	f.Close()
	_ = os.Rename(tmp, l.path)
}

// Add records an entry.
func (l *Log) Add(e Entry) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if len(e.Detail) > 500 {
		e.Detail = e.Detail[:500] + "…"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ring = append(l.ring, e)
	if len(l.ring) > keep {
		l.ring = l.ring[len(l.ring)-keep:]
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(e)
}

// List returns the newest entries first (at most limit).
func (l *Log) List(limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.ring) {
		limit = len(l.ring)
	}
	out := make([]Entry, 0, limit)
	for i := len(l.ring) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.ring[i])
	}
	return out
}
