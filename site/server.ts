// pm0.thakur.dev — tiny fast Bun server for landing + docs + install.sh
// Run: bun ./server.ts  (PORT=3000 by default)
// Deploy: pm0 start ecosystem.config.js

const ROOT = import.meta.dir;
const PUBLIC = `${ROOT}/public`;
const INSTALL_SRC = `${ROOT}/../install.sh`;
const PKG_SRC = `${ROOT}/../package.json`;

const PORT = Number(process.env.PORT || 3000);

let lastGhCheck = 0;
let ghReleaseTag = "";

function semverCompare(a: string, b: string): number {
  const pa = a.replace(/^v/, "").split(".").map((n) => parseInt(n, 10) || 0);
  const pb = b.replace(/^v/, "").split(".").map((n) => parseInt(n, 10) || 0);
  for (let i = 0; i < 3; i++) {
    const na = pa[i] || 0;
    const nb = pb[i] || 0;
    if (na > nb) return 1;
    if (na < nb) return -1;
  }
  return 0;
}

async function getDynamicVersion(): Promise<string> {
  let ver = "0.1.3";
  try {
    const f = Bun.file(PKG_SRC);
    if (await f.exists()) {
      const data = await f.json();
      if (data?.version) ver = data.version.replace(/^v/, "");
    }
  } catch {}

  const now = Date.now();
  if (now - lastGhCheck > 5 * 60 * 1000) {
    lastGhCheck = now;
    try {
      const res = await fetch("https://api.github.com/repos/thakurdotdev/pm0/releases/latest", {
        headers: { "User-Agent": "pm0-site", "Accept": "application/vnd.github.v3+json" },
        signal: AbortSignal.timeout(3000),
      });
      if (res.ok) {
        const d: any = await res.json();
        if (d?.tag_name) {
          ghReleaseTag = d.tag_name.replace(/^v/, "");
        }
      }
    } catch {}
  }

  if (ghReleaseTag && semverCompare(ghReleaseTag, ver) > 0) {
    return ghReleaseTag;
  }
  return ver;
}

const MIME: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".xml": "application/xml; charset=utf-8",
  ".txt": "text/plain; charset=utf-8",
  ".sh": "text/x-shellscript; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".webmanifest": "application/manifest+json",
};

function extOf(path: string): string {
  const i = path.lastIndexOf(".");
  return i >= 0 ? path.slice(i) : "";
}

async function serveFile(path: string, opts: { cache?: string; type?: string } = {}) {
  const f = Bun.file(path);
  if (!(await f.exists())) return null;
  const isDev = process.env.NODE_ENV !== "production";
  const headers: Record<string, string> = {
    "content-type": opts.type ?? MIME[extOf(path)] ?? "application/octet-stream",
    "cache-control": isDev ? "no-cache, no-store, must-revalidate" : (opts.cache ?? "public, max-age=3600"),
    "x-content-type-options": "nosniff",
  };
  return new Response(f, { headers });
}

const SEC = {
  "x-content-type-options": "nosniff",
  "x-frame-options": "DENY",
  "referrer-policy": "strict-origin-when-cross-origin",
} as const;

async function html(path: string) {
  const f = Bun.file(path);
  if (!(await f.exists())) return null;
  let text = await f.text();
  const ver = await getDynamicVersion();
  const tag = `v${ver}`;

  // Inject live version into HTML before serving
  const scriptTag = `<script>window.__PM0_VERSION__="${ver}";window.__PM0_TAG__="${tag}";</script>`;
  if (text.includes("</head>")) {
    text = text.replace("</head>", `${scriptTag}</head>`);
  }

  const isDev = process.env.NODE_ENV !== "production";
  return new Response(text, {
    headers: {
      "content-type": "text/html; charset=utf-8",
      "cache-control": isDev ? "no-cache, no-store, must-revalidate" : "public, max-age=60",
      ...SEC,
    },
  });
}

const ROBOTS = `User-agent: *
Allow: /
Sitemap: https://pm0.thakur.dev/sitemap.xml
`;

const SITEMAP = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://pm0.thakur.dev/</loc><changefreq>weekly</changefreq><priority>1.0</priority></url>
  <url><loc>https://pm0.thakur.dev/docs</loc><changefreq>weekly</changefreq><priority>0.9</priority></url>
