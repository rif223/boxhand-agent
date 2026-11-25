// Package envs holds the environment that the agent serves: exactly one tsbox daemon, "local". The API keeps
// Boxhand's notion of environments (?env=<id>), so a Boxhand can talk to the agent like to itself.
package envs

import (
	"runtime"

	"github.com/rif223/boxhand-agent/internal/cli"
	"github.com/rif223/boxhand-agent/internal/daemon"
)

// Kinds of environments.
const (
	KindAuto = "auto" // like the tsbox CLI: TSBOX_HOST, system socket (Linux), user daemon
	KindHost = "host" // a fixed address (unix://… or npipe://…)
)

// Config describes an environment.
type Config struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Host string `json:"host,omitempty"`
}

// Env is an environment with its daemon connection.
type Env struct {
	Config
	Daemon *daemon.Manager
	CLI    cli.Runner
}

// Drivers returns the drivers tsbox supports on this platform.
func Drivers() []string {
	if runtime.GOOS == "linux" {
		return []string{"gvisor", "firecracker", "dev"}
	}
	return []string{"dev"}
}

// Store is the list of environments (here: one).
type Store struct {
	list []*Env
}

// New returns the store with the environment "local" for the daemon at host (empty = like the tsbox CLI).
func New(bin, host string) *Store {
	e := &Env{Config: Config{ID: "local", Name: "Local", Kind: KindAuto}, CLI: cli.Runner{Bin: bin}}
	if host != "" {
		e.Kind, e.Host = KindHost, host
		e.CLI.Env = []string{"TSBOX_HOST=" + host}
	}
	e.Daemon = daemon.New(e.Host)
	return &Store{list: []*Env{e}}
}

// List returns all environments.
func (s *Store) List() []*Env {
	return append([]*Env(nil), s.list...)
}

// Get returns an environment by ID; an empty ID means the first one.
func (s *Store) Get(id string) (*Env, bool) {
	if id == "" && len(s.list) > 0 {
		return s.list[0], true
	}
	for _, e := range s.list {
		if e.ID == id {
			return e, true
		}
	}
	return nil, false
}
