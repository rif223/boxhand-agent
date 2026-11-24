// Package daemon finds the tsbox daemon (tsboxd) and keeps a client to it.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rif223/tsbox/client"
)

// ErrNoDaemon means that no daemon answered on any known address.
var ErrNoDaemon = errors.New("no tsbox daemon reachable")

// Manager resolves the daemon address lazily and reconnects after failures.
type Manager struct {
	fixed string // --host; empty = client.Hosts()

	mu  sync.Mutex
	cur *client.Client
}

// New creates a manager. host may be empty (auto-detect like the tsbox CLI).
func New(host string) *Manager { return &Manager{fixed: host} }

// Candidates lists the addresses that are tried, in order.
func (m *Manager) Candidates() []string {
	if m.fixed != "" {
		return []string{m.fixed}
	}
	return client.Hosts()
}

// Client returns a working client, probing the candidates if there is none yet.
func (m *Manager) Client(ctx context.Context) (*client.Client, error) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur != nil {
		return cur, nil
	}
	var errs []error
	for _, h := range m.Candidates() {
		c, err := client.New(h)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err = c.Info(pctx)
		cancel()
		if err != nil {
			c.Close()
			errs = append(errs, fmt.Errorf("%s: %w", h, err))
			continue
		}
		m.mu.Lock()
		if m.cur == nil {
			m.cur = c
		} else {
			c.Close()
		}
		cur = m.cur
		m.mu.Unlock()
		return cur, nil
	}
	return nil, fmt.Errorf("%w (%w)", ErrNoDaemon, errors.Join(errs...))
}

// Reset drops the current client so the next call probes again (after connection errors).
func (m *Manager) Reset() {
	m.mu.Lock()
	if m.cur != nil {
		m.cur.Close()
		m.cur = nil
	}
	m.mu.Unlock()
}

// IsAPIError reports whether err came from the daemon (as opposed to a connection problem).
func IsAPIError(err error) bool {
	var ae *client.Error
	return errors.As(err, &ae)
}
