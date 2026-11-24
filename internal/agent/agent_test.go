package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCertIsStable(t *testing.T) {
	dir := t.TempDir()
	_, a, err := EnsureCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := EnsureCert(dir)
	if err != nil || a != b || len(a) != 95 {
		t.Fatalf("fingerprints %q %q (%v)", a, b, err)
	}
	if st, err := os.Stat(filepath.Join(dir, keyFile)); err != nil || st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("key file mode: %v %v", st, err)
	}
}

func TestToken(t *testing.T) {
	dir := t.TempDir()
	if _, err := TokenKey(dir); err == nil {
		t.Fatal("token key without init")
	}
	key := func(s string) string { return strings.Repeat("0", 64-len(s)%10) + strings.Repeat("1", len(s)%10) }
	token, err := NewToken(dir, key)
	if err != nil || !strings.HasPrefix(token, "bha_") {
		t.Fatal(token, err)
	}
	got, err := TokenKey(dir)
	if err != nil || got != key(token) {
		t.Fatalf("%q %v", got, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, confFile))
	if strings.Contains(string(b), token) {
		t.Error("the token itself was stored")
	}
}
