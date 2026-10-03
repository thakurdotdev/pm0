import Link from "next/link";
import {
  Boxes,
  Cable,
  FileCode2,
  FileDigit,
  Gauge,
  Radio,
  Repeat,
  ScrollText,
  ShieldCheck,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";

const FEATURES: {
  icon: React.ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  title: string;
  body: React.ReactNode;
  span?: boolean;
  badge?: string;
}[] = [
  {
    icon: Cable,
    title: "Unix socket IPC",
    body: (
      <>
        The CLI talks to the daemon over a Unix domain socket — 0.3ms
        roundtrip, no V8 warmup, no TCP bootstrap on every command.
      </>
    ),
    span: true,
  },
  {
    icon: FileDigit,
    title: "16 MB static ELF",
    body: <>One binary. No runtime, no node_modules, no interpreter.</>,
  },
  {
    icon: Gauge,
    title: "Sub-40ms CLI at scale",
    body: (
      <>
        <code className="font-mono">list</code> answers in 38.5ms with 500 apps
        under management.
      </>
    ),
  },
  {
    icon: Repeat,
    title: "SO_REUSEPORT rolling reload",
    badge: "Node.js only",
    body: (
      <>
        Kernel-level zero-downtime reloads. Requires Node ≥ 23.2 and kernel
        ≥ 3.9. Other runtimes run as fork families with no shared port.
      </>
    ),
  },
  {
    icon: ShieldCheck,
    title: "cgroup v2 kill paths",
    body: (
      <>
        Process tracking through cgroup v2 — SIGINT → kill_timeout → SIGKILL
        through the actor mailbox. Zero orphaned children.
      </>
    ),
  },
  {
    icon: Boxes,
    title: "Multi-runtime auto-detect",
    body: (
      <>
        Node, Bun, Deno, Python, Go, Rust, shell — detected from the script
        header. Compiled binaries run with{" "}
        <code className="font-mono">--interpreter none</code>.
      </>
    ),
    span: true,
  },
  {
    icon: ScrollText,
    title: "Built-in log rotation",
    body: (
      <>
        Size-based copytruncate ring buffers per process. No external log
        module to install or misconfigure.
      </>
    ),
  },
  {
    icon: FileCode2,
    title: "ecosystem.config.js drop-in",
    body: (
      <>
        Point pm0 at your existing PM2 ecosystem file.{" "}
        <code className="font-mono">--only</code> and{" "}
        <code className="font-mono">--env production</code> work as expected.
      </>
    ),
    span: true,
  },
  {
    icon: Radio,
    title: "Opt-in HTTP API",
    body: (
      <>
        REST + SSE streaming, off by default, loopback/Unix-socket only —{" "}
        <code className="font-mono">PM0_HTTP_ADDR</code>.{" "}
        <Link
          href="/docs/http-api"
          className="text-ink underline underline-offset-4 hover:text-ink"
        >
          Read the docs →
        </Link>
      </>
    ),
  },
];

export function Bento() {
  return (
    <section id="features" aria-label="Features" className="scroll-mt-24">
      <div className="mb-10">
        <h2 className="heading-h2 text-ink">What pm0 does differently</h2>
        <p className="mt-3 max-w-[560px] text-ink-muted">
          Same commands you already know. Different engineering underneath.
        </p>
      </div>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
        {FEATURES.map((f) => (
          <article
            key={f.title}
            className={`rounded-xl border border-line p-6 transition-colors duration-150 hover:border-line-strong ${
              f.span ? "lg:col-span-2" : ""
            }`}
          >
            <div className="flex items-center gap-3">
              <f.icon className="size-4 text-ink-faint" aria-hidden={true} />
              <h3 className="text-[0.9375rem] font-medium text-ink">{f.title}</h3>
              {f.badge && (
                <Badge
                  variant="outline"
                  className="border-line-strong px-2 py-0 font-mono text-[0.6875rem] font-normal text-warn"
                >
                  {f.badge}
                </Badge>
              )}
            </div>
            <p className="mt-3 text-sm leading-[1.625] text-ink-muted">{f.body}</p>
          </article>
        ))}
      </div>
    </section>
  );
}
