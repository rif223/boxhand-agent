// Package api is the HTTP API of boxhand-agent. It speaks the same API as Boxhand for one environment, so a
// Boxhand on another machine can forward requests for that environment here.
//
// Most endpoints work on one environment (one tsbox daemon), chosen with the query parameter ?env=<id>
// (default: the first environment).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rif223/boxhand-agent/internal/activity"
	"github.com/rif223/boxhand-agent/internal/daemon"
	"github.com/rif223/boxhand-agent/internal/envs"
	"github.com/rif223/boxhand-agent/internal/projects"
	"github.com/rif223/tsbox/client"
)

// Server bundles the dependencies of the handlers.
type Server struct {
	Envs     *envs.Store
	Projects *projects.Store
	Activity *activity.Log
	History  *projects.History

	// Facts about how the agent runs (GET /api/info).
	Listen  string
	TLS     bool
	DataDir string
	Version string

	// TokenKey is the SHA-256 of the only accepted token, from TokenKey().
	TokenKey string
}

// Handler returns the HTTP handler. Only the routes in routes.go exist.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	for _, r := range s.routes() {
		mux.HandleFunc(r.pattern, r.handler)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not_found", "not available on an agent")
	})
	return s.withToken(s.withActivity(mux))
}

// ---------- helpers ----------

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var e apiError
	e.Error.Code, e.Error.Message = code, msg
	writeJSON(w, status, e)
}

// daemonErr maps an error of the daemon client to an HTTP response.
func daemonErr(w http.ResponseWriter, e *envs.Env, err error) {
	var ae *client.Error
	switch {
	case errors.As(err, &ae):
		st := ae.Status
		if st == 0 {
			st = http.StatusBadRequest
		}
		writeErr(w, st, ae.Code, ae.Message)
	case errors.Is(err, daemon.ErrNoDaemon):
		writeErr(w, http.StatusServiceUnavailable, "no_daemon", err.Error())
	default:
		e.Daemon.Reset()
		writeErr(w, http.StatusBadGateway, "daemon", err.Error())
	}
}

// env returns the environment selected with ?env= (or a path value "id").
func (s *Server) env(w http.ResponseWriter, r *http.Request) (*envs.Env, bool) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("env")
	}
	e, ok := s.Envs.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "environment not found")
		return nil, false
	}
	return e, true
}

// client returns the daemon client of the selected environment.
func (s *Server) client(w http.ResponseWriter, r *http.Request) (*envs.Env, *client.Client, bool) {
	e, ok := s.env(w, r)
	if !ok {
		return nil, nil, false
	}
	c, err := e.Daemon.Client(r.Context())
	if err != nil {
		daemonErr(w, e, err)
		return nil, nil, false
	}
	return e, c, true
}

