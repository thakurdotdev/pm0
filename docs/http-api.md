# pm0 HTTP API

An **opt-in**, locally-bound HTTP/REST control plane for the pm0 daemon. It
exposes the same capabilities as the CLI and the gRPC SDK — list processes and
their full data, and act on them (start, stop, restart, reload, delete, scale,
stream logs) — as plain JSON over HTTP so a dashboard backend can consume it.

> This is a transport adapter over the existing gRPC `pm0.v1.Daemon` service.
> Every handler calls the same server methods the CLI uses, so process-control
> semantics (selectors, name families, rolling reload, kill paths, log
> buffering) are identical. There is no second implementation to drift.

---

## 1. Quick start

The API is **off by default** — pm0 opens no HTTP port unless you ask it to.
Enable it with the `PM0_HTTP_ADDR` environment variable (or `pm0 daemon
--http-addr` for a foreground run):

```sh
# restart the daemon so it picks up the variable
pm0 kill
PM0_HTTP_ADDR=127.0.0.1:9615 pm0 daemon

# from another shell
curl -s http://127.0.0.1:9615/api/v1/health
curl -s http://127.0.0.1:9615/api/v1/processes
```

`PM0_HTTP_ADDR` accepts:

- a TCP address — `127.0.0.1:9615` (loopback recommended)
- a local Unix socket — `unix:/run/pm0-http.sock` (created `0600`)
- an explicit disable — `off` (also `none`, `disabled`, `false`, `0`)

### Persisting the setting: `$PM0_HOME/config`

The daemon is long-lived and usually auto-spawned (or run by systemd), so an
inline env var only affects the daemon that reads it. To make the choice stick
without editing a shell profile, put it in pm0's config file:

```sh
# ~/.pm0/config  (or $PM0_HOME/config)
PM0_HTTP_ADDR=127.0.0.1:9615
```

Resolution order: the `PM0_HTTP_ADDR` **environment variable wins**, then the
`PM0_HTTP_ADDR` line in `$PM0_HOME/config`, otherwise **off**.

### Installing with the curl script

`curl -fsSL https://pm0.thakur.dev/install.sh | bash` **asks** whether to
enable the API (`[y/N]`, default No; loopback `127.0.0.1:9615` only). Answering
`y` writes `PM0_HTTP_ADDR=127.0.0.1:9615` to `$PM0_HOME/config`, so the daemon
picks it up on its next start — including the in-place reload the installer
performs when a daemon is already running. Answering No leaves it disabled.

Non-interactive installs (no TTY, e.g. CI) skip the prompt. Pre-answer with
`PM0_HTTP=1` (enable) / `PM0_HTTP=0` (disable), or export `PM0_HTTP_ADDR`
yourself (the env var wins and is not written to the config).

> The installer itself does **not** start a daemon on a fresh install — the
> daemon starts on your first `pm0` command, or at boot via `pm0 startup`.

Verify:

```sh
pm0 ping                                       # gRPC control still works
curl -s http://127.0.0.1:9615/api/v1/health    # HTTP health (when enabled)
```

---

## 2. Running it as a service

If you manage pm0 with a systemd unit (`pm0 startup`), enable the API there
(the service does not read the shell environment):

```ini
[Service]
Environment=PM0_HTTP_ADDR=127.0.0.1:9615
ExecStart=/usr/local/bin/pm0 daemon
```

The `$PM0_HOME/config` file (§1) is read regardless of how the daemon starts,
so it is an alternative to editing the unit. Prefer `127.0.0.1` or a Unix
socket. Do **not** bind to `0.0.0.0`.

---

## 3. Security model (read this before exposing anything)

- **The daemon is a trusted, unauthenticated control plane.** Whoever can
  talk to the socket/port can start, stop and delete processes as the user
  running the daemon.
- **It has no authentication of its own.** That is deliberate: it is meant to
  be reachable only locally.
- **Bind locally.** Use `127.0.0.1:PORT` (loopback only) or
  `unix:/path.sock` (filesystem `0600`). Both are unreachable from the
  network unless a proxy exposes them.
