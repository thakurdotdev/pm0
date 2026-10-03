import type { MetadataRoute } from "next";
import { source } from "@/lib/source";

const SITE = "https://pm0.thakur.dev";

export default function sitemap(): MetadataRoute.Sitemap {
  const docPages = source.getPages().map((page) => ({
    url: `${SITE}/docs/${page.slugs.join("/") || ""}`.replace(/\/$/, ""),
    lastModified: new Date(),
    changeFrequency: "weekly" as const,
    priority: 0.7,
  }));

  return [
    { url: SITE, lastModified: new Date(), changeFrequency: "weekly", priority: 1 },
    { url: `${SITE}/docs`, lastModified: new Date(), changeFrequency: "weekly", priority: 0.9 },
    ...docPages,
    { url: `${SITE}/changelog`, lastModified: new Date(), changeFrequency: "daily", priority: 0.6 },
  ];
}
