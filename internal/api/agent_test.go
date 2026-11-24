package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rif223/boxhand-agent/internal/activity"
	"github.com/rif223/boxhand-agent/internal/envs"
	"github.com/rif223/boxhand-agent/internal/projects"
)

const testToken = "bha_test-token"

func newServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	p, err := projects.Open(filepath.Join(dir, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := activity.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		Envs:     envs.New("tsbox", "unix:///nonexistent/tsboxd.sock"),
		Projects: p, Activity: l, History: projects.OpenHistory(dir), DataDir: dir,
		TokenKey: TokenKey(testToken),
	}
}

func request(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:1234"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method != http.MethodGet {
		r.Header.Set("X-Boxhand", "1")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestToken(t *testing.T) {
	h := newServer(t).Handler()
	for _, c := range []struct {
		token string
		want  int
	}{{"", 401}, {"bha_wrong", 401}, {testToken, 200}} {
		if w := request(h, "GET", "/api/info", c.token, ""); w.Code != c.want {
			t.Errorf("token %q: %d, want %d", c.token, w.Code, c.want)
		}
	}
}

func TestWithoutTokenKeyNothingWorks(t *testing.T) {
	s := newServer(t)
	s.TokenKey = ""
	if w := request(s.Handler(), "GET", "/api/info", testToken, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no token key: %d", w.Code)
	}
}

// The agent offers no accounts, tokens, settings, schedules, notifications or UI.
func TestOnlyAgentRoutes(t *testing.T) {
	h := newServer(t).Handler()
	for _, p := range []string{"/api/auth/status", "/api/users", "/api/tokens", "/api/backup", "/api/metrics",
		"/api/settings", "/api/notifications", "/api/schedules", "/api/activity", "/", "/index.html"} {
		if w := request(h, "GET", p, testToken, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/api/auth/login", "/api/envs", "/api/tokens"} {
		if w := request(h, "POST", p, testToken, "{}"); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d", p, w.Code)
		}
	}
	if w := request(h, "GET", "/api/projects", testToken, ""); w.Code != http.StatusOK {
		t.Errorf("projects: %d", w.Code)
	}
}

func TestRoutesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range newServer(t).routes() {
		if seen[r.pattern] {
			t.Errorf("route %q twice", r.pattern)
		}
		seen[r.pattern] = true
	}
}

func TestGuardNeedsHeader(t *testing.T) {
	h := Guard{}.Wrap(newServer(t).Handler())
	r := httptest.NewRequest("POST", "/api/boxes/web/stop", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST without X-Boxhand: %d", w.Code)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security headers missing")
	}
}

func TestChangesAreRecorded(t *testing.T) {
	s := newServer(t)
	request(s.Handler(), "POST", "/api/boxes/web/stop", testToken, "")
	list := s.Activity.List(10)
	if len(list) != 1 || list[0].Action != "box.stop" || list[0].Target != "web" || list[0].User != "agent" || list[0].Source != "agent" {
		t.Fatalf("activity: %+v", list)
	}
	if list[0].OK {
		t.Error("a failed stop (no daemon) was recorded as OK")
	}
}
