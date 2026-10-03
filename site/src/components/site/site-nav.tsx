import Link from "next/link";
import { Button } from "@/components/ui/button";
import { getPm0Version, getRepoInfo, formatStars, REPO_URL } from "@/lib/github";
import { LogoWordmark } from "./logo";
import { MobileNav } from "./mobile-nav";
import { GitHubMark } from "./github-mark";

const LINKS = [
  { href: "/#features", label: "Features" },
  { href: "/#benchmarks", label: "Benchmarks" },
  { href: "/docs", label: "Docs" },
  { href: "/changelog", label: "Changelog" },
];

export async function SiteNav() {
  const [version, repo] = await Promise.all([getPm0Version(), getRepoInfo()]);

  return (
    <header className="sticky top-0 z-50 border-b border-line bg-background/80 backdrop-blur">
      <div className="mx-auto flex h-16 max-w-[1120px] items-center gap-6 px-6">
        <Link href="/" aria-label="pm0 home" className="text-ink">
          <LogoWordmark />
        </Link>

        <Link
          href="/changelog"
          className="hidden items-center gap-1.5 rounded-full border border-line px-2.5 py-1 font-mono text-xs text-ink-muted transition-colors hover:border-line-strong hover:text-ink sm:inline-flex"
          aria-label={`Latest version v${version}, view changelog`}
        >
          <span className="size-1.5 rounded-full bg-online" aria-hidden="true" />
          v{version}
        </Link>

        <nav aria-label="Main navigation" className="ml-2 hidden items-center gap-6 md:flex">
          {LINKS.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              className="text-sm text-ink-muted transition-colors hover:text-ink"
            >
              {link.label}
            </Link>
          ))}
        </nav>

        <div className="ml-auto flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            asChild
            className="hidden gap-1.5 text-ink-muted hover:bg-elevated hover:text-ink sm:inline-flex"
          >
            <a
              href={repo?.html_url ?? REPO_URL}
              target="_blank"
              rel="noopener noreferrer"
              aria-label="pm0 on GitHub"
            >
              <GitHubMark className="size-[1.1rem]" aria-hidden="true" />
              {repo && repo.stargazers_count > 0 && (
                <span className="font-mono text-xs">{formatStars(repo.stargazers_count)}</span>
              )}
            </a>
          </Button>
          <Button
            size="sm"
            asChild
            className="rounded-lg bg-primary font-medium text-primary-foreground hover:bg-accent-hover"
          >
            <Link href="/docs">Get Started</Link>
          </Button>
          <MobileNav />
        </div>
      </div>
    </header>
  );
}