- **Add auth in your own backend.** The intended topology is:

  ```
  browser/dashboard ──HTTP(auth)──► your backend service ──HTTP──► pm0 daemon
                                       (same host, 127.0.0.1)       (127.0.0.1 / UDS)
  ```

  Your backend terminates TLS + auth and re-exposes a narrow, permissioned
  API. The pm0 daemon is never exposed publicly.
- **Run as the right user.** The caller must run as the same user that owns
  `PM0_HOME` (default `~/.pm0`), or both must share a `PM0_HOME`. This is how
  the process/kill authority is scoped.
- **Secrets.** `pm2_env.env` contains the **full child environment**. If you
  do not want to serve secrets, run the daemon with `--redact-env` (or the
  `redact_env` policy) — it scrubs secret-looking values from
  jlist/describe/dump. See §6.5.

---

## 4. Base URL and conventions

- Base path: `/api/v1`
- Requests/responses are JSON (`Content-Type: application/json`).
- All `{target}` path segments are a **selector** (§7): a `pm_id` (`"3"`), an
  app name (`"web"`, which resolves the whole `web-0..web-N` family), or
  `"all"`.
- Mutating endpoints that are only meaningful with a body accept an empty body.
- Field names in request bodies accept both `snake_case` (`max_restarts`) and
  `lowerCamelCase` (`maxRestarts`) — they are decoded from `daemon.proto`.
  Unknown fields are ignored.

---

## 5. Endpoints

### 5.1 `GET /api/v1/health`

Daemon liveness and metadata (backed by the gRPC `Ping`).

```sh
curl -s http://127.0.0.1:9615/api/v1/health
```

```json
{
  "ok": true,
  "version": "1.0.1",
  "commit": "abc1234",
  "pid": 7712,
  "kill_mode": "cgroup-kill",
  "kernel_release": "6.6.0",
  "cgroup_warning": ""
}
```

`kill_mode` is one of `cgroup-kill`, `cgroup-freeze`, `pgid`.
`cgroup_warning` is non-empty when cgroup accounting is degraded.

### 5.2 `GET /api/v1/processes`

List every supervised app. Returns the **PM2-compatible `jlist` array** (the
exact JSON `pm0 jlist` prints). See §6 for the field reference.

```sh
curl -s http://127.0.0.1:9615/api/v1/processes
```

```json
[
  {
    "pid": 7731,
    "name": "web-0",
    "pm_id": 0,
    "monit": { "memory": 48896000, "cpu": 0.4 },
    "pm2_env": { "name": "web-0", "status": "online", "pm_id": 0, "...": "..." }
  }
]
```

An empty app set returns `[]` (never `null`).

### 5.3 `GET /api/v1/processes/{target}`

Describe one app in full (same object shape as one element of the list). On an
instance family the lowest `pm_id` wins, matching `pm0 describe`.

```sh
curl -s http://127.0.0.1:9615/api/v1/processes/web
curl -s http://127.0.0.1:9615/api/v1/processes/0
```

```json
{ "pid": 7731, "name": "web-0", "pm_id": 0, "monit": { "...": "..." }, "pm2_env": { "...": "..." } }
```

Missing app → `404` with `{"error":"no matching app"}`.

### 5.4 `POST /api/v1/processes` — start

Start one or more apps. The body accepts three shapes:

```jsonc
// (a) a single spec
{ "script": "app.js", "name": "web", "instances": 2 }

// (b) a wrapper with a specs array
{ "specs": [ { "script": "a.js" }, { "script": "b.js" } ] }

// (c) a bare array
[ { "script": "a.js" }, { "script": "b.js" } ]
```

Response: `200` with a **jlist array of the started apps** (same shape as
§5.2). `script` is required; a missing/blocked interpreter fails with `400`.

```sh
curl -s -X POST http://127.0.0.1:9615/api/v1/processes \
  -H 'Content-Type: application/json' \
  -d '{"script":"app.js","name":"web","instances":2,"env":{"PORT":"3000"}}'
```

**Key `ProcessSpec` fields** (JSON names; both `snake_case` and
`lowerCamelCase` accepted — full list in `api/v1/daemon.proto`):

