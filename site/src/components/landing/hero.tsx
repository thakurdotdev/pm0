import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  formatStars,
  getPm0Version,
  getRepoInfo,
  latestAssetUrl,
  REPO_URL,
} from "@/lib/github";
import { GitHubMark } from "@/components/site/github-mark";
import { InstallTabs } from "./install-tabs";
import { Terminal } from "./terminal";

/**
 * Split hero: copy + CTAs + install command on the left, the animated
 * pm0 terminal as the right-hand visual anchor. The terminal lives in
 * the hero (not a separate section below) so the first viewport shows
 * the product doing its job instead of a wall of centered text.
 */
export async function Hero() {
  const [version, repo] = await Promise.all([getPm0Version(), getRepoInfo()]);

  return (
    <section className="relative overflow-hidden">
      <div className="mx-auto grid max-w-[1120px] items-start gap-12 px-6 pb-16 pt-12 sm:pb-20 sm:pt-16 lg:min-h-[calc(100dvh-4rem)] lg:grid-cols-[minmax(0,1fr)_440px] lg:items-center lg:gap-14 lg:pb-16 xl:gap-20">
        {/* Left column: message, actions, install command */}
        <div className="flex min-w-0 flex-col items-start">
          <p className="inline-flex flex-wrap items-center gap-x-2.5 gap-y-1 rounded-full border border-line bg-surface px-3.5 py-1.5 text-[0.8125rem] text-ink-muted">
            <span className="inline-flex items-center gap-2 font-mono text-xs text-ink">
              <span className="size-1.5 rounded-full bg-online" aria-hidden="true" />
              v{version}
            </span>
            <span aria-hidden="true" className="text-ink-faint">
              ·
            </span>
            <span>Drop-in PM2 supervisor, written in Go</span>
          </p>

          <h1 className="heading-display mt-6 text-ink">
            Process management
            <br />
            without the overhead.
          </h1>

          <p className="mt-5 max-w-[540px] text-[1.0625rem] leading-[1.65] text-ink-muted">
            pm0 is a PM2-compatible supervisor in a single static Go binary.
            Same commands, same workflow — 77% less memory, sub-40ms CLI,
            kernel-level zero-downtime reloads.
          </p>

          <div className="mt-8 w-full max-w-[600px]">
            <InstallTabs />
          </div>

          <p className="label-caption mt-3.5">
            Auto-detects amd64 · arm64 · arm · 386 —{" "}
            <a
              href={latestAssetUrl("checksums.txt")}
              target="_blank"
              rel="noopener noreferrer"
              className="text-ink-muted underline underline-offset-4 hover:text-ink"
            >
              verify with checksums.txt
            </a>
            .
          </p>
        </div>

        {/* Right column: live terminal, vertically centered */}
        <div className="w-full min-w-0 max-w-[560px] lg:max-w-none">
          <Terminal className="shadow-md" />
        </div>
      </div>
    </section>
  );
}
