import { SiteNav } from "@/components/site/site-nav";
import { SiteFooter } from "@/components/site/site-footer";
import { Hero } from "@/components/landing/hero";
import { MetricsStrip } from "@/components/landing/metrics-strip";
import { Bento } from "@/components/landing/bento";
import { Architecture } from "@/components/landing/architecture";
import { Benchmarks } from "@/components/landing/benchmarks";
import { Comparison } from "@/components/landing/comparison";
import { Quickstart } from "@/components/landing/quickstart";
import { Reveal } from "@/components/landing/reveal";

export default function Home() {
  return (
    <div className="flex min-h-screen flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-[100] focus:rounded-lg focus:bg-elevated focus:px-4 focus:py-2 focus:text-sm focus:text-ink"
      >
        Skip to content
      </a>
      <SiteNav />

      <main id="main">
        <Hero />

        <MetricsStrip />

        <div className="mx-auto flex max-w-[1120px] flex-col gap-24 px-6 py-24">
          <Reveal>
            <Bento />
          </Reveal>

          <Reveal>
            <section aria-label="Architecture">
              <div className="mb-10">
                <h2 className="heading-h2 text-ink">Under the hood</h2>
                <p className="mt-3 max-w-[560px] text-ink-muted">
                  A single Go daemon supervising workers through the kernel —
                  not a JS event loop supervising children.
                </p>
              </div>
              <div className="rounded-xl border border-line p-6 text-ink sm:p-10">
                <Architecture />
              </div>
            </section>
          </Reveal>

          <Reveal>
            <Benchmarks />
          </Reveal>

          <Reveal>
            <Comparison />
          </Reveal>

          <Reveal>
            <Quickstart />
          </Reveal>
        </div>
      </main>

      <SiteFooter />
    </div>
  );
}