| Field | Type | Notes |
|---|---|---|
| `name` | string | App name (default: script basename) |
| `script` | string | Path to script/binary — required |
| `args` | string[] | Arguments passed to the script |
| `interpreter` | string | `""` = auto-detect, `"none"` = exec directly, else a binary (`node`, `bun`, `python3`, …) |
| `interpreter_args` | string[] | Args for the interpreter |
| `cwd` | string | Working directory |
| `env` | map | Extra environment variables (merged over the daemon env) |
| `instances` | int | `1` = single fork; `>1` = cluster (Node) or `<name>-0..N-1` fork family |
| `exec_mode` | string | `"fork"` \| `"cluster"` (cluster needs a Node interpreter) |
| `autorestart` | bool | Restart on exit |
| `kill_signal` | string | Default `SIGINT` |
| `kill_timeout_ms` | int | Grace before force kill, default `1600` |
| `max_restarts` | int | Crash-loop budget, default `16` |
| `min_uptime_ms` | int | Stable-run threshold, default `1000` |
| `max_memory_restart_bytes` | int | `0` = off |
| `cron_restart` | string | Cron expression |
| `watch` | bool | Restart on file changes |
| `stop_exit_codes` | int[] | Exit codes that park the app `stopped` |
| `out_file` / `error_file` / `pid_file` | string | Explicit sinks (default `~/.pm0/logs`, `~/.pm0/pids`) |
| `log_rotate_max_bytes` | int | `0` = default (10 MiB), `-1` = off |
| `log_rotate_retain` | int | `0` = default (30) |
| `health_check_cmd` | string | `sh -c` probe while online |

Note: boolean fields cannot be distinguished as "unset" on the wire, so send
the values you want explicitly (e.g. `"autorestart": true`).

### 5.5 `POST /api/v1/processes/{target}/stop`

Stop (state is kept; status becomes `stopped`). Idempotent on stopped apps.

```sh
curl -s -X POST http://127.0.0.1:9615/api/v1/processes/web/stop
```

```json
{ "ok": true, "affected_pm_ids": [0, 1] }
```

### 5.6 `POST /api/v1/processes/{target}/restart`

Stop+start, preserving `pm_id` and config. Send `{"env":{...}}` to restart with
an updated environment (the `pm0 restart --update-env` behavior); omit the body
for a plain restart.

```sh
curl -s -X POST http://127.0.0.1:9615/api/v1/processes/web/restart
curl -s -X POST http://127.0.0.1:9615/api/v1/processes/web/restart \
  -H 'Content-Type: application/json' -d '{"env":{"PORT":"4000"}}'
```

```json
{ "ok": true, "affected_pm_ids": [0, 1] }
```

### 5.7 `POST /api/v1/processes/{target}/reload`

Rolling, zero-downtime reload (one instance at a time, health-gated,
cluster-aware). Names resolve to the whole family and are rolled in instance
order.

```sh
curl -s -X POST http://127.0.0.1:9615/api/v1/processes/web/reload
```

```json
{ "ok": true, "affected_pm_ids": [0, 1] }
```

### 5.8 `DELETE /api/v1/processes/{target}`

Stop (if running) and **forget** the app; its `pm_id` may be reused.

```sh
curl -s -X DELETE http://127.0.0.1:9615/api/v1/processes/web
```

```json
{ "ok": true, "affected_pm_ids": [0, 1] }
```

### 5.9 Scaling

Two equivalent forms:

- `POST /api/v1/processes/{target}/scale` (name from the path)
- `POST /api/v1/scale` (name from the body)

Body/`delta` semantics: `+N` adds instances, `-N` removes the highest-index
ones, `0` is a no-op that reports the family.

```sh
# scale the "web" family up by 2
curl -s -X POST http://127.0.0.1:9615/api/v1/processes/web/scale \
  -H 'Content-Type: application/json' -d '{"delta": 2}'

# equivalent, name in the body
curl -s -X POST http://127.0.0.1:9615/api/v1/scale \
  -H 'Content-Type: application/json' -d '{"name":"web","delta":-1}'
```

