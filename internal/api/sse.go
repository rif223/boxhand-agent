package api

import (
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"sync"
	"time"

	"github.com/rif223/boxhand-agent/internal/daemon"
	"github.com/rif223/boxhand-agent/internal/envs"
)

// streamSSE forwards an NDJSON stream of the daemon as Server-Sent Events.
//
// Events: "message" (data = JSON item), "error" (data = {"message"}) and a final "end".
func streamSSE[T any](w http.ResponseWriter, r *http.Request, e *envs.Env, seq iter.Seq2[T, error]) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming not supported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	var mu sync.Mutex
	send := func(event string, data []byte) bool {
		mu.Lock()
		defer mu.Unlock()
		if event != "" {
			if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		fl.Flush()
		return true
	}

	// Heartbeat keeps proxies from closing idle streams.
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-r.Context().Done():
				return
			case <-t.C:
				mu.Lock()
				_, _ = fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
				mu.Unlock()
			}
		}
	}()

	for item, err := range seq {
		if err != nil {
			if !daemon.IsAPIError(err) && r.Context().Err() == nil {
				e.Daemon.Reset()
			}
			b, _ := json.Marshal(map[string]string{"message": err.Error()})
			send("error", b)
			return
		}
		b, err := json.Marshal(item)
		if err != nil {
			continue
		}
		if !send("", b) {
			return
		}
	}
	send("end", []byte("{}"))
}
