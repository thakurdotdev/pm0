#!/usr/bin/env node
// pm0 npm shim — resolves the native pm0 binary bundled with this package
// and hands off. The Go binary is the real pm0; this file only finds it.
//
// Resolution order:
//   1. PM0_BINARY env var (explicit override)
//   2. prebuilds/<platform>-<arch>/pm0[-exe]  (populated at release time or
//      by the postinstall source build)
//   3. build/pm0-<platform>-<arch>            (dev layout from the Makefile)
"use strict";

const path = require("path");
const fs = require("fs");
const { spawn } = require("child_process");

const pkgRoot = path.resolve(__dirname, "..");
const platform = process.platform; // linux | darwin | win32 | freebsd
const arch = process.arch; // x64 | arm64 | arm | ia32 ...

function exeName(base) {
  return platform === "win32" ? base + ".exe" : base;
}

// normalize node arch names to Go GOARCH names
function goArch(a) {
  return { x64: "amd64", ia32: "386", arm64: "arm64", arm: "arm" }[a] || a;
}

function candidates() {
  const list = [];
  if (process.env.PM0_BINARY) list.push(process.env.PM0_BINARY);
  const ga = goArch(arch);
  list.push(path.join(pkgRoot, "prebuilds", `${platform}-${ga}`, exeName("pm0")));
  list.push(path.join(pkgRoot, "prebuilds", `${platform}-${arch}`, exeName("pm0")));
  list.push(path.join(pkgRoot, "build", `pm0-${platform}-${arch}`, exeName("pm0")));
  return list;
}

const binary = candidates().find((p) => {
  try {
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
});

if (!binary) {
  process.stderr.write(
    `pm0: native binary not found for ${platform}/${arch}.\n` +
      `  - reinstall so the postinstall step can fetch/build it:  npm rebuild -g pm0\n` +
      `  - or build from source (needs Go >= 1.26):               make build install\n` +
      `  - or point PM0_BINARY at an existing pm0 executable\n`
  );
  process.exit(1);
}

const isWin = platform === "win32";
const child = spawn(binary, process.argv.slice(2), {
  stdio: ["inherit", "inherit", "inherit"],
  windowsHide: true,
});

child.on("error", (err) => {
  process.stderr.write(`pm0: failed to exec ${binary}: ${err.message}\n`);
  process.exit(1);
});

// Forward termination signals so interactive commands (logs, daemon) behave.
for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => child.kill(sig === "SIGHUP" && isWin ? "SIGTERM" : sig));
}

child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  process.exit(code === undefined || code === null ? 1 : code);
});