```json
{ "ok": true, "affected_pm_ids": [2, 3] }
```

### 5.10 `GET /api/v1/processes/{target}/logs` — Server-Sent Events

Streams logs as SSE. Each frame is `data: <json>\n\n` where `<json>` is:

```json
{ "pm_id": 0, "name": "web-0", "stream": "stdout", "data": "line text" }
```

`stream` is `"stdout"` or `"stderr"`. A terminal error is delivered as an
`event: error` frame; a leading `: pm0 log stream` comment flushes headers.

Query parameters:

| Param | Default | Meaning |
|---|---|---|
| `lines` | `100` | Backlog lines from the ring buffer (`0` = live only) |
| `follow` | `true` | Keep streaming live lines after the backlog |
| `stderr` | `false` | Include stderr (separate sinks are filtered otherwise) |
| `raw` | `false` | Emit lines without the `name: ` prefix |

```sh
# follow web's stdout live
curl -N http://127.0.0.1:9615/api/v1/processes/web/logs

# last 200 lines of stdout+stderr, then close
curl -N 'http://127.0.0.1:9615/api/v1/processes/web/logs?lines=200&stderr=true&follow=false'
```

Browser / Node consumer:

```js
const es = new EventSource("http://127.0.0.1:9615/api/v1/processes/web/logs?lines=100");
es.onmessage = (e) => {
  const line = JSON.parse(e.data); // { pm_id, name, stream, data }
  console.log(line.name, line.data);
};
```

> Behind a reverse proxy, disable response buffering for this route
> (`proxy_buffering off;` in nginx). The daemon already sends
> `X-Accel-Buffering: no`.

### 5.11 `POST /api/v1/daemon/save` and `POST /api/v1/daemon/resurrect`

Persist the current app set to `dump.json`, and adopt/start from it.

```sh
curl -s -X POST http://127.0.0.1:9615/api/v1/daemon/save
# { "ok": true, "dump_path": "/home/me/.pm0/dump.json", "saved_count": 2 }

curl -s -X POST http://127.0.0.1:9615/api/v1/daemon/resurrect
# { "ok": true, "started_count": 2, "errored_count": 0 }
```

---

## 6. Data model — the process object (`jlist` shape)

List and describe return the PM2-compatible object (identical to `pm0 jlist`).
Each element has **exactly** these top-level keys:

| Key | Type | Meaning |
|---|---|---|
| `pid` | number | OS pid (`0` when not running) |
| `name` | string | App name (cluster instances share the name; fork families are `<name>-<i>`) |
| `pm_id` | number | Stable supervisor id |
| `monit` | object | `{ memory, cpu }` |
| `pm2_env` | object | The full environment/config + runtime (§6.3) |

### 6.1 `monit`

| Key | Type | Meaning |
|---|---|---|
| `memory` | number | RSS of the **process tree** in bytes |
| `cpu` | number | Percent of one core (first sample after a start may read `0`) |

### 6.2 `status` vocabulary (load-bearing)

`launching` · `online` · `stopped` · `stopping` · `waiting restart` ·
`errored`

`errored` means a crash-loop exhausted `max_restarts`. `waiting restart`
appears inside a `restart_delay`/backoff window.

### 6.3 `pm2_env` (selected keys)

`name`, `namespace`, `pm_id`, `pm_exec_path` (the **script** path, never the
interpreter), `args` (always `[]`, never `null`), `pm_cwd`,
`pm_out_log_path`, `pm_err_log_path`, `pm_pid_path`, `interpreter` **and**
`exec_interpreter` (same value), `interpreter_args` **and** `node_args` (same
list), `exec_mode` (`fork_mode` \| `cluster_mode`), `instances`,
`autorestart`, `max_restarts`, `min_uptime`, `max_memory_restart`,
`kill_signal`, `kill_timeout`, `wait_ready`, `listen_timeout`,
`cron_restart`, `watch`, `exp_backoff_restart_delay`, `stop_exit_codes`,
`restart_time`, `unstable_restarts`, `created_at`, `pm_uptime`, `exit_code`,
`treekill` (`true`), `time`, `merge_logs`, `log_date_format`, `env`.