func parseSince(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	if d, err := time.ParseDuration(v); err == nil {
		return time.Now().Add(-d)
	}
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

// ---------- system & environments ----------

type infoResponse struct {
	Version string   `json:"version"`
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	CLI     string   `json:"cli"`
	CLIOK   bool     `json:"cliFound"`
	Drivers []string `json:"drivers"`
	Listen  string   `json:"listen"`
	TLS     bool     `json:"tls"`
	DataDir string   `json:"dataDir"`
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	resp := infoResponse{Version: s.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Drivers: envs.Drivers(),
		Listen: s.Listen, TLS: s.TLS, DataDir: s.DataDir}
	if e, ok := s.Envs.Get(""); ok {
		resp.CLI, resp.CLIOK = e.CLI.Available()
		if !resp.CLIOK {
			resp.CLI = e.CLI.Bin
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type envView struct {
	envs.Config
	Hosts      []string     `json:"hosts"`
	Daemon     *client.Info `json:"daemon"`
	DaemonHost string       `json:"daemonHost,omitempty"`
	Error      string       `json:"error,omitempty"`
}

func envStatus(ctx context.Context, e *envs.Env) envView {
	v := envView{Config: e.Config, Hosts: e.Daemon.Candidates()}
	c, err := e.Daemon.Client(ctx)
	if err == nil {
		var i client.Info
		if i, err = c.Info(ctx); err == nil {
			v.Daemon, v.DaemonHost = &i, c.Host()
		} else {
			e.Daemon.Reset()
		}
	}
	if err != nil {
		v.Error = err.Error()
	}
	return v
}

// listEnvs returns all environments with the status of their daemons (probed in parallel).
func (s *Server) listEnvs(w http.ResponseWriter, r *http.Request) {
	list := s.Envs.List()
	out := make([]envView, len(list))
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, e := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = envStatus(ctx, e)
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// startDaemon lets the tsbox CLI autostart the environment's daemon (any box command does that).
func (s *Server) startDaemon(w http.ResponseWriter, r *http.Request) {
	e, ok := s.env(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := e.CLI.Run(ctx, "", "ps")
	e.Daemon.Reset()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cli", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// stopDaemon ends the daemon (its running boxes are stopped).
func (s *Server) stopDaemon(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	if err := c.Shutdown(r.Context()); err != nil {
		daemonErr(w, e, err)
		return
	}
	e.Daemon.Reset()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	e, ok := s.env(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := e.CLI.Run(ctx, "", "doctor")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cli", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- boxes ----------

func (s *Server) listBoxes(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	boxes, err := c.List(r.Context(), r.URL.Query().Get("all") != "0")
	if err != nil {
		daemonErr(w, e, err)
		return
	}
	if boxes == nil {
		boxes = []client.Box{}
	}
	writeJSON(w, http.StatusOK, boxes)
}

func (s *Server) inspectBox(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	i, err := c.Inspect(r.Context(), r.PathValue("name"))
	if err != nil {
		daemonErr(w, e, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

func (s *Server) boxAction(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	grace, _ := time.ParseDuration(r.URL.Query().Get("grace"))
	var (
		b   client.Box
		err error
	)
	switch r.PathValue("action") {
	case "start":
		b, err = c.Start(r.Context(), name, nil)
	case "stop":
		b, err = c.Stop(r.Context(), name, grace)
	case "restart":
		b, err = c.Restart(r.Context(), name, grace)
	default:
		writeErr(w, http.StatusNotFound, "not_found", "unknown action")
		return
	}
	if err != nil {
		daemonErr(w, e, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) removeBox(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	if err := c.Remove(r.Context(), r.PathValue("name"), r.URL.Query().Get("force") == "1"); err != nil {
		daemonErr(w, e, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- streams (SSE) ----------

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	lq := client.LogQuery{
		Follow: q.Get("follow") != "0",
		Since:  parseSince(q.Get("since")),
		Script: q.Get("script"),
		Tail:   200,
	}
	if t, err := strconv.Atoi(q.Get("tail")); err == nil {
		lq.Tail = t
	}
	streamSSE(w, r, e, followLogs(r.Context(), c, r.PathValue("name"), lq))
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if q.Get("stream") == "0" {
		for entries, err := range c.Stats(r.Context(), q.Get("box"), false) {
			if err != nil {
				daemonErr(w, e, err)
				return
			}
			if entries == nil {
				entries = []client.StatsEntry{}
			}
			writeJSON(w, http.StatusOK, entries)
			return
		}
		writeJSON(w, http.StatusOK, []client.StatsEntry{})
		return
	}
	streamSSE(w, r, e, c.Stats(r.Context(), q.Get("box"), true))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	streamSSE(w, r, e, c.Events(r.Context(), parseSince(q.Get("since")), q.Get("box")))
}

// ---------- projects ----------

type projectView struct {
	projects.Project
	Name    string       `json:"name"`
	Dir     string       `json:"dir"`
	Exists  bool         `json:"exists"`
	Content string       `json:"content,omitempty"`
	Boxes   []client.Box `json:"boxes"`
}

func (s *Server) projectView(p projects.Project, boxes []client.Box, withContent bool) projectView {
	v := projectView{Project: p, Name: p.Name(), Dir: p.Dir(), Boxes: []client.Box{}}
	if v.Env == "" {
		if e, ok := s.Envs.Get(""); ok {
			v.Env = e.ID
		}
	}
	if b, err := p.Read(); err == nil {
		v.Exists = true
		if withContent {
			v.Content = string(b)
		}
	}
	if v.Name == "" {
		v.Name = filepath.Base(v.Dir)
	}
	for _, b := range boxes {
		if belongsTo(b, v.Dir, v.Name) {
			v.Boxes = append(v.Boxes, b)
		}
	}
	return v
}

func belongsTo(b client.Box, dir, name string) bool {
	if d, ok := b.Labels[client.LabelDir]; ok {
		return sameDir(d, dir)
	}
	return b.Labels[client.LabelProject] == name
}

func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// projectEnv returns the environment of a project (the first one if its environment was removed).
func (s *Server) projectEnv(p projects.Project) *envs.Env {
	if e, ok := s.Envs.Get(p.Env); ok {
		return e
	}
	e, _ := s.Envs.Get("")
	return e
}

// boxesOf returns all boxes of an environment, or nil when its daemon is unavailable.
func boxesOf(ctx context.Context, e *envs.Env) []client.Box {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := e.Daemon.Client(ctx)
	if err != nil {
		return nil
	}
	boxes, err := c.List(ctx, true)
	if err != nil {
		if !daemon.IsAPIError(err) {
			e.Daemon.Reset()
		}
		return nil
	}
	return boxes
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	cache := map[string][]client.Box{}
	out := []projectView{}
	for _, p := range s.Projects.List() {
		e := s.projectEnv(p)
		boxes, ok := cache[e.ID]
		if !ok {
			boxes = boxesOf(r.Context(), e)
			cache[e.ID] = boxes
		}
		p.Env = e.ID
		out = append(out, s.projectView(p, boxes, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	e := s.projectEnv(p)
	p.Env = e.ID
	writeJSON(w, http.StatusOK, s.projectView(p, boxesOf(r.Context(), e), true))
}

func (s *Server) addProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Env     string `json:"env"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	e, ok := s.Envs.Get(req.Env)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid", "environment not found")
		return
	}
	p, err := s.Projects.Add(req.Path, e.ID, []byte(req.Content))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s.projectView(p, boxesOf(r.Context(), e), true))
}

func (s *Server) saveProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	var req struct {
		Content *string `json:"content"`
		Env     *string `json:"env"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if req.Content != nil {
		if err := s.writeProject(r, p, []byte(*req.Content)); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
	}
	if req.Env != nil {
		if _, ok := s.Envs.Get(*req.Env); !ok || *req.Env == "" {
			writeErr(w, http.StatusBadRequest, "invalid", "environment not found")
			return
		}
		var err error
		if p, err = s.Projects.SetEnv(p.ID, *req.Env); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
	}
	e := s.projectEnv(p)
	p.Env = e.ID
	writeJSON(w, http.StatusOK, s.projectView(p, boxesOf(r.Context(), e), true))
}

func (s *Server) removeProject(w http.ResponseWriter, r *http.Request) {
	if err := s.Projects.Remove(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	s.History.Remove(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) projectAction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	var args []string
	switch r.PathValue("action") {
	case "up":
		args = []string{"up", "-d", "-f", p.File}
	case "down":
		args = []string{"down", "-f", p.File}
	default:
		writeErr(w, http.StatusNotFound, "not_found", "unknown action")
		return
	}
	if _, err := os.Stat(p.File); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", p.File+" does not exist")
		return
	}
	e := s.projectEnv(p)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := e.CLI.Run(ctx, p.Dir(), args...)
	e.Daemon.Reset() // the CLI may have autostarted a daemon
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cli", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
