# pm0

**PM2-compatible process supervisor for Linux, written in Go.** Same workflow and
wire surface as PM2 (`start / status / logs / scale / reload / jlist / ecosystem files`),
a fraction of the memory, and a CLI that stays fast at 500 apps. All planned milestones
are complete: fork mode, cluster mode, logs with rotation, ecosystem config, persistence,
startup units, ops triggers, and the performance/reliability hardening pass.

Built around one rule first: **reliability** — never orphan a child, never lose state,
never block an app.

```
$ pm0 start api.js -i max        # cluster mode, one port, N workers
$ pm0 status
name │ id │ mode    │ pid  │ uptime │ ↺ │ status │ cpu  │ mem
api  │ 0  │ cluster │ 7731 │ 12s    │ 0 │ online │ 0.0% │ 46.6mb
api  │ 1  │ cluster │ 7738 │ 12s    │ 0 │ online │ 0.0% │ 46.6mb
$ pm0 reload api                 # rolling, zero-downtime
```

## Install

pm0 is a single static Go binary (Linux only — it uses cgroup v2 and `/proc`).
Four ways to install it:

**npm** (the PM2 way — the npm package bundles the Go source and builds the binary in
`postinstall`, or ships prebuilds in release packages):

```sh
npm install -g pm0
# newer npm may gate install scripts; allow them explicitly:
npm install -g --allow-scripts=pm0 pm0
```

> The postinstall step needs Go ≥ 1.26 on PATH when no prebuild for your platform is
> bundled. To install without Go, use a release tarball instead.

**Release tarball** (static binaries, no toolchain needed):

```sh
curl -fsSL https://github.com/pm0/pm0/releases/latest/download/pm0-linux-amd64.tar.gz | tar -xz
install -m 0755 pm0-linux-amd64 /usr/local/bin/pm0
```

**Quick-install script** (tries the release download, falls back to a source build):

```sh
curl -fsSL https://raw.githubusercontent.com/pm0/pm0/main/install.sh | sh
```

**From source** (Go ≥ 1.26):

```sh
git clone https://github.com/pm0/pm0 && cd pm0
make install              # builds bin/pm0, installs to /usr/local/bin (PREFIX overridable)
# release artifacts for every supported arch:
make release              # -> dist/pm0-<version>-linux-{amd64,arm64,arm,386}.tar.gz
# npm tarball with a staged prebuild:
make npm-pack             # -> dist/pm0-<version>.tgz  (npm i -g dist/pm0-<version>.tgz)
```

Pin a build yourself at any commit:

```sh
go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)" -o pm0 ./cmd/pm0
```

The `pm0` executable self-reports what it was built from: `pm0 version`.

## Quick start

```sh
pm0 start app.js                     # fork mode
pm0 start app.js -n web              # named (pm2 shorthand; --name also works)
pm0 start app.js -i 4                # cluster mode: 4 workers, one shared port
pm0 start app.js -i max              # one worker per CPU
pm0 start python.sh --interpreter none   # any language, not just node
pm0 start ecosystem.config.js --env production --only web,api

pm0 status | list                    # process table
pm0 jlist                            # PM2-shaped JSON (scripts parse it)
pm0 describe web                     # full detail incl. env
pm0 logs web --lines 100             # streamed + backlog
pm0 scale web +2                     # grow/shrink a cluster group
pm0 reload web                       # rolling restart (0-downtime for clusters)
pm0 restart web | stop web | delete web
pm0 save && pm0 resurrect            # persist + restore the app set (boot unit uses this)
pm0 startup                          # install the systemd boot unit
pm0 update                           # re-exec the daemon in place, apps survive
pm0 doctor                           # kill-path / cgroup diagnostics
```

## Status

