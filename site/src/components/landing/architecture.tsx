/**
 * Bespoke architecture diagram (hand-authored SVG, not an icon grid).
 *
 * Flow: CLI → unix socket → daemon → workers; TCP fan-in via SO_REUSEPORT.
 * Monochrome strokes via currentColor; red (--accent) only on the socket and
 * active paths. Theme-aware through CSS variables (§4).
 */
export function Architecture() {
  const workers = [
    { y: 52, label: "worker 0", sub: "online · pid 7731", active: true },
    { y: 162, label: "worker 1", sub: "online", active: false },
    { y: 272, label: "worker 2", sub: "online", active: false },
  ];

  return (
    <svg
      viewBox="0 0 880 430"
      role="img"
      aria-label="pm0 architecture: the CLI talks to the daemon over a Unix socket; the daemon supervises worker processes; inbound TCP fans out to workers via SO_REUSEPORT (Node.js only)"
      className="w-full"
    >
      <defs>
        <marker
          id="arch-arrow"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="5.5"
          markerHeight="5.5"
          orient="auto-start-reverse"
        >
          <path d="M 0 1 L 9 5 L 0 9" fill="none" stroke="currentColor" strokeWidth="1.5" />
        </marker>
        <marker
          id="arch-arrow-accent"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="5.5"
          markerHeight="5.5"
          orient="auto-start-reverse"
        >
          <path d="M 0 1 L 9 5 L 0 9" fill="none" stroke="var(--accent)" strokeWidth="1.5" />
        </marker>
      </defs>

      {/* ── CLI box ─────────────────────────────── */}
      <g>
        <rect
          x="20"
          y="162"
          width="150"
          height="66"
          rx="8"
          fill="var(--surface)"
          stroke="currentColor"
          strokeOpacity="0.35"
        />
        <text x="95" y="190" textAnchor="middle" fill="currentColor" fontSize="13" fontWeight="600" fontFamily="var(--font-mono)">
          pm0 CLI
        </text>
        <text x="95" y="209" textAnchor="middle" fill="currentColor" fillOpacity="0.55" fontSize="9.5" fontFamily="var(--font-mono)">
          status · logs · scale
        </text>
      </g>

      {/* ── unix socket (accent hot path) ───────── */}
      <g>
        <line
          x1="170"
          y1="195"
          x2="322"
          y2="195"
          stroke="var(--accent)"
          strokeWidth="1.8"
          markerEnd="url(#arch-arrow-accent)"
          markerStart="url(#arch-arrow-accent)"
        />
        <text x="246" y="176" textAnchor="middle" fill="var(--accent)" fontSize="11" fontFamily="var(--font-mono)">
          unix socket
        </text>
        <text x="246" y="218" textAnchor="middle" fill="currentColor" fillOpacity="0.45" fontSize="10" fontFamily="var(--font-mono)">
          0.3 ms roundtrip
        </text>
      </g>

      {/* ── daemon box ──────────────────────────── */}
      <g>
        <rect
          x="326"
          y="135"
          width="196"
          height="120"
          rx="8"
          fill="var(--surface)"
          stroke="currentColor"
          strokeOpacity="0.5"
        />
        <circle cx="504" cy="153" r="3" fill="var(--online)" />
        <text x="424" y="172" textAnchor="middle" fill="currentColor" fontSize="13" fontWeight="600" fontFamily="var(--font-mono)">
          pm0 daemon
        </text>
        <text x="424" y="194" textAnchor="middle" fill="currentColor" fillOpacity="0.55" fontSize="10" fontFamily="var(--font-mono)">
          actor mailbox
        </text>
        <text x="424" y="212" textAnchor="middle" fill="currentColor" fillOpacity="0.55" fontSize="10" fontFamily="var(--font-mono)">
          cgroup v2 tracking
        </text>
        <text x="424" y="230" textAnchor="middle" fill="currentColor" fillOpacity="0.55" fontSize="10" fontFamily="var(--font-mono)">
          ring-buffer logs
        </text>
      </g>

      {/* ── workers ─────────────────────────────── */}
      {workers.map((w) => (
        <g key={w.label}>
          <rect
            x="632"
            y={w.y}
            width="180"
            height="58"
            rx="8"
            fill="var(--surface)"
            stroke={w.active ? "var(--accent)" : "currentColor"}
            strokeOpacity={w.active ? 0.9 : 0.35}
          />
          <circle cx="650" cy={w.y + 18} r="3" fill="var(--online)" />
          <text
            x="662"
            y={w.y + 22}
            fill="currentColor"
            fontSize="11.5"
            fontWeight="600"
            fontFamily="var(--font-mono)"
          >
            api · {w.label}
          </text>
          <text x="650" y={w.y + 41} fill="currentColor" fillOpacity="0.45" fontSize="9.5" fontFamily="var(--font-mono)">
            {w.sub}
          </text>
        </g>
      ))}

      {/* daemon → workers supervision lines (monochrome) */}
      <line x1="522" y1="160" x2="626" y2="81" stroke="currentColor" strokeOpacity="0.3" markerEnd="url(#arch-arrow)" />
      <line x1="522" y1="195" x2="626" y2="191" stroke="currentColor" strokeOpacity="0.3" markerEnd="url(#arch-arrow)" />
      <line x1="522" y1="230" x2="626" y2="301" stroke="currentColor" strokeOpacity="0.3" markerEnd="url(#arch-arrow)" />

      {/* ── TCP fan-in rail (SO_REUSEPORT) ──────── */}
      <g>
        <text x="838" y="24" textAnchor="end" fill="currentColor" fillOpacity="0.55" fontSize="10" fontFamily="var(--font-mono)">
          TCP :8080
        </text>
        <line x1="848" y1="34" x2="848" y2="382" stroke="currentColor" strokeOpacity="0.4" strokeWidth="1.2" />
        {workers.map((w) => (
          <line
            key={w.label}
            x1="848"
            y1={w.y + 29}
            x2="816"
            y2={w.y + 29}
            stroke="currentColor"
            strokeOpacity="0.4"
            strokeWidth="1.2"
            markerEnd="url(#arch-arrow)"
          />
        ))}
      </g>

      {/* ── footnote under workers ──────────────── */}
      <text x="722" y="368" textAnchor="middle" fill="currentColor" fillOpacity="0.55" fontSize="10" fontFamily="var(--font-mono)">
        TCP fan-in via SO_REUSEPORT
      </text>
      <text x="722" y="386" textAnchor="middle" fill="currentColor" fillOpacity="0.35" fontSize="10" fontFamily="var(--font-mono)">
        Node.js only · Node ≥ 23.2 · kernel ≥ 3.9
      </text>

      {/* ── legend ──────────────────────────────── */}
      <g fontFamily="var(--font-mono)" fontSize="10">
        <line x1="20" y1="392" x2="44" y2="392" stroke="var(--accent)" strokeWidth="1.8" />
        <text x="52" y="396" fill="currentColor" fillOpacity="0.55">IPC / hot path</text>
        <line x1="20" y1="414" x2="44" y2="414" stroke="currentColor" strokeOpacity="0.4" strokeWidth="1.2" />
        <text x="52" y="418" fill="currentColor" fillOpacity="0.55">supervision / traffic</text>
      </g>
    </svg>
  );
}
