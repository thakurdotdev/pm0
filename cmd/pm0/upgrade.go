package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pm0/pm0/internal/selfupdate"
	"github.com/pm0/pm0/pkg/client"
)

// cmdUpgrade implements `pm0 upgrade`: install the newest release (or
// --version) over this binary, then reload a running daemon in place.
// Distinct from `pm0 update`, which only re-execs the daemon with the
// binary already on disk (pm2 update parity; install.sh relies on it).
func cmdUpgrade(args []string) {
	fs := newFlagSet("upgrade")
	check := fs.Bool("check", false, "only report whether a newer release exists")
	want := fs.String("version", "", "install this release tag instead of the latest (e.g. v0.1.9); allows downgrades")
	force := fs.Bool("force", false, "reinstall even when already on that version")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	if runtime.GOOS != "linux" {
		fatalf("upgrade: releases are published for linux only")
	}

	src := selfupdate.NewSource()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tag := *want
	if tag == "" {
		latest, err := src.LatestTag(ctx)
		if err != nil {
			fatalf("upgrade: %v", err)
		}
		tag = latest
	} else if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	current, target := selfupdate.Normalize(version), selfupdate.Normalize(tag)
	cmp := selfupdate.Compare(target, current)

	if *check {
		if cmp > 0 {
			fmt.Printf("[pm0] update available: %s → %s (run `pm0 upgrade`)\n", current, target)
		} else {
			fmt.Printf("[pm0] pm0 %s is up to date (latest release: %s)\n", current, target)
		}
		return
	}
	if !*force {
		if cmp == 0 {
			fmt.Printf("[pm0] already on %s (use --force to reinstall)\n", current)
			return
		}
		if cmp < 0 && *want == "" {
			fmt.Printf("[pm0] installed %s is newer than the latest release %s; nothing to do\n", current, target)
			return
		}
	}

	exe, err := os.Executable()
	if err != nil {
		fatalf("upgrade: locate this binary: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, string(filepath.Separator)+"node_modules"+string(filepath.Separator)) {
		fatalf("upgrade: this pm0 was installed with npm (%s); upgrade it with: npm install -g pm0@latest", exe)
	}

	asset := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	fmt.Printf("[pm0] upgrading %s → %s (%s)\n", current, target, asset)
	sums, err := src.Download(ctx, tag, "checksums.txt")
	if err != nil {
		fatalf("upgrade: %v", err)
	}
	expected, err := selfupdate.ExpectedSum(sums, asset)
	if err != nil {
		fatalf("upgrade: %v", err)
	}
	bin, err := src.Download(ctx, tag, asset)
	if err != nil {
		fatalf("upgrade: %v", err)
	}
	if err := selfupdate.Verify(bin, expected); err != nil {
		fatalf("upgrade: %s: %v (nothing was changed)", asset, err)
	}
	fmt.Printf("[pm0] downloaded and verified SHA-256 %s…\n", expected[:16])
	if err := selfupdate.Replace(exe, bin, target); err != nil {
		if errors.Is(err, selfupdate.ErrNotWritable) {
			fatalf("upgrade: cannot write %s (%v); run it as the owner of that file, e.g. `sudo pm0 upgrade`", exe, err)
		}
		fatalf("upgrade: %v (nothing was changed)", err)
	}
	fmt.Printf("[pm0] installed pm0 %s at %s\n", target, exe)

	// Reload the daemon so it runs the new code too. No daemon: the next
	// command that starts one uses the new binary.
	c, err := client.Dial("")
	if err != nil {
		fmt.Println("[pm0] no running daemon; it will start on the new version")
		return
	}
	if _, perr := c.Ping(); perr != nil {
		_ = c.Close()
		fmt.Println("[pm0] no running daemon; it will start on the new version")
		return
	}
	got, err := reloadDaemonInPlace(c)
	_ = c.Close()
	if err != nil {
		fatalf("upgrade: binary installed, but the daemon reload failed: %v (run `pm0 update`)", err)
	}
	if selfupdate.Normalize(got) != target {
		fmt.Fprintf(os.Stderr, "[pm0] warning: the daemon reports %s, not %s: it runs from a different binary path. "+
			"Upgrade that binary (or restart the daemon from %s).\n", got, target, exe)
	}
}