| Milestone | Scope | State |
|---|---|---|
| M0 | platform spike: Launcher, cgroup v2 kill paths, pgid fallback, subreaper+reaper, fake-child | **done** (fec724f) |
| M1 | supervisor core: state machine, actor mailbox, backoff, restart policy | **done** |
| M2 | daemon + CLI + compat contract (golden suite vs real pm2) | **done** |
| M3 | logs: rotation, tail, bounded ring buffer, drop counters | **done** |
| M4 | config (goja sandbox) + persistence + env + startup unit | **done** |
| M5 | ops: max_memory_restart, cron_restart, watch, health checks, wait_ready | **done** |
| M5.1 | perf & reliability queue: O(1)-pass list, lazy ops tick, log-pump idle backoff, stale-socket takeover, flood RSS | **done** |
| M6 | cluster mode (SO_REUSEPORT pools, scale, rolling reload) | **done** (714ee94) |
| — | rename to `pm0` + npm/release packaging | **done** (f15e854) |
| 1.0.1 | profiled hardening pass: reaper idle backoff, flood-path optimization (3.6–6.5x CPU), counted batch cap, pprof endpoint | **done** (88e3610) |
| M7 (optional) | extras: web dashboard, shell completions | not started — future work |

## Control plane (M2)

`pm0 start script.js --name web` boots a detached daemon on first use
(gRPC over `~/.pm0/pm0.sock`, 0600) and manages apps through it:
`stop / restart / reload / scale / delete`, `list / jlist / describe /
logs`, `save / resurrect / update / kill`. `jlist` renders the exact PM2
wire shape (docs/compat.md §2) — the golden suite (`test/golden/`) diffs
scripted scenarios against committed fixtures, and
`scripts/golden-pm2-check.js` asserts the same semantics against a real
pm2 daemon. Daemon `update` re-execs in place: children survive and the
new image re-adopts every tree by its attribution marker (same mechanism
resurrect uses after a crash). Child stdout/stderr land in
`~/.pm0/logs/<name>-out.log` / `-error.log` (append) from M2 on.

## Log router (M3)

Every app's sinks are tailed into a memory-bounded ring
(`internal/logbus`: 1000 lines AND 1 MiB per app, 8 KiB per-line
truncation in the ring only — the files keep full history) and new lines
push live to `logs`/StreamLogs subscribers. Nothing is lost silently
(invariant I8): ring evictions and slow-consumer drops are counted, and
a fallen-behind `logs` consumer gets an inline
`[pm0: N <stream> lines dropped]` marker. Built-in size-based rotation
(divergence 12; pm2 needs the pm2-logrotate module for this): 10 MB per
sink, copytruncate so children never cooperate, timestamped rotated
files, 30 retained (configurable per app since M4). The ring spans app
restarts; `delete` keeps the files, pm2 parity.

The M2 pin corrected five contract rows against observed pm2 source and
experiments (pm2 5.4.2 / 7.0.4): monotonic pm_id allocation with
reset-on-empty, the two-condition unstable window, errored resetting
`unstable_restarts` to 0 and nulling `created_at`, the two-word
`waiting restart` status string, and `exp_backoff_restart_delay` as a
number with the x1.5 capped curve. See docs/compat.md changelog.

## Config files, env and startup (M4)

`pm0 start ecosystem.config.js` batch-starts PM2-shaped ecosystem
files: `apps: [...]` arrays or a single app object, `env_<name>` maps
selected with `--env production`, `--only web,api` filtering, relative
paths resolved against the file's directory, and aliases
(`exec_interpreter`, `node_args`). JS bodies run in a **sandboxed** goja
VM: only `module.exports` exists — no `require`, no `process`, no I/O —
and evaluation is time-boxed, so a `while(1){}` config fails the start
instead of hanging the CLI. Unknown keys are hard errors listing the
supported set (divergence 2). `pm0 env <name|id>` prints an app's full
child environment (redacted under `--redact-env`).

Rotation knobs (divergence 12): `--log-rotate-max 4KB|off` /
`--log-rotate-retain N` on `start`, or `log_rotate_max_bytes` /
`log_rotate_retain` per ecosystem app — thresholds, retention, and a
complete off switch, persisted in `dump.json`.

