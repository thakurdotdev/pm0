import { ImageResponse } from "next/og";

export const alt = "pm0 — Process management without the overhead";
export const size = { width: 1200, height: 630 };
export const contentType = "image/png";

/**
 * OG image — ALWAYS dark regardless of visitor theme (OG images must not vary).
 */
export default function OgImage() {
  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          flexDirection: "column",
          justifyContent: "space-between",
          backgroundColor: "#09090b",
          color: "#fafafa",
          padding: 72,
          fontFamily: "monospace",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 16 }}>
          <svg width="44" height="44" viewBox="0 0 24 24" fill="none">
            <rect x="2.75" y="2.75" width="18.5" height="18.5" rx="5.5" stroke="#fafafa" strokeWidth="1.8" />
            <circle cx="12" cy="12" r="6.25" stroke="#ef4444" strokeWidth="1.4" opacity="0.4" />
            <circle cx="12" cy="12" r="3.25" fill="#ef4444" />
          </svg>
          <span style={{ fontSize: 34, fontWeight: 600 }}>pm0</span>
          <span
            style={{
              display: "flex",
              alignItems: "center",
              gap: 8,
              marginLeft: 12,
              fontSize: 20,
              color: "#a1a1aa",
              border: "1px solid rgba(255,255,255,0.14)",
              borderRadius: 999,
              padding: "6px 16px",
            }}
          >
            <span style={{ width: 8, height: 8, borderRadius: 999, backgroundColor: "#22c55e" }} />
            v0.1.6
          </span>
        </div>

        <div style={{ display: "flex", flexDirection: "column", gap: 24 }}>
          <div style={{ fontSize: 72, fontWeight: 600, letterSpacing: -3, lineHeight: 1.1 }}>
            Process management
          </div>
          <div style={{ fontSize: 72, fontWeight: 600, letterSpacing: -3, lineHeight: 1.1 }}>
            without the overhead.
          </div>
          <div style={{ fontSize: 28, color: "#a1a1aa", lineHeight: 1.5, maxWidth: 900 }}>
            Drop-in PM2-compatible supervisor in a single static Go binary.
            77% less memory · sub-40ms CLI · zero dependencies.
          </div>
        </div>

        <div
          style={{
            display: "flex",
            justifyContent: "space-between",
            color: "#71717a",
            fontSize: 20,
          }}
        >
          <span>pm0.thakur.dev</span>
          <span>MIT · github.com/thakurdotdev/pm0</span>
        </div>
      </div>
    ),
    size,
  );
}
