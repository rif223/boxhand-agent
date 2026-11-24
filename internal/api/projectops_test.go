package api

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestCheckTargets(t *testing.T) {
	dir := t.TempDir()
	content := []byte(`{"boxes":{"main":{
		"sdks":{"log":{},"http":{"grants":{"host":["example.com"]},"config":{"token":"literal-secret"}}},
		"scripts":{"a":{"entry":"src/a.ts"},"evil":{"entry":"../outside.ts"},"flag":{"entry":"-x.ts"}}
	}}}`)
	targets, err := checkTargets(dir, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 {
		t.Fatalf("%d targets", len(targets))
	}
	a := targets[0]
	if a.err != nil || !slices.Contains(a.args, "http:host=example.com") || a.args[len(a.args)-1] != filepath.Join(dir, "src", "a.ts") {
		t.Fatalf("args: %v, %v", a.args, a.err)
	}
	if slices.ContainsFunc(a.args, func(s string) bool { return s == "--sdk-config" || s == "literal-secret" }) {
		t.Fatalf("settings passed on the command line: %v", a.args)
	}
	if targets[1].err == nil {
		t.Fatal("entry outside the project accepted")
	}
	if last := targets[2].args[len(targets[2].args)-1]; last != filepath.Join(dir, "-x.ts") {
		t.Fatalf("entry can be read as a flag: %q", last)
	}
}
