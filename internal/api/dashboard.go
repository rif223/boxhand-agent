package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/rif223/boxhand-agent/internal/envs"
	"github.com/rif223/tsbox"
	"github.com/rif223/tsbox/client"
)

// dashCounts counts boxes by health.
type dashCounts struct {
	Running   int `json:"running"`  // running, even if a script has a problem
	Stopped   int `json:"stopped"`  // created, exited, failed or quarantined
	Failed    int `json:"failed"`   // failed or quarantined (also in Stopped)
	Degraded  int `json:"degraded"` // not failed, but a script is broken or in backoff
	Starting  int `json:"starting"` // starting or stopping
	Total     int `json:"total"`
	Scripts   int `json:"scripts"`
	Restarts  int `json:"restarts"`
	Projects  int `json:"projects"`
	SDKUsages int `json:"sdks"`
}

type dashTop struct {
	Box    string  `json:"box"`
	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`
}

type dashEnv struct {
	envView
	Counts      dashCounts         `json:"counts"`
	Problems    []string           `json:"problems"` // names of failed/degraded boxes
	CPU         float64            `json:"cpu"`
	CPULimit    float64            `json:"cpuLimit"`
	Memory      int64              `json:"memory"`
	MemoryLimit int64              `json:"memoryLimit"`
	EventsToday int                `json:"eventsToday"`
	Events24h   int                `json:"events24h"`
	Recent      []tsbox.Event      `json:"recent"`
	Top         []dashTop          `json:"top"`
	Boxes       []client.Box       `json:"boxes"`
	Usage       map[string]dashTop `json:"usage"` // per box
}

// dashboard returns an overview of all environments (probed in parallel).
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	list := s.Envs.List()
	projectsPerEnv := map[string]int{}
	for _, p := range s.Projects.List() {
		id := p.Env
		if _, ok := s.Envs.Get(id); !ok || id == "" {
			if e, ok := s.Envs.Get(""); ok {
				id = e.ID
			}
		}
		projectsPerEnv[id]++
	}

	out := make([]dashEnv, len(list))
	var wg sync.WaitGroup
	for i, e := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = summarize(r.Context(), e)
			out[i].Counts.Projects = projectsPerEnv[e.ID]
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

func emptyDash(v envView) dashEnv {
	return fillDash(dashEnv{envView: v})
}

// fillDash replaces missing lists with empty ones, so the UI never gets null.
func fillDash(d dashEnv) dashEnv {
	if d.Recent == nil {
		d.Recent = []tsbox.Event{}
	}
	if d.Top == nil {
		d.Top = []dashTop{}
	}
	if d.Problems == nil {
		d.Problems = []string{}
	}
	if d.Boxes == nil {
		d.Boxes = []client.Box{}
	}
	if d.Usage == nil {
		d.Usage = map[string]dashTop{}
	}
	return d
}

func summarize(ctx context.Context, e *envs.Env) dashEnv {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	d := emptyDash(envStatus(ctx, e))
	if d.Daemon == nil {
		return d
	}
	c, err := e.Daemon.Client(ctx)
	if err != nil {
		return d
	}

	if boxes, err := c.List(ctx, true); err == nil {
		d.Boxes = boxes
		for _, b := range boxes {
			d.Counts.Total++
			d.Counts.Scripts += len(b.Scripts)
			d.Counts.SDKUsages += len(b.SDKs)
			degraded := false
			for _, sc := range b.Scripts {
				d.Counts.Restarts += sc.Restarts
				if sc.State == tsbox.ScriptBroken || sc.State == tsbox.ScriptBackoff {
					degraded = true
				}
			}
			if b.State == tsbox.BoxRunning {
				d.CPULimit += b.Limits.CPU
				d.MemoryLimit += b.Limits.Memory
			}
			// Running + Starting + Stopped = Total. Problems are counted on top, because a box
			// can run and still have a broken script (same rule as boxHasProblem in web/src/lib/format.ts).
			switch b.State {
			case tsbox.BoxRunning:
				d.Counts.Running++
			case tsbox.BoxStarting, tsbox.BoxStopping:
				d.Counts.Starting++
			default:
				d.Counts.Stopped++
			}
			switch {
			case b.State == tsbox.BoxFailed || b.State == tsbox.BoxQuarantined:
				d.Counts.Failed++
				d.Problems = append(d.Problems, b.Name)
			case degraded:
				d.Counts.Degraded++
				d.Problems = append(d.Problems, b.Name)
			}
		}
	}

	for entries, err := range c.Stats(ctx, "", false) {
		if err != nil {
			break
		}
		for _, st := range entries {
			cpu := max(0, st.Stats.CPUCores)
			mem := max(0, st.Stats.Memory)
			d.CPU += cpu
			d.Memory += mem
			d.Top = append(d.Top, dashTop{Box: st.Box, CPU: cpu, Memory: mem})
			d.Usage[st.Box] = dashTop{Box: st.Box, CPU: cpu, Memory: mem}
		}
		break
	}
	sort.Slice(d.Top, func(i, j int) bool { return d.Top[i].CPU > d.Top[j].CPU })
	if len(d.Top) > 5 {
		d.Top = d.Top[:5]
	}

	// The event stream first returns the buffered events, then waits for new ones: read briefly.
	ectx, ecancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer ecancel()
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var all []tsbox.Event
	for ev, err := range c.Events(ectx, now.Add(-24*time.Hour), "") {
		if err != nil {
			break
		}
		all = append(all, ev)
		d.Events24h++
		if !ev.At.Before(midnight) {
			d.EventsToday++
		}
	}
	for i := len(all) - 1; i >= 0 && len(d.Recent) < 8; i-- {
		d.Recent = append(d.Recent, all[i])
	}
	return d
}
