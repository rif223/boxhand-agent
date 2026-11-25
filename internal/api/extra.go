package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rif223/tsbox"
	"github.com/rif223/tsbox/client"
)

// ---------- project files ----------

// Editable text files; everything else is listed but read-only.
var editableExt = map[string]bool{
	".ts": true, ".mts": true, ".tsx": true, ".js": true, ".mjs": true, ".json": true, ".jsonc": true,
	".md": true, ".txt": true, ".env": true, ".yml": true, ".yaml": true, ".toml": true, ".sql": true, ".html": true, ".css": true,
}

var skipDirs = map[string]bool{"node_modules": true, ".git": true, ".tsbox": true, ".svelte-kit": true, "dist": true, "build": true}

const maxFileSize = 1 << 20

type fileEntry struct {
	Path     string `json:"path"` // slash-separated, relative to the project directory
	Size     int64  `json:"size"`
	Editable bool   `json:"editable"`
}

// projectPath resolves rel inside the project directory and refuses anything outside of it.
func projectPath(dir, rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", errors.New("path is required")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path must stay inside the project directory")
	}
	full := filepath.Join(dir, clean)
	// Refuse symlinks that point outside of the project.
	if real, err := filepath.EvalSymlinks(filepath.Dir(full)); err == nil {
		base, _ := filepath.EvalSymlinks(dir)
		if r, err := filepath.Rel(base, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", errors.New("path must stay inside the project directory")
		}
	}
	return full, nil
}

