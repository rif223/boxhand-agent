package projects

import (
	"fmt"
	"testing"
)

func TestHistoryKeepsNewestAndSkipsDuplicates(t *testing.T) {
	h := OpenHistory(t.TempDir())
	for i := range keepVersions + 5 {
		if err := h.Save("abc123", "", fmt.Appendf(nil, "v%d", i), "root"); err != nil {
			t.Fatal(err)
		}
	}
	list := h.List("abc123")
	if len(list) != keepVersions {
		t.Fatalf("kept %d versions", len(list))
	}
	_, newest, err := h.Get("abc123", list[0].ID)
	if err != nil || newest != fmt.Sprintf("v%d", keepVersions+4) {
		t.Fatalf("newest = %q, %v", newest, err)
	}
	if err := h.Save("abc123", "", []byte(newest), "root"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.List("abc123")); n != keepVersions {
		t.Fatalf("duplicate stored: %d", n)
	}
	h.Remove("abc123")
	if n := len(h.List("abc123")); n != 0 {
		t.Fatalf("%d versions left after remove", n)
	}
}

func TestHistoryKeepsFilesApart(t *testing.T) {
	h := OpenHistory(t.TempDir())
	for i := range keepVersions + 3 {
		if err := h.Save("abc123", "src/a.ts", fmt.Appendf(nil, "a%d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	// Same content as the newest version of another file: still a version of this file.
	if err := h.Save("abc123", "src/b.ts", fmt.Appendf(nil, "a%d", keepVersions+2), ""); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, v := range h.List("abc123") {
		counts[v.Path]++
	}
	if counts["src/a.ts"] != keepVersions || counts["src/b.ts"] != 1 {
		t.Fatalf("counts: %v", counts)
	}
}

func TestHistoryRejectsPathsAsIDs(t *testing.T) {
	h := OpenHistory(t.TempDir())
	if err := h.Save("../evil", "", []byte("x"), ""); err == nil {
		t.Fatal("path accepted as project id")
	}
	if _, _, err := h.Get("abc123", "../../secret"); err == nil {
		t.Fatal("path accepted as version")
	}
}
