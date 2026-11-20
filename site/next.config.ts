import { createMDX } from "fumadocs-mdx/next";
import type { NextConfig } from "next";

// Static export: `next build` writes the whole site to out/, served straight from Nginx.
// No runtime server, so no headers()/rewrites()/middleware. The `/docs/<slug>.md`
// copies are written by scripts/postbuild.ts.
const nextConfig: NextConfig = {
  output: "export",
  reactStrictMode: false,
  images: { unoptimized: true },
  typescript: {
    ignoreBuildErrors: true,
  },
};

export default createMDX()(nextConfig);
