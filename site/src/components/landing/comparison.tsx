import { Check, Minus, X } from "lucide-react";

type Cell = { yes: true; text?: string } | { yes: false; text: string } | { text: string; partial?: boolean };

const ROWS: { feature: string; pm0: Cell; pm2: Cell }[] = [
  {
    feature: "Memory (daemon idle)",
    pm0: { yes: true, text: "16 MB" },
    pm2: { yes: false, text: "69 MB" },
  },
  {
    feature: "Runtime",
    pm0: { yes: true, text: "Single static Go binary" },
    pm2: { yes: false, text: "Node.js runtime required" },
  },
  {
    feature: "Zero-downtime reload",
    pm0: {
      yes: false,
      text: "SO_REUSEPORT cluster — Node.js only (≥ 23.2)",
    },
    pm2: { yes: true, text: "Cluster mode, older Node versions" },
  },
  {
    feature: "Other runtimes (Bun, Deno, Python, Go, Rust, shell)",
    pm0: { yes: true, text: "Fork families, auto-detected" },
    pm2: { text: "Fork mode only", partial: true },
  },
  {
    feature: "Kill semantics",
    pm0: { yes: true, text: "SIGINT → kill_timeout → SIGKILL via actor mailbox" },
    pm2: { yes: true, text: "SIGINT → kill_timeout → SIGKILL" },
  },
  {
    feature: "Log rotation",
    pm0: { yes: true, text: "Built-in size-based copytruncate ring buffers" },
    pm2: { yes: false, text: "External module (pm2-logrotate)" },
  },
  {
    feature: "HTTP API",
    pm0: { yes: true, text: "Opt-in, off by default, loopback/Unix-socket only" },
    pm2: { yes: false, text: "On by default once launched (port 9615)" },
  },
  {
    feature: "Stop/restart/delete --parallel",
    pm0: { yes: true, text: "Optional — trades staggered downtime for speed" },
    pm2: { yes: true, text: "Parallel by default on many ops" },
  },
  {
    feature: "reload sequencing",
    pm0: { yes: true, text: "Always sequential by design (zero-downtime + rollback)" },
    pm2: { yes: true, text: "Sequential with graceful timeout" },
  },
  {
    feature: "Process tracking",
    pm0: { yes: true, text: "cgroup v2 — no orphaned children" },
    pm2: { yes: false, text: "PID polling; orphans possible" },
  },
];

/**
 * TODO(DATA): The final row must be filled with the real, honest gap list
 * (verify against the pm0 README "differences" section before shipping).
 */
const NOT_YET = [
  "Windows and macOS (pm0 is Linux-native; PM2 runs everywhere Node runs)",
  "Module ecosystem (pm2-logrotate, pm2-server-monit, pm2.io integration)",
  "Older Node.js versions for cluster mode (pm0 needs Node ≥ 23.2 for SO_REUSEPORT reload)",
  "Long-term production track record — pm0 is at v0.1.6",
];

function CellContent({ cell }: { cell: Cell }) {
  if ("yes" in cell && cell.yes) {
    return (
      <span className="flex items-start gap-2 text-ink">
        <Check className="mt-0.5 size-3.5 shrink-0 text-online" aria-hidden="true" />
        <span className="text-[0.8125rem]">{cell.text ?? "Yes"}</span>
      </span>
    );
  }
  if ("partial" in cell && cell.partial) {
    return (
      <span className="flex items-start gap-2 text-ink-muted">
        <Minus className="mt-0.5 size-3.5 shrink-0 text-warn" aria-hidden="true" />
        <span className="text-[0.8125rem]">{cell.text}</span>
      </span>
    );
  }
  return (
    <span className="flex items-start gap-2 text-ink-faint">
      <X className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
      <span className="text-[0.8125rem]">{cell.text}</span>
    </span>
  );
}

export function Comparison() {
  return (
    <section aria-label="pm0 vs PM2 comparison">
      <div className="mb-10">
        <h2 className="heading-h2 text-ink">Honest comparison</h2>
        <p className="mt-3 max-w-[560px] text-ink-muted">
          Where pm0 wins, where it matches, and what PM2 still does better.
        </p>
      </div>

      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="w-full min-w-[720px] border-collapse text-sm">
          <thead>
            <tr className="border-b border-line bg-surface text-left">
              <th scope="col" className="px-5 py-3.5 font-medium text-ink-muted">
                Feature
              </th>
              <th scope="col" className="bg-accent-tint px-5 py-3.5 font-medium text-ink">
                pm0
              </th>
              <th scope="col" className="px-5 py-3.5 font-medium text-ink-muted">
                PM2 7.0.4
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {ROWS.map((row) => (
              <tr
                key={row.feature}
                className="transition-colors duration-150 hover:bg-elevated/60"
              >
                <td className="px-5 py-3.5 text-ink">{row.feature}</td>
                <td className="bg-accent-tint px-5 py-3.5">
                  <CellContent cell={row.pm0} />
                </td>
                <td className="px-5 py-3.5">
                  <CellContent cell={row.pm2} />
                </td>
              </tr>
            ))}
            <tr>
              <td className="px-5 py-3.5 align-top font-medium text-ink">
                What PM2 has that pm0 doesn't yet
              </td>
              <td colSpan={2} className="px-5 py-3.5 align-top">
                <ul className="flex flex-col gap-1.5 text-[0.8125rem] text-ink-muted">
                  {NOT_YET.map((item) => (
                    <li key={item} className="flex gap-2">
                      <span className="text-ink-faint" aria-hidden="true">·</span>
                      {item}
                    </li>
                  ))}
                  <li className="mt-1 font-mono text-[0.6875rem] text-ink-faint">
                    TODO(DATA): verify this list against the pm0 README before
                    shipping.
                  </li>
                </ul>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  );
}
