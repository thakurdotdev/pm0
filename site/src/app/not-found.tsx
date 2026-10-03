import Link from "next/link";

export const metadata = {
  title: "404 — process not found",
};

export default function NotFound() {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background px-6 text-center">
      <p className="font-mono text-sm text-ink-muted">
        <span className="text-accent">$</span> pm0 status <span className="text-ink-faint">— 404</span>
      </p>
      <h1 className="mt-5 font-mono text-2xl font-semibold tracking-tight text-ink sm:text-3xl">
        pm0: process not found <span className="text-ink-faint">(exit code 127)</span>
      </h1>
      <p className="mt-4 max-w-[440px] text-[0.9375rem] leading-[1.625] text-ink-muted">
        The page you requested isn't running under this supervisor. Check the
        docs, or go back to the homepage.
      </p>
      <div className="mt-8 flex items-center gap-3">
        <Link
          href="/"
          className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-accent-hover"
        >
          Go home
        </Link>
        <Link
          href="/docs"
          className="rounded-lg border border-line px-4 py-2 text-sm text-ink-muted transition-colors hover:border-line-strong hover:text-ink"
        >
          Read the docs
        </Link>
      </div>
    </div>
  );
}
