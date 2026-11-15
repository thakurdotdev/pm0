package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeReleases serves the two GitHub URL shapes upgrade uses.
func fakeReleases(t *testing.T, tag string, assets map[string][]byte) Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/releases/download/"+tag+"/"):
			if b, ok := assets[strings.TrimPrefix(r.URL.Path, "/releases/download/"+tag+"/")]; ok {
				_, _ = w.Write(b)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return Source{BaseURL: srv.URL, HTTP: srv.Client()}
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestLatestTagAndDownload(t *testing.T) {
	bin := []byte("binary-bytes")
	src := fakeReleases(t, "v0.2.0", map[string][]byte{
		"pm0-linux-arm64": bin,
		"checksums.txt":   []byte(sum(bin) + "  pm0-linux-arm64\nffff  pm0-linux-amd64\n"),
	})
	ctx := context.Background()
	tag, err := src.LatestTag(ctx)
	if err != nil || tag != "v0.2.0" {
		t.Fatalf("LatestTag = %q, %v", tag, err)
	}
	sums, err := src.Download(ctx, tag, "checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	want, err := ExpectedSum(sums, "pm0-linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Download(ctx, tag, "pm0-linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(got, want); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := Verify([]byte("tampered"), want); err == nil {
		t.Fatal("tampered download verified")
	}
	if _, err := ExpectedSum(sums, "pm0-linux-386"); err == nil {
		t.Fatal("missing asset found in checksums")
	}
	if _, err := src.Download(ctx, tag, "nope"); err == nil {
		t.Fatal("404 asset downloaded")
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.9", "0.1.8", 1},
		{"v0.1.8", "0.1.8", 0},
		{"0.1.10", "0.1.9", 1}, // numeric, not lexical
		{"1.0.0", "0.9.9", 1},
		{"0.1.9-rc1", "0.1.9", -1},
		{"0.1.8", "0.0.0-m6", 1}, // dev build upgrades to any release
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// writeFakeBinary writes an executable that prints `pm0 <ver> (x)`.
func writeFakeBinary(ver string) []byte {
	return []byte("#!/bin/sh\necho 'pm0 " + ver + " (test)'\n")
}

func TestReplaceSwapsAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pm0")
	if err := os.WriteFile(target, writeFakeBinary("0.1.8"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Wrong version: refused, old binary untouched.
	if err := Replace(target, writeFakeBinary("0.1.7"), "0.2.0"); err == nil {
		t.Fatal("binary reporting the wrong version was installed")
	}
	// Does not run: refused.
	if err := Replace(target, []byte("\x7fELF garbage"), "0.2.0"); err == nil {
		t.Fatal("non-runnable binary was installed")
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "0.1.8") {
		t.Fatalf("failed replace modified the target: %q", b)
	}
	if err := Replace(target, writeFakeBinary("0.2.0"), "v0.2.0"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "0.2.0") {
		t.Fatalf("target not replaced: %q", b)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v", st.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".pm0.upgrade.*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestReplaceReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "pm0")
	if err := os.WriteFile(target, writeFakeBinary("0.1.8"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := Replace(target, writeFakeBinary("0.2.0"), "0.2.0"); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("err = %v, want ErrNotWritable", err)
	}
}
