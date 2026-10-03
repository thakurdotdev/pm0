"use client";

import Link from "next/link";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

interface Row {
  metric: string;
  pm2: string;
  pm0: string;
  delta: string;
  /** normalized share for bars, [pm2, pm0] in % of the pair max */
  bars?: [number, number];
  unit: string;
  note?: string;
}

/**
 * All numbers are verified benchmark results (site spec §1) — never invent.
 * TODO(DATA): align the benchmarked pm0 version with the actually-tested
 * release (see environment footnote).
 */
const ROWS: Row[] = [
  {
    metric: "Daemon idle RSS",
    pm2: "69 MB",
    pm0: "16 MB",
    delta: "−77%",
    bars: [100, 23],
    unit: "memory (lower is better)",
  },
  {
    metric: "list @ 500 apps",
    pm2: "245 ms",
    pm0: "38.5 ms",
    delta: "6.4× faster",
    bars: [100, 16],
    unit: "latency (lower is better)",
  },
  {
    metric: "Ecosystem start @ 500 apps",
    pm2: "1.97 s · 139 MB",
    pm0: "0.31 s · 78 MB",
    delta: "6.3× · −44% RSS",
    bars: [100, 16],
    unit: "wall clock (lower is better)",
  },
  {
    metric: "1,200 restart ops",
    pm2: "238 ms/op · 290 s",
    pm0: "6.5 ms/op · 9.3 s",
    delta: "37× faster",
    bars: [100, 3],
    unit: "per-op latency (lower is better)",
  },
  {
    metric: "Log flood @ 100 MB/s",
    pm2: "20.2% CPU",
    pm0: "5.7% CPU",
    delta: "3.6× less CPU",
    bars: [100, 28],
    unit: "CPU (lower is better)",
  },
  {
    metric: "Cluster -i 2 throughput",
    pm2: "25.8k req/s",
    pm0: "27.3k req/s",
    delta: "pm0 +6%",
    bars: [95, 100],
    unit: "throughput (higher is better)",
  },
  {
    metric: "Idle CPU @ 0 apps",
    pm2: "0.033%",
    pm0: "0.033%",
    delta: "parity",
    bars: [100, 100],
    unit: "CPU (lower is better)",
  },
];

export function Benchmarks() {
  return (
    <section id="benchmarks" aria-label="Benchmarks" className="scroll-mt-24">
      <Tabs defaultValue="visual">
        <div className="mb-10 flex flex-col gap-6 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <h2 className="heading-h2 text-ink">Measured, not marketed.</h2>
            <p className="mt-3 max-w-[520px] text-ink-muted">
              pm0 vs PM2 7.0.4 on identical workloads. 100-run averages, same
              machine, same scripts.
            </p>
          </div>
          <TabsList className="h-9 rounded-lg border border-line bg-surface p-1">
            <TabsTrigger
              value="visual"
              className="h-7 rounded-md px-3 text-xs text-ink-muted data-[state=active]:bg-elevated data-[state=active]:text-ink"
            >
              Visual
            </TabsTrigger>
            <TabsTrigger
              value="detailed"
              className="h-7 rounded-md px-3 text-xs text-ink-muted data-[state=active]:bg-elevated data-[state=active]:text-ink"
            >
              Detailed
            </TabsTrigger>
          </TabsList>
        </div>

      {/* Visual */}
      <TabsContent value="visual" className="mt-0">
        <div className="flex flex-col gap-7">
          {ROWS.map((row) => (
            <div key={row.metric}>
              <div className="mb-2 flex items-baseline justify-between gap-4">
                <p className="text-sm font-medium text-ink">{row.metric}</p>
                <p className="font-mono text-xs text-accent">{row.delta}</p>
              </div>
              {row.bars && (
                <div className="flex flex-col gap-1.5">
                  <Bar label="PM2" value={row.pm2} pct={row.bars[0]} />
                  <Bar label="pm0" value={row.pm0} pct={row.bars[1]} highlight />
                </div>
              )}
              <p className="label-caption mt-1.5">{row.unit}</p>
            </div>
          ))}
        </div>
      </TabsContent>

      {/* Detailed */}
      <TabsContent value="detailed" className="mt-0">
        <div className="overflow-x-auto rounded-xl border border-line">
          <table className="w-full min-w-[680px] border-collapse text-sm">
            <thead>
              <tr className="border-b border-line bg-surface text-left">
                <th scope="col" className="px-5 py-3.5 font-medium text-ink-muted">Metric</th>
                <th scope="col" className="px-5 py-3.5 font-medium text-ink-muted">PM2 7.0.4</th>
                <th scope="col" className="bg-accent-tint px-5 py-3.5 font-medium text-ink">pm0</th>
                <th scope="col" className="px-5 py-3.5 font-medium text-ink-muted">Delta</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {ROWS.map((row) => (
                <tr key={row.metric} className="transition-colors duration-150 hover:bg-elevated/60">
                  <td className="px-5 py-3.5 text-ink">{row.metric}</td>
                  <td className="px-5 py-3.5 font-mono text-[0.8125rem] text-ink-muted">{row.pm2}</td>
                  <td className="bg-accent-tint px-5 py-3.5 font-mono text-[0.8125rem] text-ink">{row.pm0}</td>
                  <td className="px-5 py-3.5 font-mono text-[0.8125rem] text-accent">{row.delta}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </TabsContent>

        <p className="label-caption mt-6">
          Environment: 2 vCPU / 3.9 GB Linux · PM2 7.0.4 vs pm0 · 100-run
          averages.{" "}
          <span className="font-mono">TODO(DATA): align pm0 version with the actually-benchmarked release.</span>{" "}
          <Link
            href="/docs/benchmarks"
            className="text-ink-muted underline underline-offset-4 hover:text-ink"
          >
            Methodology →
          </Link>
        </p>
      </Tabs>
    </section>
  );
}

function Bar({
  label,
  value,
  pct,
  highlight,
}: {
  label: string;
  value: string;
  pct: number;
  highlight?: boolean;
}) {
  return (
    <div className="flex items-center gap-3">
      <span className="w-9 shrink-0 font-mono text-[0.6875rem] text-ink-faint">
        {label}
      </span>
      <div className="h-5 flex-1 overflow-hidden rounded-md bg-elevated">
        <div
          className={`h-full rounded-md ${highlight ? "bg-accent" : "bg-ink-faint"}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span
        className={`w-44 shrink-0 text-right font-mono text-[0.8125rem] whitespace-nowrap ${
          highlight ? "text-ink" : "text-ink-muted"
        }`}
      >
        {value}
      </span>
    </div>
  );
}
