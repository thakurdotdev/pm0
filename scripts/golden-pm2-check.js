#!/usr/bin/env node
/*
 * golden-pm2-check.js — contract assertions against REAL pm2 jlist output.
 *
 * The pm0 golden fixtures (test/golden/fixtures) pin pm0's own wire
 * shape; the pm2 side cannot byte-diff the same fixtures (scenario args,
 * scripts and interpreters necessarily differ). This checker instead runs
 * the same SEMANTIC scenarios against real pm2 and asserts the contract
 * properties both sides must share (docs/compat.md §2/§3):
 *
 *   - top-level key set is exactly { pid, pm_id, name, monit, pm2_env }
 *   - §2.2 config + runtime keys present in pm2_env
 *   - exit_code null while running; recorded after exit (autorestart off)
 *   - status vocabulary: online / stopped / errored
 *   - restart_time counts relaunches; unstable_restarts hits max_restarts
 *   - lowest-unused pm_id reuse after delete (§3.2)
 *   - exec_mode renders "fork_mode"; exec_interpreter is the interpreter
 *
 * Usage: node golden-pm2-check.js <pm2-binary>
 * Exit 0 = all assertions hold; nonzero = drift (either side).
 */

const { execFileSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const pm2bin = process.argv[2] || "pm2";
const home = fs.mkdtempSync(path.join(os.tmpdir(), "pm2-golden-"));
const work = fs.mkdtempSync(path.join(os.tmpdir(), "pm2-golden-work-"));

function pm2(...args) {
  return execFileSync(pm2bin, args, {
    env: { ...process.env, PM2_HOME: home },
    encoding: "utf8",
  });
}

function jlist() {
  return JSON.parse(pm2("jlist"));
}

// Atomics.wait: synchronous sleep without spawning threads.
const sleep = (ms) => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);

function waitStatus(name, status, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const app = jlist().find((p) => p.name === name);
    if (app && app.pm2_env.status === status) return app;
    sleep(50);
  }
  throw new Error(`timeout: ${name} never reached ${status}`);
}

let failures = 0;
function check(name, cond, detail) {
  if (cond) {
    console.log(`  ok  ${name}`);
  } else {
    failures++;
    console.error(`FAIL  ${name}${detail ? " — " + detail : ""}`);
  }
}

const TOP_KEYS = ["monit", "name", "pid", "pm2_env", "pm_id"].sort();
// Keys both pm2 and pm0 emit. NOTE: pm2 7.0.4 no longer emits `args`
// (pm0 keeps it — pm2 5.x compat, compat.md row 2) and dropped several
// §1 keys from pm2_env (min_uptime, kill_timeout, ...); pm0 keeps the
// 5.x key set. Those omissions are documented in compat.md §5.
const ENV_KEYS = [
  "name", "pm_id", "pm_exec_path", "pm_cwd", "pm_out_log_path",
  "pm_err_log_path", "pm_pid_path", "exec_interpreter", "exec_mode",
  "instances", "autorestart", "created_at", "pm_uptime", "restart_time",
  "unstable_restarts", "status", "env", "namespace",
];

// Driver scripts: one stays alive until killed; one exits code 3 quickly.
const nodeApp = path.join(work, "app.js");
fs.writeFileSync(nodeApp, "setTimeout(()=>{}, 1e7);\n");
const exitApp = path.join(work, "exiter.js");
fs.writeFileSync(exitApp, "setTimeout(()=>process.exit(3), 50);\n");

console.log(`golden-pm2: pm2=${pm2bin} PM2_HOME=${home}`);

// --- S1: start / jlist shape / stop -----------------------------------
pm2("start", nodeApp, "--name", "web");
const web = waitStatus("web", "online");
check("S1 top-level keys", JSON.stringify(Object.keys(web).sort()) === JSON.stringify(TOP_KEYS),
  JSON.stringify(Object.keys(web).sort()));
// Observed pm2: exit_code absent until the first exit (pm2 7) or null
// while running; after ANY exit (signal deaths included) it records
// code || 0. pm0 emits null while running — both satisfy the contract.
check("S1 exit_code null/absent while running",
  web.pm2_env.exit_code === null || !("exit_code" in web.pm2_env),
  JSON.stringify(web.pm2_env.exit_code));
check("S1 exec_mode renders fork_mode", web.pm2_env.exec_mode === "fork_mode",
  web.pm2_env.exec_mode);
check("S1 exec_interpreter present", typeof web.pm2_env.exec_interpreter === "string");
for (const k of ENV_KEYS) {
  check(`S1 pm2_env.${k} present`, k in web.pm2_env);
}
check("S1 monit has memory+cpu",
  web.monit && typeof web.monit.memory === "number" && typeof web.monit.cpu === "number");

pm2("stop", "web");
const webStopped = waitStatus("web", "stopped");
check("S1 status stops at stopped", webStopped.pm2_env.status === "stopped");

// --- S2: crash loop parks errored, counters pinned ---------------------
// pm2 7 dropped --min-uptime from the CLI; the ecosystem file still
// carries it (programmatic config is the stable surface).
const ecoFile = path.join(work, "crash.config.js");
fs.writeFileSync(ecoFile, `module.exports = {
  apps: [{
    name: "crasher",
    script: ${JSON.stringify(exitApp)},
    max_restarts: 2,
    min_uptime: 5000,
  }],
};
`);
pm2("start", ecoFile);
const crasher = waitStatus("crasher", "errored", 30000);
check("S2 errored after budget", crasher.pm2_env.status === "errored");
// Observed pm2 (5.4.2 + 7.0.4): parking errored resets unstable_restarts
// to 0 and sets created_at = null (God.handleExit).
check("S2 unstable_restarts reset to 0 at errored", crasher.pm2_env.unstable_restarts === 0,
  String(crasher.pm2_env.unstable_restarts));
check("S2 created_at null at errored", crasher.pm2_env.created_at === null,
  JSON.stringify(crasher.pm2_env.created_at));
check("S2 restart_time counts relaunches (1)", crasher.pm2_env.restart_time === 1,
  String(crasher.pm2_env.restart_time));

// --- S3: --no-autorestart records exit code ----------------------------
pm2("start", exitApp, "--name", "oneshot", "--no-autorestart");
const oneshot = waitStatus("oneshot", "stopped", 30000);
check("S3 exit_code recorded", oneshot.pm2_env.exit_code === 3,
  JSON.stringify(oneshot.pm2_env.exit_code));

// --- S4: pm_id MONOTONIC allocation (§3.2, M2 correction) ---------------
// Observed pm2 (source God.next_id++ + experiment): ids never rewind while
// the registry is non-empty; deleting the LAST app resets the counter.
const beforeIds = jlist().map((p) => p.pm_id);
const maxId = Math.max(...beforeIds);
pm2("delete", "crasher");
pm2("start", nodeApp, "--name", "replacement");
const replacement = waitStatus("replacement", "online");
check("S4 id NOT reused after delete (monotonic)", replacement.pm_id === maxId + 1,
  `got ${replacement.pm_id}, want ${maxId + 1}`);

// --- cleanup ------------------------------------------------------------
try { pm2("kill"); } catch {}

fs.rmSync(home, { recursive: true, force: true });
fs.rmSync(work, { recursive: true, force: true });

if (failures > 0) {
  console.error(`golden-pm2: ${failures} assertion(s) failed`);
  process.exit(1);
}
console.log("golden-pm2: all contract assertions hold against real pm2");
