/**
 * TODO(DATA): Replace with the real pm0 logo SVG once available.
 * This placeholder is theme-safe: strokes use currentColor, the accent ring
 * and status dot use var(--accent), so it renders correctly in light + dark.
 */
export function LogoMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      className={className}
      aria-hidden="true"
    >
      <rect
        x="2.75"
        y="2.75"
        width="18.5"
        height="18.5"
        rx="5.5"
        stroke="currentColor"
        strokeWidth="1.8"
      />
      <circle cx="12" cy="12" r="6.25" stroke="var(--accent)" strokeWidth="1.4" opacity="0.4" />
      <circle cx="12" cy="12" r="3.25" fill="var(--accent)" />
    </svg>
  );
}

export function LogoWordmark({ className }: { className?: string }) {
  return (
    <span className={`inline-flex items-center gap-2.5 ${className ?? ""}`}>
      <LogoMark className="size-6" />
      <span className="text-[1.0625rem] font-semibold tracking-tight">pm0</span>
    </span>
  );
}
