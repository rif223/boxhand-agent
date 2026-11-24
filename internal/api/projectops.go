package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rif223/boxhand-agent/internal/cli"
	"github.com/rif223/boxhand-agent/internal/projects"
)

// writeProject replaces the project file and keeps the previous content in the history.
func (s *Server) writeProject(r *http.Request, p projects.Project, content []byte) error {
	if err := projects.ValidateJSON(content); err != nil {
		return err
	}
	if old, err := p.Read(); err == nil && string(old) != string(content) {
		user := ""
		if who := principalOf(r); who != nil {
			user = who.Name
		}
		if err := s.History.Save(p.ID, "", old, user); err != nil {
			return err
		}
	}
	return p.Write(content)
}

// writeScript replaces another file of the project and keeps its previous content in the history.
func (s *Server) writeScript(r *http.Request, p projects.Project, rel, full string, content []byte) error {
	if !editableExt[strings.ToLower(filepath.Ext(full))] {
		return errors.New("this file type cannot be edited")
	}
	if old, err := os.ReadFile(full); err == nil && string(old) != string(content) {
		user := ""
		if who := principalOf(r); who != nil {
			user = who.Name
		}
		if err := s.History.Save(p.ID, filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))), old, user); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".boxhand.tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Server) projectHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	writeJSON(w, http.StatusOK, s.History.List(p.ID))
}

func (s *Server) projectVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	path, content, err := s.History.Get(p.ID, r.PathValue("version"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "version not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "content": content})
}

// restoreVersion puts an earlier version back. The current state goes into the history, so this can be undone.
func (s *Server) restoreVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	path, content, err := s.History.Get(p.ID, r.PathValue("version"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "version not found")
		return
	}
	if path == "" {
		err = s.writeProject(r, p, []byte(content))
	} else {
		var full string
		if full, err = projectPath(p.Dir(), path); err == nil {
			err = s.writeScript(r, p, path, full, []byte(content))
		}
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkArgs builds one `tsbox check` call per script of a project file.
type checkTarget struct {
	label string
	args  []string
	err   error
}

func checkTargets(dir string, content []byte) ([]checkTarget, error) {
	var file struct {
		Boxes map[string]struct {
			SDKs map[string]struct {
				Path   string              `json:"path"`
				Grants map[string][]string `json:"grants"`
			} `json:"sdks"`
			Scripts map[string]struct {
				Entry string `json:"entry"`
			} `json:"scripts"`
		} `json:"boxes"`
	}
	if err := json.Unmarshal(content, &file); err != nil {
		return nil, err
	}
	var out []checkTarget
	for _, boxName := range sortedKeys(file.Boxes) {
		box := file.Boxes[boxName]
		// Settings of SDKs are left out on purpose: they can hold secrets and would show up in the process list.
		var sdkArgs []string
		for _, name := range sortedKeys(box.SDKs) {
			sdk := box.SDKs[name]
			ref := name
			if sdk.Path != "" {
				full, err := projectPath(dir, sdk.Path)
				if err != nil {
					return nil, fmt.Errorf("sdk %s: %w", name, err)
				}
				ref = full
			}
			sdkArgs = append(sdkArgs, "--sdk", ref)
			for _, right := range sortedKeys(sdk.Grants) {
				for _, value := range sdk.Grants[right] {
					sdkArgs = append(sdkArgs, "--grant", name+":"+right+"="+value)
				}
			}
		}
		for _, scriptName := range sortedKeys(box.Scripts) {
			entry := box.Scripts[scriptName].Entry
			t := checkTarget{label: boxName + "/" + scriptName + " (" + entry + ")"}
			// The absolute path also keeps an entry like "-x.ts" from being read as a flag.
			if full, err := projectPath(dir, entry); err != nil {
				t.err = err
			} else {
				t.args = append(append([]string{"check"}, sdkArgs...), full)
			}
			out = append(out, t)
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkProject type-checks and builds every script without starting anything.
func (s *Server) checkProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	content, err := p.Read()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", p.File+" does not exist")
		return
	}
	targets, err := checkTargets(p.Dir(), content)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if len(targets) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "the project has no scripts")
		return
	}
	e := s.projectEnv(p)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	res := cli.Result{Command: []string{"tsbox", "check"}}
	var out strings.Builder
	failed := 0
	for _, t := range targets {
		var detail string
		bad := t.err != nil
		if bad {
			detail = t.err.Error()
		} else {
			run, err := e.CLI.Run(ctx, p.Dir(), t.args...)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
				writeErr(w, http.StatusGatewayTimeout, "timeout", "check took too long")
				return
			}
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "cli", err.Error())
				return
			}
			bad = run.ExitCode != 0
			detail = strings.TrimSpace(run.Output)
		}
		if bad {
			failed++
			if len(detail) > 4000 {
				detail = detail[:4000] + " …"
			}
			fmt.Fprintf(&out, "FAIL %s\n%s\n\n", t.label, detail)
		} else {
			fmt.Fprintf(&out, "ok   %s\n", t.label)
		}
	}
	if failed > 0 {
		res.ExitCode = 1
	}
	res.Output = strings.TrimSpace(out.String())
	res.Duration = time.Since(start).Round(time.Millisecond).String()
	writeJSON(w, http.StatusOK, res)
}
