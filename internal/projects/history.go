package projects

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// keepVersions is how many earlier versions are kept per file of a project.
const keepVersions = 30

var (
	// ErrNoVersion means that a version does not exist.
	ErrNoVersion = errors.New("version not found")

	historyIDRe = regexp.MustCompile(`^[a-f0-9]{1,32}$`)
	versionRe   = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// Version describes an earlier state of a file of a project.
type Version struct {
	ID   string    `json:"id"`
	Path string    `json:"path"` // relative to the project; empty = the project file
	At   time.Time `json:"at"`   // when this state was replaced
	User string    `json:"user"` // who replaced it (empty for changes without an account)
	Size int       `json:"size"`
}

type storedVersion struct {
	At      time.Time `json:"at"`
	User    string    `json:"user,omitempty"`
	Path    string    `json:"path,omitempty"`
	Content string    `json:"content"`
}

// History keeps earlier versions of project files in dir/<project>/<version>.json.
type History struct {
	dir string
	mu  sync.Mutex
}

// OpenHistory uses dataDir/history.
func OpenHistory(dataDir string) *History {
	return &History{dir: filepath.Join(dataDir, "history")}
}

func (h *History) projectDir(id string) (string, error) {
	if !historyIDRe.MatchString(id) {
		return "", ErrNoVersion
	}
	return filepath.Join(h.dir, id), nil
}

func (h *History) ids(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && versionRe.MatchString(id) {
			out = append(out, id)
		}
	}
	// Newest first; the IDs are nanosecond timestamps.
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.ParseInt(out[i], 10, 64)
		b, _ := strconv.ParseInt(out[j], 10, 64)
		return a > b
	})
	return out
}

func readVersion(path string) (storedVersion, error) {
	var v storedVersion
	b, err := os.ReadFile(path)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(b, &v)
}

// Save stores the content of a file that is about to be replaced (path "" = the project file).
// The same content twice in a row is stored once.
func (h *History) Save(id, path string, content []byte, user string) error {
	if h == nil {
		return nil
	}
	dir, err := h.projectDir(id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var same []string // versions of this file, newest first
	duplicate := false
	for _, v := range h.ids(dir) {
		s, err := readVersion(filepath.Join(dir, v+".json"))
		if err != nil || s.Path != path {
			continue
		}
		if len(same) == 0 && s.Content == string(content) {
			duplicate = true
		}
		same = append(same, v)
	}
	if duplicate {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	now := time.Now().UTC()
	b, err := json.Marshal(storedVersion{At: now, User: user, Path: path, Content: string(content)})
	if err != nil {
		return err
	}
	name := strconv.FormatInt(now.UnixNano(), 10)
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o600); err != nil {
		return fmt.Errorf("history: %w", err)
	}
	if len(same) >= keepVersions {
		for _, old := range same[keepVersions-1:] {
			_ = os.Remove(filepath.Join(dir, old+".json"))
		}
	}
	return nil
}

// List returns the versions of all files of a project, newest first.
func (h *History) List(id string) []Version {
	out := []Version{}
	if h == nil {
		return out
	}
	dir, err := h.projectDir(id)
	if err != nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range h.ids(dir) {
		if s, err := readVersion(filepath.Join(dir, v+".json")); err == nil {
			out = append(out, Version{ID: v, Path: s.Path, At: s.At, User: s.User, Size: len(s.Content)})
		}
	}
	return out
}

// Get returns the file path ("" = project file) and content of one version.
func (h *History) Get(id, version string) (string, string, error) {
	if h == nil || !versionRe.MatchString(version) {
		return "", "", ErrNoVersion
	}
	dir, err := h.projectDir(id)
	if err != nil {
		return "", "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, err := readVersion(filepath.Join(dir, version+".json"))
	if err != nil {
		return "", "", ErrNoVersion
	}
	return s.Path, s.Content, nil
}

// Remove deletes the versions of a project that is no longer managed.
func (h *History) Remove(id string) {
	if h == nil {
		return
	}
	if dir, err := h.projectDir(id); err == nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		_ = os.RemoveAll(dir)
	}
}