func (s *Server) projectFiles(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	dir := p.Dir()
	out := []fileEntry{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= 500 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		out = append(out, fileEntry{
			Path:     filepath.ToSlash(rel),
			Size:     info.Size(),
			Editable: editableExt[strings.ToLower(filepath.Ext(path))] && info.Size() <= maxFileSize,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) readProjectFile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	full, err := projectPath(p.Dir(), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	st, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if st.Size() > maxFileSize {
		writeErr(w, http.StatusBadRequest, "invalid", "file is larger than 1 MiB")
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": r.URL.Query().Get("path"), "content": string(b)})
}

func (s *Server) writeProjectFile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := projectPath(p.Dir(), rel)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if !editableExt[strings.ToLower(filepath.Ext(full))] {
		writeErr(w, http.StatusBadRequest, "invalid", "this file type cannot be edited")
		return
	}
	var req struct {
		Content string `json:"content"`
		Create  bool   `json:"create"` // fail if the file exists
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxFileSize+4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if len(req.Content) > maxFileSize {
		writeErr(w, http.StatusBadRequest, "invalid", "file is larger than 1 MiB")
		return
	}
	if req.Create {
		if _, err := os.Stat(full); err == nil {
			writeErr(w, http.StatusConflict, "conflict", rel+" exists already")
			return
		}
	}
	if filepath.Clean(full) == filepath.Clean(p.File) {
		// The project file gets the JSON check of the project store.
		if err := s.writeProject(r, p, []byte(req.Content)); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
	} else {
		if err := s.writeScript(r, p, rel, full, []byte(req.Content)); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": rel})
}

// ---------- run a single script as a box ----------

type runRequest struct {
	Entry    string   `json:"entry"` // absolute path of the .ts file on the Boxhand host
	Name     string   `json:"name"`
	Memory   string   `json:"memory"`
	CPU      float64  `json:"cpu"`
	PIDs     int      `json:"pids"`
	Restart  string   `json:"restart"`
	SDKs     []string `json:"sdks"`
	Hosts    []string `json:"hosts"` // websites the box may reach (grants of the http SDK)
	Params   string   `json:"params"`
	Timeout  string   `json:"timeout"`
	Storage  string   `json:"storage"`
	Driver   string   `json:"driver"` // required: every box names its own
	RemoveOn bool     `json:"rm"`
}

var (
	boxNameRe = regexp.MustCompile(`^[a-z0-9-]{1,48}$`)
	sizeRe    = regexp.MustCompile(`^[0-9]+(Ki|Mi|Gi)?$`)
	restartRe = regexp.MustCompile(`^(no|always|on-failure(:[0-9]+)?)$`)
	sdkRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	hostRe    = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?(:[0-9]{1,5})?$`)
)

func (s *Server) runScript(w http.ResponseWriter, r *http.Request) {
	e, ok := s.env(w, r)
	if !ok {
		return
	}
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	bad := func(msg string) { writeErr(w, http.StatusBadRequest, "invalid", msg) }
	if !filepath.IsAbs(req.Entry) {
		bad("script path must be absolute")
		return
	}
	if st, err := os.Stat(req.Entry); err != nil || st.IsDir() {
		bad(req.Entry + " not found")
		return
	}
	args := []string{"run", "-d", "-q"}
	if req.Name != "" {
		if !boxNameRe.MatchString(req.Name) {
			bad("name: allowed are a-z, 0-9 and -, at most 48 characters")
			return
		}
		args = append(args, "--name", req.Name)
	}
	if req.Memory != "" {
		if !sizeRe.MatchString(req.Memory) {
			bad("memory: e.g. 128Mi or 1Gi")
			return
		}
		args = append(args, "--mem", req.Memory)
	}
	if req.CPU > 0 {
		args = append(args, "--cpu", strconv.FormatFloat(req.CPU, 'f', -1, 64))
	}
	if req.PIDs > 0 {
		args = append(args, "--pids", strconv.Itoa(req.PIDs))
	}
	if req.Restart != "" {
		if !restartRe.MatchString(req.Restart) {
			bad("restart: no, always or on-failure[:n]")
			return
		}
		args = append(args, "--restart", req.Restart)
	}
	if req.Timeout != "" {
		if _, err := time.ParseDuration(req.Timeout); err != nil {
			bad("timeout: e.g. 30s or 5m")
			return
		}
		args = append(args, "--timeout", req.Timeout)
	}
	if req.Storage != "" {
		if !sdkRe.MatchString(req.Storage) {
			bad("storage: letters, digits, _ . -")
			return
		}
		args = append(args, "--storage", req.Storage)
	}
	for _, sdk := range req.SDKs {
		if !sdkRe.MatchString(sdk) {
			bad("sdk " + strconv.Quote(sdk) + ": invalid name")
			return
		}
		args = append(args, "--sdk", sdk)
	}
	if len(req.Hosts) > 0 && !slices.Contains(req.SDKs, "http") {
		args = append(args, "--sdk", "http")
	}
	for _, host := range req.Hosts {
		if !hostRe.MatchString(host) {
			bad("website " + strconv.Quote(host) + ": expected a host name such as api.example.com")
			return
		}
		args = append(args, "--grant", "http:host="+host)
	}
	if strings.TrimSpace(req.Params) != "" {
		if !json.Valid([]byte(req.Params)) {
			bad("params: not valid JSON")
			return
		}
		args = append(args, "--params", req.Params)
	}
	switch req.Driver {
	case "gvisor", "firecracker", "dev":
		args = append(args, "--driver", req.Driver)
	default:
		bad("driver: gvisor, firecracker or dev")
		return
	}
	if req.RemoveOn {
		args = append(args, "--rm")
	}
	args = append(args, req.Entry)

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := e.CLI.Run(ctx, filepath.Dir(req.Entry), args...)
	e.Daemon.Reset()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cli", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- SDKs ----------

type sdkRow struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Import   string `json:"import"`
	Provider string `json:"provider"`
	Source   string `json:"source"`
}

// listSDKs parses the table of `tsbox sdk ls` (columns separated by two or more spaces).
func (s *Server) listSDKs(w http.ResponseWriter, r *http.Request) {
	e, ok := s.env(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := e.CLI.Run(ctx, "", "sdk", "ls")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cli", err.Error())
		return
	}
	if res.ExitCode != 0 {
		writeErr(w, http.StatusBadGateway, "cli", strings.TrimSpace(res.Output))
		return
	}
	split := regexp.MustCompile(`\s{2,}`)
	rows := []sdkRow{}
	sc := bufio.NewScanner(strings.NewReader(res.Output))
	header := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \r")
		if line == "" {
			continue
		}
		if header {
			header = false
			if strings.HasPrefix(line, "NAME") {
				continue
			}
		}
		f := split.Split(strings.TrimSpace(line), 5)
		for len(f) < 5 {
			f = append(f, "")
		}
		rows = append(rows, sdkRow{Name: f[0], Version: f[1], Import: f[2], Provider: f[3], Source: f[4]})
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) sdkInfo(w http.ResponseWriter, r *http.Request) {
	e, ok := s.env(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !sdkRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "invalid", "invalid SDK name")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := e.CLI.Run(ctx, "", "sdk", "info", name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cli", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- logs of several boxes in one stream ----------

// maxLogBoxes is how many boxes one merged log stream can follow.
const maxLogBoxes = 100

// multiLogs merges the log streams of several boxes (?box=a&box=b) into one SSE stream.
func (s *Server) multiLogs(w http.ResponseWriter, r *http.Request) {
	e, c, ok := s.client(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	names := q["box"]
	if len(names) == 0 || len(names) > maxLogBoxes {
		writeErr(w, http.StatusBadRequest, "invalid", fmt.Sprintf("choose 1 to %d boxes", maxLogBoxes))
		return
	}
	tail := 200
	if t, err := strconv.Atoi(q.Get("tail")); err == nil && t >= 0 {
		tail = t
	}
	ctx := r.Context()
	type item struct {
		line tsbox.LogLine
		err  error
	}
	ch := make(chan item, 256)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for l, err := range followLogs(ctx, c, name, client.LogQuery{Follow: true, Tail: tail}) {
				select {
				case ch <- item{l, err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	seq := func(yield func(tsbox.LogLine, error) bool) {
		for it := range ch {
			if it.err != nil {
				continue // one box ending its stream must not end the others
			}
			if !yield(it.line, nil) {
				return
			}
		}
	}
	streamSSE(w, r, e, seq)
}

// followLogs reads the logs of a box and keeps following them. Some tsbox versions end a follow stream
// right after the stored lines (seen with the dev driver), so when the stream ends while ctx is still
// alive, it reads again from the last line on and drops lines it has already sent (by sequence number).
func followLogs(ctx context.Context, c *client.Client, name string, q client.LogQuery) iter.Seq2[tsbox.LogLine, error] {
	return func(yield func(tsbox.LogLine, error) bool) {
		var last int64
		var lastAt time.Time
		wait := time.Second
		for first := true; ; first = false {
			if !first {
				q.Tail = 0
				q.Since = lastAt
			}
			got := false
			for l, err := range c.Logs(ctx, name, q) {
				if err != nil {
					if ctx.Err() == nil && first {
						yield(l, err)
					}
					return
				}
				if l.Seq <= last {
					continue
				}
				last, lastAt = l.Seq, l.At
				got = true
				if !yield(l, nil) {
					return
				}
			}
			if !q.Follow {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if got {
				wait = time.Second
			} else {
				wait = min(wait*2, 10*time.Second)
			}
		}
	}
}
