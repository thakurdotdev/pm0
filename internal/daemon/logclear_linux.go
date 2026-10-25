//go:build linux

// Log clearing for the HTTP API: truncates an app's stdout/stderr sinks
// (including rotated copies) and empties its in-memory backlog. The
// on-disk behavior matches `pm0 flush` — append-mode children keep their
// fds and continue writing to the truncated inode.
package daemon

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pm0/pm0/internal/supervisor"
)

// appLogFiles returns every existing-or-expected log path for the views,
// deduplicated, including rotated siblings (path.<timestamp>). Missing
// files are harmless: truncateLogFiles skips them.
func appLogFiles(views []supervisor.AppView) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, v := range views {
		for _, path := range []string{v.Config.OutFile, v.Config.ErrorFile} {
			if path == "" {
				continue
			}
			add(path)
			for _, rotated := range rotatedLogSiblings(path) {
				add(rotated)
			}
		}
	}
	return out
}

// rotatedLogSiblings lists path.<suffix> files next to the live sink,
// mirroring logbus's copytruncate naming without relying on glob
// metacharacters in the configured path.
func rotatedLogSiblings(path string) []string {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := base + "."
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// truncateLogFiles empties each existing regular file and returns how
// many were truncated. Not-exist entries are skipped (the log may not
// have been created yet); any other error is returned for a truthful
// HTTP response.
func truncateLogFiles(paths []string) (int, error) {
	cleared := 0
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cleared, err
		}
		if !st.Mode().IsRegular() {
			continue
		}
		if err := os.Truncate(path, 0); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}
