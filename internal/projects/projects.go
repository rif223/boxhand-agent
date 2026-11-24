// Package projects keeps the list of tsbox.json projects that Boxhand manages.
package projects

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// FileName is the default project file name of tsbox.
const FileName = "tsbox.json"

// Project is a registered tsbox.json.
type Project struct {
	ID    string    `json:"id"`
	File  string    `json:"file"` // absolute path to tsbox.json
	Env   string    `json:"env"`  // environment (daemon) the project runs in
	Added time.Time `json:"added"`
}

// Dir is the directory of the project file.
func (p Project) Dir() string { return filepath.Dir(p.File) }

// Store persists projects in a JSON file.
type Store struct {
	path string
	mu   sync.Mutex
	list []Project
}

// Open loads (or creates) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.list, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns all projects sorted by path.
func (s *Store) List() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Project(nil), s.list...)
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// Get returns a project by ID.
func (s *Store) Get(id string) (Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.list {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// Normalize turns a directory or file path into the absolute path of a tsbox.json.
func Normalize(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// A path without extension is a folder, even if it does not exist yet (new project).
	if fi, err := os.Stat(abs); (err == nil && fi.IsDir()) || (err != nil && filepath.Ext(abs) == "") {
		abs = filepath.Join(abs, FileName)
	}
	if filepath.Ext(abs) != ".json" {
		return "", errors.New("path must be a directory or a .json file")
	}
	return abs, nil
}

// Add registers a project file. If content is non-empty the file is created (it must not exist yet).
func (s *Store) Add(path, env string, content []byte) (Project, error) {
	file, err := Normalize(path)
	if err != nil {
		return Project{}, err
	}
	if len(content) > 0 {
		if _, err := os.Stat(file); err == nil {
			return Project{}, fmt.Errorf("%s exists already", file)
		}
		if err := ValidateJSON(content); err != nil {
			return Project{}, err
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return Project{}, err
		}
		if err := os.WriteFile(file, content, 0o644); err != nil {
			return Project{}, err
		}
	} else if _, err := os.Stat(file); err != nil {
		return Project{}, fmt.Errorf("%s not found", file)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.list {
		if filepath.Clean(p.File) == filepath.Clean(file) {
			return p, nil
		}
	}
	p := Project{ID: newID(), File: file, Env: env, Added: time.Now().UTC()}
	s.list = append(s.list, p)
	return p, s.save()
}

// SetEnv moves a project to another environment.
func (s *Store) SetEnv(id, env string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.list {
		if p.ID == id {
			s.list[i].Env = env
			return s.list[i], s.save()
		}
	}
	return Project{}, os.ErrNotExist
}

// Remove unregisters a project (the file stays).
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.list {
		if p.ID == id {
			s.list = append(s.list[:i], s.list[i+1:]...)
			return s.save()
		}
	}
	return os.ErrNotExist
}

// Read returns the content of the project file.
func (p Project) Read() ([]byte, error) { return os.ReadFile(p.File) }

// Write replaces the content of the project file after a JSON check.
func (p Project) Write(content []byte) error {
	if err := ValidateJSON(content); err != nil {
		return err
	}
	tmp := p.File + ".boxhand.tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.File)
}

// Name returns the "name" field of the project file ("" if unreadable).
func (p Project) Name() string {
	b, err := p.Read()
	if err != nil {
		return ""
	}
	var v struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(b, &v)
	return v.Name
}

var nameRe = regexp.MustCompile(`^[a-z0-9-]{1,48}$`)

// ValidateJSON does a light check; the full validation happens in tsbox up.
func ValidateJSON(b []byte) error {
	var v struct {
		Name  string                     `json:"name"`
		Boxes map[string]json.RawMessage `json:"boxes"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if v.Name != "" && !nameRe.MatchString(v.Name) {
		return fmt.Errorf("name %q: allowed are a-z, 0-9 and -, at most 48 characters", v.Name)
	}
	if len(v.Boxes) == 0 {
		return errors.New(`"boxes" is missing or empty`)
	}
	return nil
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
