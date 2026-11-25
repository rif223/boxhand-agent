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
	// A box needs a driver: none, "auto" and unknown ones are refused.
	for _, d := range []string{"", "auto", "docker"} {
		if w := request(h, "POST", "/api/run", testToken, body(d)); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "driver") {
			t.Errorf("driver %q: %d %s", d, w.Code, w.Body)
		}
	}
	if w := request(h, "POST", "/api/run", testToken, body("gvisor")); w.Code == http.StatusBadRequest {
		t.Errorf("gvisor rejected: %s", w.Body)
	}
}
