# pm0 site — pm0.thakur.dev

Tiny Bun server for the pm0 landing page + docs + `/install.sh`.

## Layout

```
site/
  server.ts            # Bun.serve — pages, static, /install.sh, /health, SEO files
  ecosystem.config.js  # pm0 deploy file
  package.json
  public/
    index.html         # landing
    docs.html          # docs
    styles.css
    script.js
    favicon.svg
    404.html
```

`/install.sh` is served live from the repo root `../install.sh`, so the
installer never drifts. Canonical install command:

```sh
curl -fsSL https://pm0.thakur.dev/install.sh | bash
```

## Run

```sh
cd site
bun --hot ./server.ts      # dev (PORT=3000)
PORT=8080 bun ./server.ts  # custom port
```

## Deploy with pm0

```sh
cd site
pm0 start ecosystem.config.js --env production
pm0 save
pm0 startup   # systemd unit for boot resurrect (as root)
```

Put behind Caddy/Nginx for TLS, e.g. Caddy:

```
pm0.thakur.dev {
  reverse_proxy 127.0.0.1:3000
}
```

## Routes

| Path | What |
|---|---|
| `/` | Landing |
| `/docs` | Documentation |
| `/install.sh` | Installer (from repo root) |
| `/health`, `/healthz` | JSON liveness |
| `/robots.txt`, `/sitemap.xml` | SEO |
| `/styles.css`, `/script.js`, `/favicon.svg` | Static (immutable cache) |
