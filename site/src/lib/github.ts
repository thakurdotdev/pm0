import { unstable_cache } from "next/cache";

/**
 * ─────────────────────────────────────────────────────────────────────────
 * SINGLE SOURCE OF TRUTH for the pm0 version string.
 *
 * RULE (site spec §2): the version appears in exactly ONE module — this one.
 * Nav badge, hero pill, footer, /changelog and install snippets all consume
 * `getLatestRelease()` / `getPm0Version()`. NEVER hardcode a version in JSX.
 *
 * Download URLs are version-independent:
 *   https://github.com/thakurdotdev/pm0/releases/latest/download/{asset}
 * ─────────────────────────────────────────────────────────────────────────
 */

export const REPO_OWNER = "thakurdotdev";
export const REPO_NAME = "pm0";
export const REPO_URL = `https://github.com/${REPO_OWNER}/${REPO_NAME}`;

/** Used only when the GitHub API is unreachable / rate-limited (ISR 1h). */
export const FALLBACK_VERSION = "0.1.6";

const API_BASE = `https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}`;
const FETCH_TIMEOUT_MS = 4000;

export interface Pm0Release {
  /** e.g. "v0.1.6" */
  tag_name: string;
  /** e.g. "0.1.6" */
  version: string;
  published_at: string | null;
  html_url: string;
  body: string | null;
  assets: Pm0Asset[];
}

export interface Pm0Asset {
  name: string;
  size: number;
  download_url: string;
  browser_download_url: string;
}

export interface RepoInfo {
  stargazers_count: number;
  html_url: string;
}

function stripLeadingV(tag: string): string {
  return tag.startsWith("v") ? tag.slice(1) : tag;
}

function normalizeRelease(raw: {
  tag_name?: string;
  published_at?: string | null;
  html_url?: string;
  body?: string | null;
  assets?: Array<{ name?: string; size?: number; browser_download_url?: string }>;
}): Pm0Release | null {
  if (!raw?.tag_name) return null;
  return {
    tag_name: raw.tag_name,
    version: stripLeadingV(raw.tag_name),
    published_at: raw.published_at ?? null,
    html_url: raw.html_url ?? `${REPO_URL}/releases/latest`,
    body: raw.body ?? null,
    assets: (raw.assets ?? [])
      .filter((a) => a.name)
      .map((a) => ({
        name: a.name as string,
        size: a.size ?? 0,
        download_url:
          a.browser_download_url ??
          `https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${a.name}`,
        browser_download_url:
          a.browser_download_url ??
          `https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${a.name}`,
      })),
  };
}

async function ghFetch<T>(path: string): Promise<T | null> {
  try {
    const res = await fetch(`${API_BASE}${path}`, {
      headers: {
        Accept: "application/vnd.github+json",
        "User-Agent": "pm0-site",
        // A GITHUB_TOKEN env var (optional) avoids low anonymous rate limits.
        ...(process.env.GITHUB_TOKEN
          ? { Authorization: `Bearer ${process.env.GITHUB_TOKEN}` }
          : {}),
      },
      signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
      // unstable_cache handles server-side caching; disable Next fetch cache.
      cache: "no-store",
    });
    if (!res.ok) {
      console.warn(`[github] ${path} responded ${res.status}`);
      return null;
    }
    return (await res.json()) as T;
  } catch (err) {
    console.warn(`[github] ${path} failed:`, err instanceof Error ? err.message : err);
    return null;
  }
}

/** Latest release — cached for 1 hour (ISR-style via unstable_cache). */
export const getLatestRelease = unstable_cache(
  async (): Promise<Pm0Release | null> => {
    const raw = await ghFetch<Record<string, unknown>>("/releases/latest");
    const release = raw ? normalizeRelease(raw) : null;
    if (!release) {
      console.warn(
        `[github] falling back to FALLBACK_VERSION=${FALLBACK_VERSION}`,
      );
    }
    return release;
  },
  ["pm0-latest-release"],
  { revalidate: 3600 },
);

/** Release list for /changelog — cached for 1 hour. */
export const getReleases = unstable_cache(
  async (perPage = 20): Promise<Pm0Release[]> => {
    const raw = await ghFetch<Array<Record<string, unknown>>>(
      `/releases?per_page=${perPage}`,
    );
    if (!raw) {
      console.warn(`[github] falling back to FALLBACK release ${FALLBACK_VERSION}`);
      return [];
    }
    return raw
      .map(normalizeRelease)
      .filter((r): r is Pm0Release => r !== null);
  },
  ["pm0-releases"],
  { revalidate: 3600 },
);

/** Repo metadata (star count for the nav button) — cached for 1 hour. */
export const getRepoInfo = unstable_cache(
  async (): Promise<RepoInfo | null> => {
    return await ghFetch<RepoInfo>("");
  },
  ["pm0-repo-info"],
  { revalidate: 3600 },
);

/**
 * The display version. Consumers: nav badge, hero pill, footer, download URLs,
 * /changelog. Returns FALLBACK_VERSION when the API is unavailable.
 */
export async function getPm0Version(): Promise<string> {
  const release = await getLatestRelease();
  return release?.version ?? FALLBACK_VERSION;
}

/** Version-independent asset download URL (always points at `latest`). */
export function latestAssetUrl(asset: string): string {
  return `https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${asset}`;
}

export function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return "—";
  if (bytes < 1024) return `${bytes} B`;
  const kb = bytes / 1024;
  if (kb < 1024) return `${kb.toFixed(1)} KB`;
  const mb = kb / 1024;
  return `${mb.toFixed(2)} MB`;
}

export function formatStars(count: number): string {
  if (count >= 1000) {
    const k = count / 1000;
    return `${k >= 10 ? Math.round(k) : Math.round(k * 10) / 10}k`;
  }
  return String(count);
}
