# pm0

[![CI](https://github.com/thakurdotdev/pm0/actions/workflows/ci.yml/badge.svg)](https://github.com/thakurdotdev/pm0/actions/workflows/ci.yml)
[![Release](https://github.com/thakurdotdev/pm0/actions/workflows/release.yml/badge.svg)](https://github.com/thakurdotdev/pm0/actions/workflows/release.yml)
[![GitHub Release](https://img.shields.io/github/v/release/thakurdotdev/pm0)](https://github.com/thakurdotdev/pm0/releases)
[![Website](https://img.shields.io/badge/website-pm0.thakur.dev-blue)](https://pm0.thakur.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A fast, lightweight, PM2-compatible process supervisor written in Go. Same workflow and commands as PM2 (`start`, `status`, `logs`, `scale`, `reload`, `ecosystem`), a fraction of the memory footprint, and native support for any runtime (Node.js, Bun, Deno, Python, Go, Rust, and shell scripts).

Documentation & Benchmarks: [pm0.thakur.dev](https://pm0.thakur.dev)

```text
$ pm0 start api.js -i max        # cluster mode, one port, N workers
$ pm0 status
name │ id │ mode    │ pid  │ uptime │ ↺ │ status │ cpu  │ mem
api  │ 0  │ cluster │ 7731 │ 12s    │ 0 │ online │ 0.0% │ 46.6mb
api  │ 1  │ cluster │ 7738 │ 12s    │ 0 │ online │ 0.0% │ 46.6mb
$ pm0 reload api                 # rolling zero-downtime reload
```

---

## Features

- **Low Memory Footprint**: ~16 MB idle daemon RSS (77% less than PM2).
- **Fast CLI**: Sub-40ms status table response at scale.
- **Standalone Daemon**: Single static Go binary; does not require Node.js for the supervisor daemon.
- **Multi-Runtime Support**: Native execution for Node, Bun, Deno, Python, and compiled binaries.
- **Process Reliability**: Built on Linux cgroup v2 kill paths to guarantee zero orphaned child processes.
- **Zero-Downtime Cluster Mode (Node.js only)**: Kernel `SO_REUSEPORT` socket pooling for rolling updates without dropped connections (Node ≥ 23.2; other runtimes stay fork-mode).
- **Built-in Log Rotation**: Memory-bounded ring buffers with automatic size-based rotation.

---

## Installation

### Automated Install via curl (Recommended)

Detects architecture (`amd64`, `arm64`, `arm`, `386`) and installs the binary:

```sh
# User install (installs to ~/.local/bin)
curl -fsSL https://pm0.thakur.dev/install.sh | bash

# System-wide install (installs to /usr/local/bin)
curl -fsSL https://pm0.thakur.dev/install.sh | sudo bash
```

> If installing without `sudo`, ensure `~/.local/bin` is in your `$PATH` (e.g., `export PATH="$HOME/.local/bin:$PATH"` in `~/.bashrc`).

### Direct Binary Download

Download the pre-compiled static binary directly:

```sh
# Linux amd64 (x86_64)
sudo curl -fsSL https://github.com/thakurdotdev/pm0/releases/latest/download/pm0-linux-amd64 -o /usr/local/bin/pm0 && sudo chmod +x /usr/local/bin/pm0

# Linux arm64 (aarch64)
sudo curl -fsSL https://github.com/thakurdotdev/pm0/releases/latest/download/pm0-linux-arm64 -o /usr/local/bin/pm0 && sudo chmod +x /usr/local/bin/pm0
```

---

## Quick Start

### Starting Applications

```sh
# Node.js
pm0 start server.js --name web               # Single instance (fork mode)
pm0 start server.js -i max                   # Cluster mode (1 worker per CPU, shared port)

# Bun
pm0 start server.ts --interpreter bun --name bun-api

# Deno
pm0 start server.ts --interpreter deno --name deno-api

# Python
pm0 start app.py --name python-app           # Auto-detects python3

# Compiled Binaries (Go, Rust, C++)
pm0 start ./my-binary --interpreter none --name go-service

# Shell Scripts
pm0 start deploy.sh --name worker            # Auto-detects sh/bash
```

### Process Management

```sh
pm0 status                           # View process status table
pm0 logs web --lines 100             # Stream logs with backlog
pm0 describe web                     # Detailed app metadata, uptime, and memory
pm0 scale web +2                     # Scale cluster workers up (+2) or down (-1)
pm0 reload web                       # Rolling zero-downtime reload
pm0 restart web                      # Hard restart
pm0 stop web                         # Stop process
pm0 delete web                       # Remove process from supervision
```

### Persistence and Systemd

```sh
pm0 save                             # Save running process snapshot
pm0 resurrect                        # Restore saved processes
pm0 startup                          # Install systemd service for auto-boot
```

---

## HTTP API (opt-in)

pm0 can expose a local JSON/REST control plane for dashboards and co-located
backend services — list processes with full PM2-compatible data, and act on
them (start, stop, restart, reload, delete, scale, stream logs over SSE).

Off by default. Enable it with `PM0_HTTP_ADDR` (`host:port` or `unix:/path`),
with `pm0 daemon --http-addr`, or by answering "yes" to the installer prompt
(which writes `$PM0_HOME/config`):

```sh
PM0_HTTP_ADDR=127.0.0.1:9615 pm0 daemon

curl -s http://127.0.0.1:9615/api/v1/processes
curl -s -X POST http://127.0.0.1:9615/api/v1/processes/web/restart
```

It has no authentication of its own and is meant to be bound to loopback (or a
Unix socket) only — your own backend terminates auth and re-exposes it. Full
contract: [`docs/http-api.md`](docs/http-api.md).

---

## Common Flags

| Flag | Shorthand | Description | Example |
|---|---|---|---|
| `--name` | `-n` | Assign an application name | `--name api` |
| `--interpreter` | | Specify runtime binary (`bun`, `deno`, `none`) | `--interpreter bun` |
| `--instances` | `-i` | Number of instances (`N` or `max`; Node.js apps share one port via cluster mode, other runtimes run as fork families with no shared port) | `-i max` |
| `--watch` | | Auto-restart on file changes in current directory | `--watch` |
| `--max-memory-restart` | | Auto-restart if memory exceeds limit | `--max-memory-restart 500M` |
| `--env` | | Inject environment variable | `--env PORT=3000` |
| `--parallel` | `-p` | `stop`/`restart`/`delete`: concurrent per-app RPCs. Default `1` = sequential (staggered downtime, ordered ids). `reload` stays sequential by design (zero-downtime + rollback). | `pm0 restart all --parallel 4` |
| `--` | | Pass arguments directly to your script | `-- --port 8080` |

Parallel restarts trade staggered downtime for speed: `pm0 restart all`
restarts one app at a time (each port is empty for ~100ms in turn), while
`pm0 restart all --parallel 4` restarts up to 4 at once (faster wall-clock,
but all selected ports go down together and N runtimes booting at once
spikes CPU/RSS on small boxes). Per-app semantics are unchanged either way
(`SIGINT` -> `kill_timeout` -> `SIGKILL` through the actor mailbox).

---

## Ecosystem File

`pm0` supports PM2-compatible `ecosystem.config.js` configurations:

```js
module.exports = {
  apps: [
    {
      name: "api",
      script: "./server.ts",
      interpreter: "bun",
      instances: 2,
      env: {
        PORT: 3000
      }
    }
  ]
};
```

Start the ecosystem config:
```sh
pm0 start ecosystem.config.js
```

---

## Benchmarks: pm0 vs PM2

Tested under identical workloads (2 vCPU / 3.9 GB Linux environment):

| Metric | PM2 7.0.4 | pm0 1.0.1 | Difference |
|---|---|---|---|
| `list` @ 500 apps | 245 ms | **38.5 ms** | **6.4x faster** |
| Ecosystem start @ 500 apps | 1.97 s / 139 MB | **0.31 s / 78 MB** | **6.3x faster, -44% RSS** |
| Daemon RSS (idle) | 69 MB | **16 MB** | **-77% memory** |
| Idle CPU @ 0 apps | 0.033% | **0.033%** | **Parity** |
| Cluster `-i 2` throughput | 25.8k req/s | **27.3k req/s** | **Higher throughput** |
| 1,200 restart ops | 238 ms / 290 s | **6.5 ms / 9.3 s** | **37x faster** |
| Sustained 100 MB/s log flood | 20.2% CPU | **5.7% CPU** | **3.6x less CPU** |
| Log rotation | Module required | **Built-in** | **Size-based copytruncate** |

---

## Requirements

- **Operating System**: Linux (uses cgroup v2 or pgid process tracking)
- **Cluster Mode**: Node ≥ 23.2 (kernel ≥ 3.9 `SO_REUSEPORT`)
- **Fork Mode**: Supports any runtime or executable (Bun, Node, Python, Go, Rust, shell)

---

## Building from Source

Requires Go ≥ 1.26:

```sh
git clone https://github.com/thakurdotdev/pm0.git
cd pm0
make build
sudo make install    # Installs to /usr/local/bin (PREFIX overridable)
```

---

## License

[MIT](LICENSE)
