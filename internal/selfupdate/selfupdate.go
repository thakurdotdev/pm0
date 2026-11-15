// Package selfupdate implements `pm0 upgrade`: find the newest GitHub
// release, download this platform's binary, verify it against the
// release's checksums.txt, and atomically replace the running executable.
//
// It mirrors site/public/install.sh (same repo, same asset names, same
// checksum file) so both install paths produce the same bits. The daemon
// reload afterwards is the existing `pm0 update` re-exec: the daemon execs
// its own path again, which now holds the new binary.
package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultRepo is where releases are published (install.sh's PM0_REPO).
const DefaultRepo = "github.com/thakurdotdev/pm0"

// maxBinaryBytes bounds a download (release binaries are ~16 MB).
const maxBinaryBytes = 128 << 20

// Source locates releases. BaseURL is the repo web root, e.g.
// https://github.com/thakurdotdev/pm0 (tests point it at httptest).
type Source struct {
	BaseURL string
	HTTP    *http.Client
}

// NewSource builds the default source; PM0_REPO overrides the repo the
// same way it does for install.sh.
func NewSource() Source {
	repo := os.Getenv("PM0_REPO")
	if repo == "" {
		repo = DefaultRepo
	}
	base := repo
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	return Source{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

func (s Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// LatestTag resolves the newest release tag (e.g. "v0.1.9") from the
// /releases/latest redirect — no API token, no API rate limit.
func (s Source) LatestTag(ctx context.Context) (string, error) {
	c := *s.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("check latest release: %w", err)
	}
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
		return "", fmt.Errorf("check latest release: unexpected HTTP %d from %s/releases/latest (no releases yet?)", resp.StatusCode, s.BaseURL)
	}
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 || i+5 >= len(loc) {
		return "", fmt.Errorf("check latest release: cannot read a tag from redirect %q", loc)
	}
	return loc[i+5:], nil
}

// Download fetches one release asset.
func (s Source) Download(ctx context.Context, tag, asset string) ([]byte, error) {
	url := fmt.Sprintf("%s/releases/download/%s/%s", s.BaseURL, tag, asset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d (%s)", asset, resp.StatusCode, url)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBinaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	if len(data) > maxBinaryBytes {
		return nil, fmt.Errorf("download %s: larger than %d bytes", asset, maxBinaryBytes)
	}
	return data, nil
}

// AssetName is the raw binary asset for a platform (Makefile `release`).
func AssetName(goos, goarch string) string { return "pm0-" + goos + "-" + goarch }

// ExpectedSum finds asset's SHA-256 in a sha256sum-format checksums.txt.
func ExpectedSum(checksums []byte, asset string) (string, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", asset)
}

// Verify checks data against the expected hex SHA-256.
func Verify(data []byte, expected string) error {
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != expected {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, expected)
	}
	return nil
}

// Normalize strips a leading "v" ("v0.1.9" -> "0.1.9").
func Normalize(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// Compare orders two versions: -1, 0 or 1. Numeric x.y.z parts compare
// numerically; a pre-release/build suffix ("0.1.9-rc1", "0.0.0-m6",
// git-describe "0.1.8-3-gabc") sorts before the bare release.
func Compare(a, b string) int {
	a, b = Normalize(a), Normalize(b)
	ac, as, _ := strings.Cut(a, "-")
	bc, bs, _ := strings.Cut(b, "-")
	ap, bp := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := 0; i < 3; i++ {
		x, y := part(ap, i), part(bp, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case as == bs:
		return 0
	case as == "":
		return 1
	case bs == "":
		return -1
	case as < bs:
		return -1
	default:
		return 1
	}
}

func part(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	n, _ := strconv.Atoi(p[i])
	return n
}

// ErrNotWritable means the binary's directory cannot be written by this
// user (system install): the caller should suggest sudo.
var ErrNotWritable = errors.New("install directory not writable")

// Replace atomically swaps the file at target for data: the new binary is
// written next to it (same filesystem), smoke-tested with `version`, then
// renamed over the old one. Processes already running the old binary keep
// their inode; the next exec of target gets the new one. wantVersion, when
// set, must appear in the new binary's `version` output.
func Replace(target string, data []byte, wantVersion string) error {
	dir := filepath.Dir(target)
	mode := os.FileMode(0o755)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".pm0.upgrade.*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w: %s", ErrNotWritable, dir)
		}
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmpPath, "version").Output()
	if err != nil {
		return fmt.Errorf("downloaded binary does not run on this machine: %w", err)
	}
	if wantVersion != "" && !bytes.Contains(out, []byte(Normalize(wantVersion))) {
		return fmt.Errorf("downloaded binary reports %q, expected version %s", strings.TrimSpace(string(out)), Normalize(wantVersion))
	}
	if err := os.Rename(tmpPath, target); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w: %s", ErrNotWritable, dir)
		}
		return err
	}
	return nil
}