Presence / null rules (match PM2 exactly):

- `exit_code` is `null` while running and for signal deaths.
- `created_at` is `null` after a crash-loop parks the app `errored`.
- `args` and `interpreter_args` are `[]`, never `null`.

### 6.4 Timestamps

`created_at` and `pm_uptime` are **epoch milliseconds**. Compute uptime as
`Date.now() - pm_uptime`.

### 6.5 `env` and redaction

`pm2_env.env` is the **full child environment** (daemon env merged with the
app's `env`), matching PM2 behavior. If that is undesirable, run the daemon
with `--redact-env`: secret-looking values are replaced before they reach the
API (also in `dump.json`). For cluster apps a `NODE_APP_INSTANCE` key is
present.

---

## 7. Selectors (`{target}`)

| Value | Resolves to |
|---|---|
| `3` (digits) | the app with `pm_id = 3` |
| `web` | exact name matches **and** the family `web-0..web-N` |
| `all` | every supervised app |

Selectors are resolved server-side identically to the CLI, so dashboard
actions behave the same as `pm0 restart web`, etc.

---

## 8. Errors and HTTP status codes

Every error body is `{"error":"<message>"}`. The gRPC status is mapped onto
HTTP:

| HTTP | When |
|---|---|
| `200` | Success |
| `400` | Invalid argument — bad spec, missing `script`/name, bad JSON |
| `404` | No app matched the selector |
| `405` | Wrong method for the path |
| `409` | Already exists |
| `429` | Resource exhausted |
| `500` | Internal error |
| `503` | Daemon unavailable |

Note: on a **partial batch failure** during start, the registered apps are
still returned with `200` (failures are registered, parked `errored`) — the
response list reflects the truth; re-list to reconcile.

---

## 9. Integration example (Node/TS backend)

The recommended topology: your backend talks to the daemon on
`127.0.0.1` (or a UDS) and re-exposes a **narrow, authenticated** API to the
dashboard. Do not let a browser hit the daemon directly.

```ts
// pm0.ts — a thin typed client for the local pm0 HTTP API.
const BASE = process.env.PM0_API ?? "http://127.0.0.1:9615/api/v1";

export interface Proc {
  pid: number;
  name: string;
  pm_id: number;
  monit: { memory: number; cpu: number };
  pm2_env: { status: string; pm_uptime: number; [k: string]: unknown };
}

async function call(method: string, path: string, body?: unknown) {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: body ? { "content-type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`pm0 ${method} ${path}: ${res.status} ${await res.text()}`);
  return res.json();
}

export const pm0 = {
  list: (): Promise<Proc[]> => call("GET", "/processes"),
  describe: (t: string): Promise<Proc> => call("GET", `/processes/${t}`),
  start: (spec: unknown) => call("POST", "/processes", spec),
  stop: (t: string) => call("POST", `/processes/${t}/stop`),
  restart: (t: string, env?: Record<string, string>) =>
    call("POST", `/processes/${t}/restart`, env ? { env } : undefined),
  reload: (t: string) => call("POST", `/processes/${t}/reload`),
  remove: (t: string) => call("DELETE", `/processes/${t}`),
  scale: (name: string, delta: number) => call("POST", "/scale", { name, delta }),
};
```

Proxy your own routes (with auth) onto these:

```ts
import express from "express";
import { pm0 } from "./pm0";

const app = express();
app.use(express.json());

// --- add your auth middleware here before these routes ---
app.get("/api/processes", async (_req, res, next) => {
  try { res.json(await pm0.list()); } catch (e) { next(e); }
});
app.post("/api/processes/:target/:action(restart|reload|stop)", async (req, res, next) => {
  try {
    const { target, action } = req.params;
    res.json(await (pm0 as any)[action === "remove" ? "remove" : action](target));
  } catch (e) { next(e); }
});

// Stream logs: proxy the SSE body straight through.
app.get("/api/processes/:target/logs", async (req, res) => {
  const up = await fetch(
    `${process.env.PM0_API ?? "http://127.0.0.1:9615/api/v1"}/processes/${req.params.target}/logs?${new URLSearchParams(req.query as any)}`,
  );
  res.writeHead(200, {
    "content-type": "text/event-stream",
    "cache-control": "no-cache",
    connection: "keep-alive",
  });
  up.body!.pipeTo(new WritableStream({ write: (c) => void res.write(c) }));
});

app.listen(8080);
```

Bun equivalent: replace the listener with `Bun.serve({ port: 8080, fetch })`.

### Auth checklist

- Put a real auth layer (session/JWT/API key) in front of the dashboard-facing
  routes.
- Bind the process API to loopback only.
- Restrict who can `start`/`delete` (those are the most powerful actions).
- Consider running the daemon as a dedicated user with its own `PM0_HOME`, so
  the API can only supervise that user's apps.

---

## 10. Notes and limitations

- **Off by default.** Nothing listens unless `PM0_HTTP_ADDR` (env or
  `$PM0_HOME/config`) enables it. The setting is read at daemon boot — restart
  the daemon (`pm0 kill` then any command) or run `pm0 update` for a change to
  take effect (see §1).
- **One HTTP port per host, per daemon.** If you run several daemons (distinct
  `PM0_HOME`s), give each its own port or a Unix socket; only the first to bind
  a given port wins, and the rest log a bind warning and keep supervising
  without the HTTP API.
- **One daemon per `PM0_HOME` per user.** The HTTP API is served by that same
  daemon; there is no separate server. Whoever can reach the bind can control
  every app in that home.
- **No built-in authentication.** Deliberate — it is a local control plane.
  Add auth in your backend (§3, §9).
- **The gRPC API remains the primary contract.** This HTTP layer is a facade
  over the same methods; anything not exposed here (e.g. `UpdateDaemon`,
  `KillDaemon`) is intentionally left to the CLI/SDK.
- **Booleans are copy-through.** The wire cannot distinguish "unset" from
  `false`; send the values you intend on `start`.
- **Cluster apps share a name.** `web` with `instances: 4` yields four rows
  all named `web`, distinguished by `pm_id`/`pm2_env.NODE_APP_INSTANCE`. Fork
  families are `web-0..web-3`.
- **The port is your choice.** `9615` is just the documented default in these
  examples; `PM0_HTTP_ADDR` accepts any local address or a UDS.

---

## Appendix A — endpoint summary

| Method | Path | Body | Returns |
|---|---|---|---|
| GET | `/api/v1/health` | — | daemon metadata |
| GET | `/api/v1/processes` | — | jlist array |
| POST | `/api/v1/processes` | spec \| `{specs}` \| `[spec]` | started apps (jlist) |
| GET | `/api/v1/processes/{target}` | — | one app (jlist) |
| DELETE | `/api/v1/processes/{target}` | — | `{ok, affected_pm_ids}` |
| POST | `/api/v1/processes/{target}/restart` | `{env}` (optional) | `{ok, affected_pm_ids}` |
| POST | `/api/v1/processes/{target}/reload` | — | `{ok, affected_pm_ids}` |
| POST | `/api/v1/processes/{target}/stop` | — | `{ok, affected_pm_ids}` |
| POST | `/api/v1/processes/{target}/scale` | `{delta}` | `{ok, affected_pm_ids}` |
| GET | `/api/v1/processes/{target}/logs` | — (query: `lines`,`follow`,`stderr`,`raw`) | SSE stream |
| POST | `/api/v1/scale` | `{name, delta}` | `{ok, affected_pm_ids}` |
| POST | `/api/v1/daemon/save` | — | `{ok, dump_path, saved_count}` |
| POST | `/api/v1/daemon/resurrect` | — | `{ok, started_count, errored_count}` |

---

## See also

- `api/v1/daemon.proto` — the authoritative request/response schema.
- `pkg/client/jlist.go` — the exact `jlist` JSON contract.
- `internal/daemon/http.go` — this API's implementation (handlers map to the
  same methods the CLI uses).
