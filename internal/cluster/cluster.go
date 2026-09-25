//go:build linux

// Package cluster implements M6 cluster mode: N daemon-supervised node
// children sharing one listening port via SO_REUSEPORT (kernel round-robin
// load balancing), plus the preload that makes it happen.
//
// Why a preload instead of PM2's design: PM2's God daemon is itself a Node
// cluster primary (God.nodeApp -> cluster.fork), so the N instances share
// the listening handle INSIDE the daemon process (verified live: `ss -tlnp`
// attributes the socket to "PM2 v7.0.4: God"). pm0's daemon is Go — it
// cannot hold a Node listening handle. Instead every instance is a plain
// `node --require <preload> app.js` child (exactly one process per
// instance, same process shape as PM2's workers) and the preload patches
// net.Server.prototype.listen to inject reusePort:true, so the N binds land
// in one kernel reuseport pool (Linux >= 3.9). Node >= 23.2 supports the
// listen option natively (verified on the Node shipped in this sandbox);
// older Nodes silently ignore the unknown option and the second instance
// EADDRINUSEs like fork mode — the preload detects that and warns.
package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PreloadName is the preload file inside PM0_HOME.
const PreloadName = "cluster-preload.js"

// MinNodeVersion is the first Node with net.Server.listen({reusePort})
// (nodejs/node "net: add reusePort option", v23.2.0).
const MinNodeVersion = "23.2.0"

// PreloadJS is injected with --require before the app loads, so the patch
// is in place before any require/import of net-derived modules binds.
//
// Scope: only TCP binds are patched (positional listen(port[...]) and the
// options-object form). Unix-socket paths (listen(path)) and pre-existing
// handles (listen({fd}) / listen(handle)) are left untouched — SO_REUSEPORT
// does not apply to them. dgram (UDP) is deliberately not patched: Node's
// UDP bind has no reusePort option and PM2's cluster mode never shared UDP
// handles across instances either.
const PreloadJS = `'use strict';
// pm0 cluster-mode preload: SO_REUSEPORT for TCP listeners.
// Cluster instances must all join the same kernel reuseport pool so the
// port is shared and connections round-robin between instances.
const net = require('net');

const NODE_MIN = '` + MinNodeVersion + `';
function nodeAtLeast(want) {
  const parts = String(process.versions.node || '0').split('-')[0].split('.');
  const w = want.split('.');
  for (let i = 0; i < 3; i++) {
    const a = parseInt(parts[i] || '0', 10), b = parseInt(w[i] || '0', 10);
    if (a !== b) return a > b;
  }
  return true;
}

if (!nodeAtLeast(NODE_MIN)) {
  process.stderr.write(
    '[pm0] cluster mode: node ' + process.versions.node + ' has no reusePort ' +
    'support (need >= ' + NODE_MIN + '); instances will not share the port\n');
} else {
  const origListen = net.Server.prototype.listen;
  net.Server.prototype.listen = function (...args) {
    if (args.length > 0 && args[0] !== null && typeof args[0] === 'object' &&
        !Array.isArray(args[0])) {
      if (args[0].reusePort === undefined && args[0].fd === undefined &&
          args[0].path === undefined) {
        args[0] = Object.assign({}, args[0], { reusePort: true });
      }
      return origListen.apply(this, args);
    }
    // Positional form listen([port[, host[, backlog]]][, callback]).
    // Only the numeric-port form is patched; string first args are unix
    // socket paths and must not carry the flag.
    if (args.length === 0 || typeof args[0] === 'number' || args[0] === undefined) {
      const opts = { reusePort: true };
      if (typeof args[0] === 'number') opts.port = args[0];
      const rest = args.slice(1);
      if (rest.length > 0 && typeof rest[0] === 'string') {
        opts.host = rest.shift();
      } else if (rest.length > 0 && typeof rest[0] === 'object' && rest[0] !== null) {
        Object.assign(opts, rest.shift()); // {host} object form
      }
      return origListen.apply(this, [opts, ...rest]);
    }
    return origListen.apply(this, args);
  };
}
`

// WritePreload writes the preload into home (PM0_HOME) and returns its
// absolute path. Rewritten on every daemon boot so a binary upgrade picks
// up the current preload without user action.
func WritePreload(home string) (string, error) {
	if home == "" {
		return "", fmt.Errorf("cluster: empty home")
	}
	path := filepath.Join(home, PreloadName)
	if err := os.WriteFile(path, []byte(PreloadJS), 0o644); err != nil {
		return "", fmt.Errorf("cluster: write preload: %w", err)
	}
	return path, nil
}

// versionCache memoizes node --version probes by binary string: cluster
// starts must not pay a process spawn per instance (ecosystem @500 apps).
var versionCache sync.Map // string -> string (normalized "x.y.z")

// CheckNodeVersion fails fast when the resolved node binary is older than
// MinNodeVersion (no reusePort support: the second instance would
// EADDRINUSE after the first one already bound). Callers surface this as
// InvalidArgument before registering anything.
func CheckNodeVersion(bin string) error {
	if bin == "" {
		bin = "node"
	}
	if v, ok := versionCache.Load(bin); ok {
		return checkVersionString(v.(string), bin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("cluster: exec_mode cluster requires node >= %s: cannot probe %q (--version): %v",
			MinNodeVersion, bin, err)
	}
	ver := strings.TrimSpace(string(out))
	versionCache.Store(bin, ver)
	return checkVersionString(ver, bin)
}

func checkVersionString(ver, bin string) error {
	maj, min, patch, err := parseVersion(ver)
	if err != nil {
		return fmt.Errorf("cluster: exec_mode cluster requires node >= %s: cannot parse version %q from %q",
			MinNodeVersion, ver, bin)
	}
	wmaj, wmin, wpatch, _ := parseVersion(MinNodeVersion)
	if compareVersion(maj, min, patch, wmaj, wmin, wpatch) < 0 {
		return fmt.Errorf("cluster: exec_mode cluster requires node >= %s (SO_REUSEPORT reusePort support), got %s from %q",
			MinNodeVersion, ver, bin)
	}
	return nil
}

func parseVersion(s string) (int, int, int, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	// Strip pre-release/build metadata ("23.2.0-nightly...").
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return 0, 0, 0, fmt.Errorf("bad version %q", s)
	}
	nums := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return 0, 0, 0, fmt.Errorf("bad version %q", s)
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], nil
}

func compareVersion(aMaj, aMin, aPatch, bMaj, bMin, bPatch int) int {
	if aMaj != bMaj {
		if aMaj < bMaj {
			return -1
		}
		return 1
	}
	if aMin != bMin {
		if aMin < bMin {
			return -1
		}
		return 1
	}
	if aPatch != bPatch {
		if aPatch < bPatch {
			return -1
		}
		return 1
	}
	return 0
}
