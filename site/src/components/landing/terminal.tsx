"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { RotateCcw } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * TODO(DATA): The outputs below are placeholders — paste the exact real
 * `pm0 start/status/reload` output from pm0 v0.1.6 before shipping.
 * Lines must stay ≤ ~49 chars so the demo never scrolls inside the 440px
 * hero column.
 */
type Step =
  | { kind: "cmd"; text: string }
  | { kind: "out"; text: string }
  | { kind: "pause"; ms: number };

const TRANSCRIPT: Step[] = [
  { kind: "cmd", text: "pm0 start api.js -i max" },
  {
    kind: "out",
    text: "[pm0] api started — 2 instances, cluster mode",
  },
  { kind: "cmd", text: "pm0 status" },
  {
    kind: "out",
    text: "name │ id │ mode    │ uptime │ status",
  },
  {
    kind: "out",
    text: "api  │ 0  │ cluster │ 12s    │ online",
  },
  {
    kind: "out",
    text: "api  │ 1  │ cluster │ 12s    │ online",
  },
  { kind: "cmd", text: "pm0 reload api" },
  {
    kind: "out",
    text: "[pm0] reload complete — zero dropped connections",
  },
  {
    kind: "out",
    text: "TODO(DATA): replace with exact real reload output from pm0 v0.1.6",
  },
  { kind: "pause", ms: 4200 },
];

const TYPE_SPEED_MS = 34;
const LINE_DELAY_MS = 130;
const PAUSE_BEFORE_CMD = 550;

function prefersReducedMotion(): boolean {
  if (typeof window === "undefined") return true;
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/** Renders output lines; highlights the `online` status in green. */
function OutputLine({ text }: { text: string }) {
  if (text.startsWith("TODO(DATA)")) {
    return <div className="whitespace-pre text-code-ink-faint">{text}</div>;
  }
  const idx = text.indexOf("online");
  if (idx === -1) {
    return <div className="whitespace-pre text-code-ink-muted">{text}</div>;
  }
  return (
    <div className="whitespace-pre text-code-ink-muted">
      {text.slice(0, idx)}
      <span className="text-online">{text.slice(idx, idx + "online".length)}</span>
      {text.slice(idx + "online".length)}
    </div>
  );
}

export function Terminal({ className }: { className?: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const [typed, setTyped] = useState<string | null>(null); // chars typed of current cmd
  const [idlePrompt, setIdlePrompt] = useState(true);
  const [replayable, setReplayable] = useState(false);
  const [cycle, setCycle] = useState(0);
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);

  const clearTimers = useCallback(() => {
    timers.current.forEach(clearTimeout);
    timers.current = [];
  }, []);

  const schedule = useCallback((fn: () => void, ms: number) => {
    timers.current.push(setTimeout(fn, ms));
  }, []);

  const replay = useCallback(() => {
    clearTimers();
    setLines([]);
    setTyped(null);
    setReplayable(false);
    setCycle((c) => c + 1);
  }, [clearTimers]);

  useEffect(() => {
    if (prefersReducedMotion()) {
      // Static fallback: full transcript, no animation, no timers.
      setLines(
        TRANSCRIPT.filter((s) => s.kind !== "pause").map((s) =>
          s.kind === "cmd" ? `$ ${s.text}` : s.text,
        ),
      );
      setTyped(null);
      setIdlePrompt(false);
      setReplayable(false);
      return;
    }

    let cancelled = false;
    clearTimers();
    setLines([]);
    setTyped(null);
    setIdlePrompt(true);

    let t = 300;
    for (const step of TRANSCRIPT) {
      if (step.kind === "pause") {
        t += step.ms;
      } else if (step.kind === "out") {
        const at = t;
        schedule(() => {
          if (!cancelled)
            setLines((prev) =>
              prev.includes(step.text) ? prev : [...prev, step.text],
            );
        }, at);
        t += LINE_DELAY_MS;
      } else {
        // cmd: show prompt, type chars one by one, commit when done
        const cmdAt = t;
        const chars = step.text;
        schedule(() => {
          if (!cancelled) {
            setIdlePrompt(false);
            setTyped("");
          }
        }, cmdAt);
        for (let i = 1; i <= chars.length; i++) {
          schedule(() => {
            if (!cancelled) setTyped(chars.slice(0, i));
          }, cmdAt + i * TYPE_SPEED_MS);
        }
        t += chars.length * TYPE_SPEED_MS + PAUSE_BEFORE_CMD;
      }
    }

    schedule(() => {
      if (!cancelled) {
        setTyped(null);
        setIdlePrompt(true);
        setReplayable(true);
      }
    }, t);

    // Loop: restart after the trailing pause
    schedule(() => {
      if (!cancelled) setCycle((c) => c + 1);
    }, t + 4200);

    return () => {
      cancelled = true;
      clearTimers();
    };
  }, [cycle]);

  const doneCommands = lines.filter((l) => l.startsWith("$ ")).length;

  return (
    <figure
      className={cn(
        "relative overflow-hidden rounded-xl border border-line-strong bg-code-bg shadow-sm",
        className,
      )}
      aria-label="pm0 terminal demo"
    >
      <figcaption className="flex h-10 items-center justify-between border-b border-line bg-code-chrome px-4">
        <span className="font-mono text-xs text-code-ink-muted">pm0 — zsh</span>
        <span className="flex items-center gap-3">
          <span className="font-mono text-xs text-code-ink-faint">live demo</span>
          <button
            type="button"
            onClick={replay}
            aria-label="Replay terminal demo"
            className="inline-flex size-7 items-center justify-center rounded-md text-code-ink-faint transition-colors hover:text-code-ink"
          >
            <RotateCcw className="size-3.5" aria-hidden="true" />
          </button>
        </span>
      </figcaption>

      <div className="code-scroll min-h-[248px] overflow-x-auto px-4 py-3.5 font-mono text-[0.8125rem] leading-[1.7]">
        {lines.map((line, i) =>
          line.startsWith("$ ") ? (
            <div key={`cmd-${i}-${doneCommands}`} className="whitespace-pre">
              <span className="text-accent">$</span>
              <span className="text-code-ink">{` ${line.slice(2)}`}</span>
            </div>
          ) : (
            <OutputLine key={`out-${i}`} text={line} />
          ),
        )}
        {typed !== null ? (
          <div className="whitespace-pre">
            <span className="text-accent">$</span>
            <span className="text-code-ink">{" "}{typed}</span>
            <span className="text-code-ink-muted" aria-hidden="true">▍</span>
          </div>
        ) : idlePrompt ? (
          <div className="whitespace-pre">
            <span className="text-accent">$</span>
            <span className="text-code-ink-muted" aria-hidden="true">▍</span>
          </div>
        ) : null}
      </div>

      {/* Screen-reader friendly static transcript */}
      <pre className="sr-only">
        {"$ pm0 start api.js -i max\n$ pm0 status\n$ pm0 reload api"}
      </pre>
    </figure>
  );
}
