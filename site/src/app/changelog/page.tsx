import Link from "next/link";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { getReleases, REPO_URL, FALLBACK_VERSION } from "@/lib/github";
import { SiteNav } from "@/components/site/site-nav";
import { SiteFooter } from "@/components/site/site-footer";

export const revalidate = 3600;

export const metadata = {
  title: "Changelog",
  description:
    "Every pm0 release — version notes, dates, and compare links, straight from GitHub Releases.",
};

function formatDate(iso: string | null): string {
  if (!iso) return "";
  return new Date(iso).toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

export default async function ChangelogPage() {
  const releases = await getReleases(20);

  return (
    <div className="flex min-h-screen flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-[100] focus:rounded-lg focus:bg-elevated focus:px-4 focus:py-2 focus:text-sm focus:text-ink"
      >
        Skip to content
      </a>
      <SiteNav />

      <main id="main" className="mx-auto w-full max-w-[760px] flex-1 px-6 py-16">
        <div className="mb-12">
          <h1 className="heading-h2 text-ink">Changelog</h1>
          <p className="mt-3 text-ink-muted">
            Release notes from{" "}
            <a
              href={`${REPO_URL}/releases`}
              target="_blank"
              rel="noopener noreferrer"
              className="text-ink underline underline-offset-4"
            >
              GitHub Releases
            </a>
          </p>
        </div>

        {releases.length === 0 ? (
          <div className="rounded-xl border border-line p-6">
            <p className="font-mono text-sm text-ink">
              v{FALLBACK_VERSION}
            </p>
            <p className="mt-2 text-sm text-ink-muted">
              GitHub Releases could not be reached (rate limit or network).
              View all releases directly on{" "}
              <a
                href={`${REPO_URL}/releases`}
                target="_blank"
                rel="noopener noreferrer"
                className="text-accent underline underline-offset-4"
              >
                GitHub
              </a>
              .
            </p>
          </div>
        ) : (
          <ol className="flex flex-col gap-12">
            {releases.map((release, i) => {
              const prev = releases[i + 1];
              return (
                <li key={release.tag_name} className="relative pl-6">
                  <span
                    className="absolute left-0 top-2 size-2 rounded-full bg-accent"
                    aria-hidden="true"
                  />
                  <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                    <h2 className="font-mono text-xl font-semibold tracking-tight text-ink">
                      {release.tag_name}
                    </h2>
                    <time className="label-caption" dateTime={release.published_at ?? undefined}>
                      {formatDate(release.published_at)}
                    </time>
                  </div>
                  <div className="mt-2 flex items-center gap-4">
                    <a
                      href={release.html_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-sm text-ink-muted underline-offset-4 hover:text-ink hover:underline"
                    >
                      Release on GitHub
                    </a>
                    {prev && (
                      <a
                        href={`${REPO_URL}/compare/${prev.tag_name}...${release.tag_name}`}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-sm text-ink-muted underline-offset-4 hover:text-ink hover:underline"
                      >
                        Compare {prev.tag_name}…{release.tag_name}
                      </a>
                    )}
                  </div>
                  <div className="release-md mt-4">
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>
                      {release.body ?? "_No release notes._"}
                    </ReactMarkdown>
                  </div>
                </li>
              );
            })}
          </ol>
        )}

        <p className="label-caption mt-16">
          <Link href="/docs/introduction" className="hover:text-ink-muted">
            ← Back to docs
          </Link>
        </p>
      </main>

      <SiteFooter />
    </div>
  );
}
