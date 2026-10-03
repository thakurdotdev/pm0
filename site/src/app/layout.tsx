import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { RootProvider } from "fumadocs-ui/provider/next";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

const SITE_URL = "https://pm0.thakur.dev";

export const metadata: Metadata = {
  metadataBase: new URL(SITE_URL),
  title: {
    default: "pm0 — Process management without the overhead",
    template: "%s — pm0",
  },
  description:
    "pm0 is a drop-in PM2-compatible process supervisor written in Go. Single static binary, zero dependencies, 77% less memory, sub-40ms CLI, kernel-level zero-downtime reloads.",
  keywords: [
    "pm0",
    "pm2",
    "process manager",
    "process supervisor",
    "golang",
    "zero-downtime",
    "cluster mode",
  ],
  authors: [{ name: "pm0 contributors" }],
  icons: { icon: "/icon.svg" },
  openGraph: {
    type: "website",
    url: SITE_URL,
    siteName: "pm0",
    title: "pm0 — Process management without the overhead",
    description:
      "Drop-in PM2-compatible supervisor in a single static Go binary. 77% less memory, sub-40ms CLI at 500 apps, zero dependencies.",
  },
  twitter: {
    card: "summary_large_image",
    title: "pm0 — Process management without the overhead",
    description:
      "Drop-in PM2-compatible supervisor in a single static Go binary.",
  },
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: dark)", color: "#09090b" },
    { media: "(prefers-color-scheme: light)", color: "#fafafa" },
  ],
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      suppressHydrationWarning
      className={`${geistSans.variable} ${geistMono.variable}`}
    >
      <body className="min-h-screen bg-background font-sans text-foreground antialiased">
        <RootProvider
          theme={{
            attribute: "class",
            defaultTheme: "dark",
            enableSystem: true,
            disableTransitionOnChange: true,
          }}
        >
          {children}
        </RootProvider>
      </body>
    </html>
  );
}
