import { CommandLine } from "@/components/site/code-block";

const STEPS = [
  {
    title: "Install",
    body: "One command. Auto-detects your architecture — amd64, arm64, arm, or 386.",
    code: "$ curl -fsSL https://pm0.thakur.dev/install.sh | bash",
  },
  {
    title: "Start an app in cluster mode",
    body: "Point pm0 at any Node entrypoint. -i max uses one worker per CPU core.",
    code: "$ pm0 start api.js -i max",
  },
  {
    title: "Manage it",
    body: "Check status, stream logs, reload with zero dropped connections.",
    code: "$ pm0 status\n$ pm0 reload api",
  },
];

export function Quickstart() {
  return (
    <section aria-label="Quickstart">
      <div className="mb-10">
        <h2 className="heading-h2 text-ink">Up and running in a minute</h2>
        <p className="mt-3 max-w-[560px] text-ink-muted">
          If you know pm2, you already know pm0.
        </p>
      </div>
      <ol className="grid grid-cols-1 gap-4 md:grid-cols-3">
        {STEPS.map((step, i) => (
          <li
            key={step.title}
            className="flex flex-col rounded-xl border border-line p-6 transition-colors duration-150 hover:border-line-strong"
          >
            <span className="font-mono text-xs text-accent">
              {String(i + 1).padStart(2, "0")}
            </span>
            <h3 className="mt-3 text-[0.9375rem] font-medium text-ink">
              {step.title}
            </h3>
            <p className="mt-2 text-sm leading-[1.625] text-ink-muted">
              {step.body}
            </p>
            <div className="mt-4">
              <CommandLine code={step.code} />
            </div>
          </li>
        ))}
      </ol>
      <p className="label-caption mt-4">
        Full walkthrough in the{" "}
        <a href="/docs/quickstart" className="text-ink-muted underline underline-offset-4 hover:text-ink">
          Quickstart docs →
        </a>
      </p>
    </section>
  );
}