</urlset>
`;

Bun.serve({
  port: PORT,
  hostname: "0.0.0.0",
  async fetch(req) {
    const url = new URL(req.url);
    const p = url.pathname;

    // health
    if (p === "/health" || p === "/healthz") {
      return Response.json({ status: "ok", service: "pm0-site" }, { headers: { ...SEC, "cache-control": "no-store" } });
    }

    // dynamic version endpoint
    if (p === "/api/version" || p === "/version.json") {
      const ver = await getDynamicVersion();
      const tag = `v${ver}`;
      return Response.json(
        {
          version: ver,
          tag,
          user: "curl -fsSL https://pm0.thakur.dev/install.sh | bash",
          sudo: "curl -fsSL https://pm0.thakur.dev/install.sh | sudo bash",
          pinned: `curl -fsSL https://pm0.thakur.dev/install.sh | PM0_VERSION=${tag} bash`,
          binary: `curl -fsSL https://github.com/thakurdotdev/pm0/releases/download/${tag}/pm0-linux-amd64 -o /usr/local/bin/pm0 && chmod +x /usr/local/bin/pm0`,
        },
        {
          headers: {
            ...SEC,
            "cache-control": "public, max-age=60",
            "access-control-allow-origin": "*",
          },
        }
      );
    }

    // install script — served from repo root so it never drifts
    if (p === "/install.sh") {
      const f = Bun.file(INSTALL_SRC);
      if (await f.exists()) {
        const text = await f.text();
        // Rewrite any stale raw.githubusercontent URLs to canonical domain in comments only
        // (kept minimal — the script itself is source of truth)
        return new Response(text, {
          headers: {
            ...SEC,
            "content-type": "text/x-shellscript; charset=utf-8",
            "cache-control": "public, max-age=300",
            "content-disposition": 'inline; filename="install.sh"',
          },
        });
      }
      return new Response("# install.sh not found\n", { status: 500 });
    }

    // seo
    if (p === "/robots.txt") {
      return new Response(ROBOTS, { headers: { ...SEC, "content-type": "text/plain; charset=utf-8", "cache-control": "public, max-age=86400" } });
    }
    if (p === "/sitemap.xml") {
      return new Response(SITEMAP, { headers: { ...SEC, "content-type": "application/xml; charset=utf-8", "cache-control": "public, max-age=86400" } });
    }

    // pages
    if (p === "/" || p === "/index.html") {
      const r = await html(`${PUBLIC}/index.html`);
      if (r) return r;
    }
    if (p === "/docs" || p === "/docs/" || p === "/docs.html") {
      const r = await html(`${PUBLIC}/docs.html`);
      if (r) return r;
    }

    // static assets (css/js/svg/...)
    if (p.startsWith("/assets/") || p.endsWith(".css") || p.endsWith(".js") || p.endsWith(".svg") || p.endsWith(".ico") || p.endsWith(".png")) {
      const clean = p.replace(/\.\./g, "");
      const isDev = process.env.NODE_ENV !== "production";
      const r = await serveFile(`${PUBLIC}${clean}`, { cache: isDev ? "no-cache, no-store, must-revalidate" : "public, max-age=300" });
      if (r) return r;
    }

    // generic public file (safe, no dotfiles)
    if (!p.includes("..") && !p.startsWith("/.")) {
      const candidate = `${PUBLIC}${p}`;
      if (extOf(p)) {
        const isDev = process.env.NODE_ENV !== "production";
        const r = await serveFile(candidate, { cache: isDev ? "no-cache, no-store, must-revalidate" : "public, max-age=300" });
        if (r) return r;
      }
    }

    // 404
    const notFound = await Bun.file(`${PUBLIC}/404.html`).exists()
      ? await Bun.file(`${PUBLIC}/404.html`).text()
      : `<!doctype html><html><head><title>404 — pm0</title><meta name="robots" content="noindex"></head><body style="font-family:system-ui;background:#0b0e14;color:#e6edf3;display:grid;place-items:center;min-height:100vh"><main style="text-align:center"><h1>404</h1><p>Page not found.</p><p><a href="/" style="color:#22d3ee">← pm0.thakur.dev</a></p></main></body></html>`;
    return new Response(notFound, { status: 404, headers: { ...SEC, "content-type": "text/html; charset=utf-8", "cache-control": "no-store" } });
  },
});

console.log(`pm0 site listening on http://0.0.0.0:${PORT}`);
