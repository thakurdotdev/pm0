// Static-export extras that a Next server used to handle at request time:
// - `/docs/<slug>.md` serves the page's Markdown (was a rewrite to /llms.mdx/docs/<slug>/content.md).
// - `/docs` redirects to /docs/introduction.
import { cpSync, existsSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { mkdirSync, writeFileSync } from "node:fs";

const out = join(import.meta.dir, "..", "out");
const llmsRoot = join(out, "llms.mdx", "docs");

if (!existsSync(llmsRoot)) {
  console.error("postbuild: out/llms.mdx/docs missing — did `next build` run with output: export?");
  process.exit(1);
}

let copied = 0;
function walk(dir: string) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p);
    else if (name === "content.md") {
      const slug = relative(llmsRoot, dirname(p));
      if (!slug) continue;
      const dest = join(out, "docs", `${slug}.md`);
      mkdirSync(dirname(dest), { recursive: true });
      cpSync(p, dest);
      copied++;
    }
  }
}
walk(llmsRoot);
console.log(`postbuild: wrote ${copied} docs/<slug>.md files`);

// `/docs` has no page of its own (the server used to redirect it), so ship a redirect page.
writeFileSync(
  join(out, "docs", "index.html"),
  `<!doctype html><meta charset="utf-8"><title>pm0 docs</title>` +
    `<link rel="canonical" href="/docs/introduction">` +
    `<meta http-equiv="refresh" content="0; url=/docs/introduction">` +
    `<script>location.replace("/docs/introduction")</script>` +
    `<a href="/docs/introduction">Continue to the docs</a>\n`,
);