`pm0 startup` installs (as root) or prints (`--print`) the systemd
unit `pm0-<user>.service`: `ExecStart=pm0 resurrect` on boot,
`ExecStop=pm0 kill` on shutdown, `PM0_HOME` pinned to the effective
home. `pm0 unstartup` removes it. Only systemd in v1.

## Ops: restart triggers (M5)

One daemon-side ops layer (`internal/ops`) enforces four restart triggers
through the same restart path as `pm0 restart` (manual restart
semantics — `restart_time` counts them, crash budget resets):

- **max_memory_restart** (row 15): when the process tree's RSS (the same
  number `monit.memory` reports) exceeds the limit, the app restarts.
  Checked on pm2's monitoring cadence (30s) while the app is online.
- **cron_restart** (row 20): a vixie-cron expression (5 fields, ranges,
  steps, lists, names, `@daily` aliases; UTC, minute resolution) fires a
  restart when the clock enters a matching minute. An invalid expression
  fails the start loudly (divergence 2).
- **watch** (rows 21/22): fsnotify on the app's cwd, recursive, ignoring
  `node_modules`/`.git` subtrees and `*.swp`/`*.tmp` churn; editor save
  storms debounce into one restart (500ms quiet window). Online and
  errored apps restart; deliberately stopped apps are never resurrected
  by file churn.
- **health checks** (divergence 7, pm0 extension): `health_check_cmd`
  runs through `sh -c` every `health_check_interval` (default 30s) while
  the app is online; `health_check_retries` consecutive failures
  (default 3) restart it, any success resets the count. Off by default;
  ecosystem keys or `--health-check-*` start flags.

**wait_ready** (row 19) lives in the supervision core: a wait_ready app
holds `launching` until its child sends the node-IPC string `ready`
(`process.send('ready')`) or `listen_timeout` (row 18, default 3000ms)
elapses — observed pm2 forces online at the deadline. Every fork child
gets a real IPC channel (`NODE_CHANNEL_FD=3`, node wire format), so node
apps can `process.send` even without wait_ready. A crash during the
ready wait drives the normal restart policy.

When the kernel OOM-kills a tree, `describe` reports `exit reason: oom`
(SIGKILL wait status + a cgroup `memory.events` `oom_kill` counter
advance — divergence 6, additive).

## Performance & reliability hardening (M5.1)

**1.0.1 pass** (after the release hardening re-profiling, details in
docs/benchmarks5.md):

