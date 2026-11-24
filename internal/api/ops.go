package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rif223/boxhand-agent/internal/activity"
)

// ---------- activity ----------

// recorder captures the status (and, for errors, the start of the body) of a response.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 400 && r.body.Len() < 1024 {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// describe maps a mutating API request to an action name and a target.
func (s *Server) describe(r *http.Request) (action, target string) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	m := r.Method
	at := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	projectName := func(id string) string {
		if p, ok := s.Projects.Get(id); ok {
			if n := p.Name(); n != "" {
				return n
			}
			return filepath.Base(p.Dir())
		}
		return id
	}
	switch at(0) {
	case "boxes":
		if m == http.MethodDelete {
			return "box.remove", at(1)
		}
		return "box." + at(2), at(1)
	case "run":
		return "box.run", ""
	case "projects":
		switch {
		case m == http.MethodPost && len(parts) == 1:
			return "project.add", ""
		case m == http.MethodDelete:
			return "project.remove", projectName(at(1))
		case at(2) == "history":
			return "project.restore", projectName(at(1))
		case at(2) == "file":
			return "file.save", projectName(at(1)) + "/" + r.URL.Query().Get("path")
		case m == http.MethodPut:
			return "project.update", projectName(at(1))
		default:
			return "project." + at(2), projectName(at(1))
		}
	case "envs":
		return "env." + at(2), at(1)
	}
	return strings.ToLower(m) + " " + r.URL.Path, ""
}

// withActivity records every mutating API request.
func (s *Server) withActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Activity == nil || !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		action, target := s.describe(r)
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		e := activity.Entry{
			Source:   "agent",
			Action:   action,
			Env:      r.URL.Query().Get("env"),
			Target:   target,
			OK:       rec.status < 400,
			Duration: time.Since(start).Round(time.Millisecond).String(),
		}
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			e.Remote = host
		}
		if p := principalOf(r); p != nil {
			e.User = p.Name
		}
		if !e.OK {
			var ae apiError
			if json.Unmarshal(rec.body.Bytes(), &ae) == nil && ae.Error.Message != "" {
				e.Detail = ae.Error.Message
			} else {
				e.Detail = strings.TrimSpace(rec.body.String())
			}
		}
		s.Activity.Add(e)
	})
}

// ---------- storage (kv) ----------

type storageArea struct {
	Storage  string    `json:"storage"`
	Script   string    `json:"script"`
	Size     int64     `json:"size"`
	Keys     int       `json:"keys"`
	Modified time.Time `json:"modified"`
	UsedBy   []string  `json:"usedBy"`
}

// kvRoot returns <stateDir>/kv of the selected environment's daemon.
func (s *Server) kvRoot(w http.ResponseWriter, r *http.Request) (string, map[string][]string, bool) {
	e, c, ok := s.client(w, r)
	if !ok {
		return "", nil, false
	}
	info, err := c.Info(r.Context())
	if err != nil {
		daemonErr(w, e, err)
		return "", nil, false
	}
	// Which boxes use which storage area (default: the box name).
	used := map[string][]string{}
	if boxes, err := c.List(r.Context(), true); err == nil {
		for _, b := range boxes {
			st := b.Name
			if i, err := c.Inspect(r.Context(), b.Name); err == nil && i.Spec.Storage != "" {
				st = i.Spec.Storage
			}
			used[st] = append(used[st], b.Name)
		}
	}
	return filepath.Join(info.StateDir, "kv"), used, true
}

func (s *Server) listStorage(w http.ResponseWriter, r *http.Request) {
	root, used, ok := s.kvRoot(w, r)
	if !ok {
		return
	}
	out := []storageArea{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		dir, file := filepath.Split(rel)
		storage := strings.Trim(filepath.ToSlash(dir), "/")
		if storage == "" || strings.Contains(storage, "/") {
			return nil
		}
		a := storageArea{Storage: storage, Script: strings.TrimSuffix(file, ".json"), UsedBy: used[storage]}
		if fi, err := d.Info(); err == nil {
			a.Size, a.Modified = fi.Size(), fi.ModTime()
		}
		if a.Size <= 2<<20 {
			if b, err := os.ReadFile(path); err == nil {
				var m map[string]json.RawMessage
				if json.Unmarshal(b, &m) == nil {
					a.Keys = len(m)
				}
			}
		}
		if a.UsedBy == nil {
			a.UsedBy = []string{}
		}
		out = append(out, a)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Storage != out[j].Storage {
			return out[i].Storage < out[j].Storage
		}
		return out[i].Script < out[j].Script
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) readStorage(w http.ResponseWriter, r *http.Request) {
	storage, script := r.PathValue("storage"), r.PathValue("script")
	if !sdkRe.MatchString(storage) || !sdkRe.MatchString(script) || strings.Contains(storage+script, "..") {
		writeErr(w, http.StatusBadRequest, "invalid", "invalid storage area")
		return
	}
	root, _, ok := s.kvRoot(w, r)
	if !ok {
		return
	}
	path := filepath.Join(root, storage, script+".json")
	fi, err := os.Stat(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "storage area not found")
		return
	}
	if fi.Size() > 2<<20 {
		writeErr(w, http.StatusBadRequest, "invalid", "storage area is larger than 2 MiB")
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "invalid kv file: "+err.Error())
		return
	}
	type kv struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
		Size  int             `json:"size"`
	}
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{Key: k, Value: v, Size: len(k) + len(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeJSON(w, http.StatusOK, out)
}
