package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A box can pick its driver (tsbox 0.2); unknown names are rejected before the CLI runs.
func TestRunDriver(t *testing.T) {
	h := newServer(t).Handler()
	entry := filepath.Join(t.TempDir(), "a.ts")
	if err := os.WriteFile(entry, []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := func(driver string) string {
		b, _ := json.Marshal(map[string]string{"entry": entry, "driver": driver})
		return string(b)
	}
	if w := request(h, "POST", "/api/run", testToken, body("docker")); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "driver") {
		t.Errorf("unknown driver: %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/api/run", testToken, body("gvisor")); w.Code == http.StatusBadRequest {
		t.Errorf("gvisor rejected: %s", w.Body)
	}
}
