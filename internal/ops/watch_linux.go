//go:build linux

// fsnotify-based recursive directory watcher for the watch feature
// (compat.md rows 21/22): watches the app cwd tree, ignoring node_modules
// and .git subtrees entirely (never walked, never watched) plus *.swp /
// *.tmp file churn (pm0 superset of pm2's default ignore list), and
// fires once per quiet window (debounce) so editor save storms produce
// one restart instead of a burst.
package ops

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watcher owns the fsnotify watcher and its event loop.
type watcher struct {
	fsw  *fsnotify.Watcher
	stop chan struct{}
	done chan struct{}
}

// watchIgnore reports whether a path must be ignored: any path component
// named node_modules or .git, or a file ending in .swp / .tmp (row 22
// pinned superset: pm2 ignores node_modules and .git; pm0 adds the
// editor/scratch suffixes without removing pm2's defaults).
func watchIgnore(path string) bool {
	for _, comp := range strings.Split(filepath.ToSlash(path), "/") {
		if comp == "node_modules" || comp == ".git" {
			return true
		}
	}
	base := filepath.Base(path)
	return strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".tmp")
}

// startWatcher adds every directory under root (excluding ignored
// subtrees) and starts the event loop. onFire runs after each debounce
// window that saw at least one relevant event; it must not block long.
func startWatcher(root string, debounce time.Duration, onFire func(), logf func(string, ...any)) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &watcher{fsw: fsw, stop: make(chan struct{}), done: make(chan struct{})}
	if err := w.addTree(root); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	go w.loop(root, debounce, onFire, logf)
	return w, nil
}

// addTree recursively adds directories. The root must exist (error);
// per-directory errors deeper in the tree are tolerated (permission
// denied on one subdir must not kill the whole watch) except ENOENT
// mid-walk races, which are fine too.
func (w *watcher) addTree(root string) error {
	added := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
				return nil // unreadable/vanished subtree: watch what we can
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && watchIgnore(path) {
			return filepath.SkipDir
		}
		if err := w.fsw.Add(path); err == nil {
			added++
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if added == 0 {
		return errors.New("no watchable directories under " + root)
	}
	return nil
}

// loop consumes fsnotify events until stopped. Relevant events arm (or
// re-arm) a debounce timer; only the quiet-window expiry fires the
// callback. New directories created under the tree are watched too.
func (w *watcher) loop(root string, debounce time.Duration, onFire func(), logf func(string, ...any)) {
	defer close(w.done)
	defer w.fsw.Close()

	var timer *time.Timer
	var fireC <-chan time.Time
	for {
		select {
		case <-w.stop:
			if timer != nil {
				timer.Stop()
			}
			return
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			logf("ops: watch: %v", err)
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if watchIgnore(ev.Name) {
				continue
			}
			// chokidar-style recursion: directories created inside the
			// watched tree join the watch set (their contents count too).
			if ev.Has(fsnotify.Create) {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() {
					if err := w.addTree(ev.Name); err != nil {
						logf("ops: watch: add %s: %v", ev.Name, err)
					}
				}
			}
			if ev.Has(fsnotify.Create) || ev.Has(fsnotify.Write) ||
				ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				// Chmod-only churn (permissions, atime fiddling) never fires.
				if timer == nil {
					timer = time.NewTimer(debounce)
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(debounce)
				}
				fireC = timer.C
			}
		case <-fireC:
			fireC = nil
			timer = nil
			onFire()
		}
	}
}

// close stops the loop and releases the watcher (idempotent).
func (w *watcher) close() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
	}
}
