// Package cli runs the tsbox command-line tool for operations that the daemon API does not offer
// (projects: up/down, doctor, autostarting a user daemon).
package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// Runner runs the tsbox binary.
type Runner struct {
	Bin string   // path or name of the tsbox binary
	Env []string // extra environment (TSBOX_HOST, TSBOX_STATE, TSBOX_DRIVER, …)
}

// Result is the outcome of a CLI call.
type Result struct {
	Command  []string `json:"command"`
	Output   string   `json:"output"`
	ExitCode int      `json:"exitCode"`
	Duration string   `json:"duration"`
}

// Run executes tsbox with args in dir (may be empty) and returns the combined output.
func (r Runner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), r.Env...)
	cmd.Env = append(cmd.Env, "NO_COLOR=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	res := Result{
		Command:  append([]string{"tsbox"}, args...),
		Output:   out.String(),
		Duration: time.Since(start).Round(time.Millisecond).String(),
	}
	if ee, ok := err.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	if err != nil {
		res.ExitCode = -1
		return res, err
	}
	return res, nil
}

// Available reports whether the binary can be found.
func (r Runner) Available() (string, bool) {
	p, err := exec.LookPath(r.Bin)
	return p, err == nil
}