- **Idle CPU to parity**: the reaper's zero-children path polled
  `wait4(WNOHANG)` every 25 ms (40 timer allocations + 40 syscalls/s for the
  daemon's whole life). It now parks on one reusable timer with a 25 ms →
  800 ms adaptive backoff, reset instantly by the Register kick — spawn-to-reap
  latency unchanged, idle CPU 0.35 % → 0.033 % (= PM2).
- **Flood path 3.6–6.5x less daemon CPU**: profiled with the new opt-in
  `PM0_PPROF_ADDR` pprof endpoint, the log pump got a reusable read buffer,
  per-batch timestamps, a memchr-based split, consecutive-duplicate string
  sharing, batch-aggregated lock-free drop accounting and a no-subscriber
  fanout skip. 100 MB/s flood: 29.3 % → 5.7 % average daemon CPU (PM2: 20.2 %).
- **Counted batch cap (I8)**: a pump tick can read at most 64 MiB per pass;
  beyond that the head is skipped and COUNTED (rotation off + tick-starved
  daemon can no longer balloon the heap for data the ring could not hold
  anyway). Files remain the full-history authority.
- **Snapshot cache retuned**: shared `/proc` pass min-age 200 ms → 1 s (monit
  cadence); the wait-online `jlist` storm at 500 apps pays 3–4x fewer full
  passes. Registry state (pids/statuses) was never sourced from this cache.

**M5.1 pass** (the original perf & reliability queue, all re-verified since):

The 14-scenario benchmark suite (docs/benchmarks2.md) surfaced four gaps
and one bug; all five are fixed and re-measured against the same PM2
7.0.4 reference:

- **List scales flat.** One shared `/proc` pass (one stat read per pid,
  200ms min-age) attributes every app tree for `list`/`jlist`/`describe`
  — `list` at 500 apps went from 8.57s to **42ms** (PM2: 232ms), and the
  all-online poll from 8.3s to 76ms.
- **Idle CPU ≈ 0 at scale.** The ops reconciler syncs only when the
  registry mutates; log pumps stat-gate and idle back off (100ms → 1s,
  snapping back on data or a new `logs` subscriber); the reaper parks in
  a blocking `wait4` instead of a 2ms spin. Idle CPU at 500 apps:
  15.1% → **1.6%**.
- **Crash recovery is fast.** The daemon records `<pid> <starttime>` in
  `~/.pm0/pm0.pid` before binding its socket; a boot whose
  predecessor is provably dead takes over in ~0.4s (was 2s) —
  daemon-crash respawn 2.02s → 0.41s, reboot resurrect 2.10s → 0.44s
  (PM2: 0.54s). A live daemon is never disturbed.
- **Floods leave no residue.** Ring line-splitting preallocates exactly:
  flood-time daemon RSS at 100 MB/s dropped 55–59MB → **17–18MB**, and
  flood CPU fell ~15% (29.5% avg vs PM2's 21.3% at the same rate). A
  growth-triggered janitor returns post-flood heap to the OS.
- **pgid-mode monit double-count fixed.** The tree leader used to match
  both the pgrp scan and the orphan sweep, doubling `monit.memory`/CPU
  and firing `max_memory_restart` at half the limit; the orphan sweep
  now excludes pgrp members (kill coverage unchanged).

## Cluster mode (M6)

`pm0 start app.js -i 4` (or `-i max`, `--exec-mode cluster`,
`instances`/`exec_mode` in an ecosystem file) runs N same-named
instances that **share one TCP port**: the cluster preload
(`internal/cluster`, written to `PM0_HOME` at daemon boot) patches
`net.Server.listen` to inject `reusePort: true`, so the N binds form
one kernel SO_REUSEPORT pool and connections round-robin between
instances. The observable contract matches PM2: N same-named `ls` rows
with `NODE_APP_INSTANCE` 0..N-1, per-instance log files
(`<name>-out-<i>.log`), per-instance pm_ids and restart budgets.

- **`pm0 scale web +2` / `-1` / `4`** grows and shrinks the group,
  keeping survivor instances untouched (highest `NODE_APP_INSTANCE`
  leaves first).
- **`pm0 reload web`** (and `gracefulReload`) rolls one instance at a
  time: the replacement is spawned at the same pm_id and joins the port
  pool immediately, the old worker gets PM2's `'shutdown'` IPC message
  (then `kill_signal` → `kill_timeout` → SIGKILL), and a replacement
  that fails to stabilize within `listen_timeout` is removed and the
  old incarnation **restored** (PM2 `unparkOldWorker`) — a failed
  reload never leaves the app down. Verified end-to-end: zero refused
  connections during the roll.
- **Divergences** (docs/compat.md #17): balancing is the kernel's
  reuseport distribution rather than Node's cluster round-robin; Linux
  ≥ 3.9 and Node ≥ 23.2 required (older Nodes keep fork semantics —
  loud EADDRINUSE); port-0 apps get per-instance ports; apps see a
  fork-style process context.

## Benchmarks: PM2 7.0.4 vs pm0 1.0.1

Five suites, same harness, full 12-scenario operational matrix — the current
release run is **docs/benchmarks5.md** (raw data `bench/results5.json`, chart
`docs/benchmarks5.png`), a fresh both-tools re-run after the 1.0.1 hardening
pass. Earlier suites: `docs/benchmarks.md` (footprint/latency),
`docs/benchmarks2.md` (the 14-row matrix), `docs/benchmarks3.md` (post-M5.1),
`docs/benchmarks4.md` (1.0.0 + cluster). Highlights of the current matrix
(2 vCPU / 3.9 GB sandbox):

| Row | PM2 7.0.4 | pm0 1.0.1 |
|---|---|---|
| `list` @ 500 apps | 245 ms | **38.5 ms** (5.5–10.8x faster at every N) |
| Ecosystem start @ 500 apps / daemon RSS | 1.97 s / 139 MB | **0.31 s / 78 MB** |
| Daemon RSS at idle | 69 MB | **16 MB** (−77 %) |
| Idle CPU @ 0 apps | 0.033 % | **0.033 % (parity)** |
| Cluster `-i 2` throughput / children RSS | 25.8k req/s / 161 MB | **27.3k req/s / 135 MB** |
| 1 200 restart ops (p50 / total) | 238 ms / 290 s | **6.5 ms / 9.3 s** (37x / 31x) |
| Sustained 100 MB/s flood: daemon CPU | 20.2 % | **5.7 % (3.6x less)** |
| Log-rotation daemon CPU (300k lines) | 26.0 % | **4.0 % (6.5x less)** |
| Daemon-crash recovery | duplicates +5 processes | **adopts, 0 duplicates** |
| Orphan sweep after `kill` | 3 leftover | **0 leftover** |
| Crash-loop backoff curve | ×1.5 → 15 s cap | identical, restart-for-restart |
| Log rotation | module needed | builtin, 0 lines lost |

Documented residuals (honest): idle CPU at scale 0.55 / 0.8 / 1.6 % of one core
at 100 / 250 / 500 apps vs PM2's ~0 % — a steady-state profile measures 0.05 %
(the harness number is GC settling after the wait-online `jlist` storm, plus
two `statx` per app-second from the log pumps); `delete all` @ 500 within the
parity band (PM2 +12 % this session); fork throughput within sandbox variance
(PM2 +1 %).

## Requirements

- Linux (cgroup v2 or pgid kill paths — `pm0 doctor` reports the active mode);
  kernel ≥ 5.2 for freeze mode, ≥ 5.14 for atomic `cgroup.kill`
- Node ≥ 23.2 for cluster mode (`-i N`); any interpreter otherwise
- Building: Go ≥ 1.26; the npm install path needs Go only when no prebuild is bundled

## Development

```sh
make build      # build ./cmd/pm0 -> bin/pm0
make fakechild  # build test/fakechild -> bin/fakechild
make test       # go test ./...
make race       # go test -race ./...
make lint       # go vet (+ staticcheck if installed)
make proto      # regenerate api/v1 stubs (buf + protoc-gen-go/-go-grpc)
make install    # build + install to PREFIX/bin (default /usr/local)
make release    # cross-compiled dist/ tarballs: linux amd64/arm64/arm/386
make npm-pack   # stage the prebuild + produce the npm tarball in dist/
```

The cgroup-dependent tests auto-skip when no delegated cgroup v2 subtree is
available (e.g. cgroup v1 hosts, unprivileged containers). They run in the
privileged CI job. The pgid fallback is always exercised.

## Layout

- `cmd/pm0` — CLI entry point · `internal/daemon` — gRPC daemon ·
  `internal/supervisor` / `internal/machine` — supervision core ·
  `internal/proc` — launch/kill paths, `/proc` scanning, reaper ·
  `internal/cluster` — M6 cluster preload · `internal/logbus` — log router ·
  `internal/ops` — restart triggers · `internal/config` — ecosystem sandbox ·
  `pkg/client` — Go SDK
- `docs/compat.md` — the PM2 compatibility contract: defaults, `jlist` schema,
  status vocabulary, known divergences, changelog
- `bench/` — benchmark harnesses and raw results · `test/golden/` — scenario suite
