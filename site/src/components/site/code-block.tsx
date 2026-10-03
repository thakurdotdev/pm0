import { CopyButton } from "./copy-button";

/**
 * Terminal-style code lines shared by landing code surfaces (§4: code stays
 * dark in both themes). The parent owns horizontal scrolling via the
 * `code-scroll` viewport; this <pre> sizes to content (`w-max`) the same way
 * fumadocs' Pre does, so long commands scroll instead of clipping.
 *
 * mode "terminal" (default): "$ "-prefixed lines get the red prompt and bright
 * ink; everything else renders as muted program output.
 * mode "command": every line is command text (bright ink) — used for install
 * cards where continuation lines belong to the command, not its output.
 */
export function CodeLines({
  code,
  className,
  mode = "terminal",
}: {
  code: string;
  className?: string;
  mode?: "terminal" | "command";
}) {
  const lines = code.replace(/\n$/, "").split("\n");
  return (
    <pre
      className={`min-w-full w-max text-left font-mono text-[0.8125rem] leading-[1.7] ${className ?? ""}`}
    >
      {lines.map((line, i) => {
        const isCmd = line.startsWith("$ ");
        if (isCmd || mode === "command") {
          return (
            <div key={i} className="whitespace-pre">
              {isCmd && <span className="text-accent">$</span>}
              <span className="text-code-ink">
                {isCmd ? ` ${line.slice(2)}` : line}
              </span>
            </div>
          );
        }
        return (
          <div key={i} className="whitespace-pre">
            <span className="text-code-ink-muted">{line || " "}</span>
          </div>
        );
      })}
    </pre>
  );
}

/** Floating copy button used on headerless code cards (fumadocs-style). */
function FloatingCopy({ text }: { text: string }) {
  return (
    <CopyButton
      text={text}
      className="absolute right-2 top-2 z-10 rounded-lg bg-code-bg/80 backdrop-blur-sm"
    />
  );
}

/**
 * Labeled code card matching the fumadocs CodeBlock design: rounded-xl,
 * hairline border, soft shadow, thin overlay scrollbar on an always-dark
 * surface.
 */
export function CodeBlock({
  label,
  rightLabel,
  code,
  className,
}: {
  label?: string;
  rightLabel?: string;
  code: string;
  className?: string;
}) {
  return (
    <figure
      className={`relative overflow-hidden rounded-xl border border-line-strong bg-code-bg shadow-sm ${className ?? ""}`}
    >
      {label && (
        <figcaption className="flex h-10 items-center justify-between border-b border-line bg-code-chrome px-4">
          <span className="font-mono text-xs text-code-ink-muted">{label}</span>
          <span className="flex items-center gap-3">
            {rightLabel && (
              <span className="font-mono text-xs text-code-ink-faint">{rightLabel}</span>
            )}
            <CopyButton text={code.replace(/^\$ /gm, "")} />
          </span>
        </figcaption>
      )}
      <div className="code-scroll overflow-x-auto px-4 py-3.5">
        <CodeLines code={code} />
      </div>
      {!label && <FloatingCopy text={code.replace(/^\$ /gm, "")} />}
    </figure>
  );
}

/** Single-line (or few-line) copyable command chip for install snippets. */
export function CommandLine({ code, className }: { code: string; className?: string }) {
  return (
    <figure
      className={`relative overflow-hidden rounded-xl border border-line-strong bg-code-bg shadow-sm ${className ?? ""}`}
    >
      <div className="code-scroll overflow-x-auto py-3 pl-4 pr-12">
        <CodeLines code={code} />
      </div>
      <FloatingCopy text={code.replace(/^\$ /gm, "")} />
    </figure>
  );
}
