#!/usr/bin/env node
// pm0 postinstall — make sure a native pm0 binary exists for this platform.
//
// Order:
//   1. bundled prebuild (release npm packages ship prebuilds/<os>-<arch>/pm0)
//   2. build from the Go source included in this package (needs Go >= 1.26)
//
// Set PM0_SKIP_BUILD=1 to disable step 2, or PM0_GO to point at a specific
// go binary. Failing both steps is a hard error: a pm0 install without the
// native binary is useless, and a loud failure beats a broken `pm0` later.
"use strict";

const path = require("path");
const fs = require("fs");
const os = require("os");
const { spawnSync } = require("child_process");

const pkgRoot = __dirname + "/..";
const pkg = require(path.join(pkgRoot, "package.json"));
const goArchMap = { x64: "amd64", ia32: "386", arm64: "arm64", arm: "arm" };
const osName = process.platform; // go uses the same names except win32 === windows
const goOS = osName === "win32" ? "windows" : osName;
const goArch = goArchMap[process.arch] || process.arch;
const exe = goOS === "windows" ? "pm0.exe" : "pm0";
const outDir = path.join(pkgRoot, "prebuilds", `${goOS}-${goArch}`);
const outPath = path.join(outDir, exe);

function log(msg) {
  process.stdout.write(`pm0: ${msg}\n`);
}

function findPrebuild() {
  if (fs.existsSync(outPath)) return outPath;
  // also accept release bundles that only shipped this platform
  const local = path.join(pkgRoot, "build", `pm0-${osName}-${process.arch}`, exe);
  if (fs.existsSync(local)) return local;
  return null;
}

function findGo() {
  const candidates = [process.env.PM0_GO, process.env.GO, "go"].filter(Boolean);
  const extraDirs = [
    "/usr/local/go/bin",
    "/usr/lib/go/bin",
    "/opt/homebrew/bin",
    "/usr/local/bin",
    path.join(os.homedir(), "go", "bin"),
    path.join(os.homedir(), "sdk", "go", "bin"),
  ];
  for (const cmd of candidates) {
    const probe = spawnSync(cmd, ["env", "GOPATH"], { encoding: "utf8" });
    if (probe.status === 0) return cmd;
    for (const dir of extraDirs) {
      const full = path.join(dir, cmd);
      if (fs.existsSync(full)) return full;
    }
  }
  return null;
}

function goVersionOk(goCmd) {
  const probe = spawnSync(goCmd, ["version"], { encoding: "utf8" });
  const m = /go1\.(\d+)/.exec((probe.stdout || "") + (probe.stderr || ""));
  if (!m) return true; // unknown format: let the build try and fail loudly
  const minor = Number(m[1]);
  return minor >= 26; // go.mod says go 1.26
}

const existing = findPrebuild();
if (existing) {
  log(`using prebuilt binary ${path.relative(pkgRoot, existing)}`);
  process.exit(0);
}

if (process.env.PM0_SKIP_BUILD === "1") {
  log("PM0_SKIP_BUILD=1 and no prebuild for this platform — skipping the source build.");
  process.exit(0);
}

const go = findGo();
if (!go) {
  process.stderr.write(
    `pm0: no prebuild for ${goOS}/${goArch} and no Go toolchain found.\n` +
      `  pm0 is written in Go; to finish the install either:\n` +
      `    - install Go >= 1.26 (https://go.dev/dl) and rerun:  npm rebuild -g pm0\n` +
      `    - or build + install from a source checkout:         make build install\n` +
      `    - or grab a release tarball:                         make release\n`
  );
  process.exit(1);
}

if (!goVersionOk(go)) {
  process.stderr.write(`pm0: Go >= 1.26 required (found: ${go}), refusing to build.\n`);
  process.exit(1);
}

log(`no prebuild for ${goOS}/${goArch} — building from source with ${go} ...`);
fs.mkdirSync(outDir, { recursive: true });
const version = pkg.version || "0.0.0";
const build = spawnSync(
  go,
  [
    "build",
    "-trimpath",
    `-ldflags=-s -w -X main.version=${version} -X main.commit=npm`,
    `-o=${outPath}`,
    "./cmd/pm0",
  ],
  { cwd: pkgRoot, encoding: "utf8", env: { ...process.env, CGO_ENABLED: "0" } }
);

if (build.error || build.status !== 0) {
  process.stderr.write(build.stderr || String(build.error) + "\n");
  process.stderr.write(`pm0: source build failed — see the steps above or ` + `https://github.com/thakurdotdev/pm0#install\n`);
  process.exit(1);
}

log(`built ${path.relative(pkgRoot, outPath)} (pm0 ${version})`);
