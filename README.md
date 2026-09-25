# pm0

[![CI](https://github.com/thakurdotdev/pm0/actions/workflows/ci.yml/badge.svg)](https://github.com/thakurdotdev/pm0/actions/workflows/ci.yml)
[![Release](https://github.com/thakurdotdev/pm0/actions/workflows/release.yml/badge.svg)](https://github.com/thakurdotdev/pm0/actions/workflows/release.yml)
[![GitHub Release](https://img.shields.io/github/v/release/thakurdotdev/pm0)](https://github.com/thakurdotdev/pm0/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**PM2-compatible process supervisor for Linux, written in Go.**  
Same workflow (`start`, `status`, `logs`, `scale`, `reload`, `ecosystem`), a fraction of the memory, and a CLI that stays fast at 500 apps.

```
$ pm0 start api.js -i max        # cluster mode, one port, N workers
$ pm0 status
name │ id │ mode    │ pid  │ uptime │ ↺ │ status │ cpu  │ mem
api  │ 0  │ cluster │ 7731 │ 12s    │ 0 │ online │ 0.0% │ 46.6mb
api  │ 1  │ cluster │ 7738 │ 12s    │ 0 │ online │ 0.0% │ 46.6mb
$ pm0 reload api                 # rolling zero-downtime reload
```

---

## Installation

Install via `curl` (automatically detects architecture and installs the binary):

```sh
curl -fsSL https://raw.githubusercontent.com/thakurdotdev/pm0/main/install.sh | bash
```

Or download the pre-compiled static binary directly:

```sh
# Linux amd64 (x86_64)
curl -fsSL https://github.com/thakurdotdev/pm0/releases/latest/download/pm0-linux-amd64 -o /usr/local/bin/pm0 && chmod +x /usr/local/bin/pm0

# Linux arm64 (aarch64)
curl -fsSL https://github.com/thakurdotdev/pm0/releases/latest/download/pm0-linux-arm64 -o /usr/local/bin/pm0 && chmod +x /usr/local/bin/pm0
```

> **Note**: For non-root installation, replace `/usr/local/bin/pm0` with `~/.local/bin/pm0` and ensure `~/.local/bin` is in your `$PATH`.

---

## Quick Start

### Starting Applications

```sh
pm0 start app.js                     # fork mode
pm0 start app.js -n web              # named process
pm0 start app.js -i 4                # cluster mode (4 workers, shared port)
pm0 start app.js -i max              # cluster mode (1 worker per CPU)
pm0 start server.py --interpreter python3  # any language or binary
pm0 start ecosystem.config.js        # start from ecosystem file
```

### Process Management

```sh
pm0 status                           # process list table
pm0 logs web --lines 100             # stream tail and live logs
pm0 describe web                     # details, env, uptime, memory
pm0 scale web +2                     # scale cluster group
pm0 reload web                       # zero-downtime rolling reload
pm0 restart web                      # restart process
pm0 stop web                         # stop process
pm0 delete web                       # delete process from list
```

### Persistence & Systemd

```sh
pm0 save                             # save running apps
pm0 resurrect                        # restore saved apps
pm0 startup                          # setup systemd boot service
```

---

## Ecosystem Config

pm0 supports PM2 `ecosystem.config.js` configurations:

```js
module.exports = {
  apps: [
    {
      name: "api",
      script: "./server.js",
      instances: "max",
      exec_mode: "cluster",
      env: {
        NODE_ENV: "development",
        PORT: 3000
      },
      env_production: {
        NODE_ENV: "production",
        PORT: 8080
      }
    }
  ]
};
```

Run with:
```sh
pm0 start ecosystem.config.js --env production
```

---

## Benchmarks: pm0 vs PM2

| Metric | PM2 7.0.4 | pm0 1.0.1 |
|---|---|---|
| `list` @ 500 apps | 245 ms | **38.5 ms** (6–10x faster) |
| Ecosystem start @ 500 apps / daemon RSS | 1.97 s / 139 MB | **0.31 s / 78 MB** |
| Daemon RSS at idle | 69 MB | **16 MB** (−77%) |
| Idle CPU @ 0 apps | 0.033% | **0.033%** (parity) |
| Cluster `-i 2` throughput / RSS | 25.8k req/s / 161 MB | **27.3k req/s / 135 MB** |
| 1,200 restart ops (p50 / total) | 238 ms / 290 s | **6.5 ms / 9.3 s** (37x faster) |
| Sustained 100 MB/s flood: daemon CPU | 20.2% | **5.7%** (3.6x less CPU) |
| Log rotation | External module needed | Built-in, 0 dropped lines |

---

## Requirements

- **OS**: Linux (uses cgroup v2 or pgid fallback)
- **Cluster Mode**: Node ≥ 23.2 (Linux kernel ≥ 3.9 `SO_REUSEPORT`)
- Fork mode supports any interpreter (Node.js, Python, Go, Rust, Ruby, shell, etc.)

---

## Building from Source

Requires Go ≥ 1.26:

```sh
git clone https://github.com/thakurdotdev/pm0.git
cd pm0
make build
make install    # installs to /usr/local/bin (PREFIX overridable)
```

---

## License

[MIT](LICENSE)
