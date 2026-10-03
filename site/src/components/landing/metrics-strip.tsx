import Link from "next/link";

const METRICS = [
  { value: "16 MB", label: "daemon RSS", delta: "−77% vs PM2" },
  { value: "38.5 ms", label: "CLI at 500 apps", delta: "6.4× faster" },
  { value: "0", label: "dependencies", delta: "static Go binary" },
];

export function MetricsStrip() {
  return (
    <section aria-label="Key metrics" className="border-y border-line bg-surface">
      <div className="mx-auto grid max-w-[1120px] grid-cols-1 divide-y divide-line px-6 sm:grid-cols-3 sm:divide-x sm:divide-y-0">
        {METRICS.map((m) => (
          <div key={m.value} className="flex items-baseline justify-between gap-4 py-6 sm:flex-col sm:justify-start sm:gap-1 sm:px-8 sm:py-7">
            <p className="text-[1.75rem] font-semibold tracking-tight text-ink">
              {m.value}
            </p>
            <div className="sm:flex sm:items-baseline sm:gap-2">
              <p className="text-sm text-ink-muted">{m.label}</p>
              <p className="font-mono text-xs text-accent">{m.delta}</p>
            </div>
          </div>
        ))}
      </div>
      <p className="mx-auto max-w-[1120px] px-6 pb-4 text-right">
        <Link
          href="/docs/benchmarks"
          className="text-xs text-ink-faint underline-offset-4 hover:text-ink-muted hover:underline"
        >
          Full benchmark methodology →
        </Link>
      </p>
    </section>
  );
}
