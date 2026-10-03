import Link from "next/link";
import { getPm0Version, REPO_URL } from "@/lib/github";
import { ThemeToggle } from "./theme-toggle";

const COLUMNS: { title: string; links: { href: string; label: string; external?: boolean }[] }[] = [
  {
    title: "Product",
    links: [
      { href: "/#features", label: "Features" },
      { href: "/#benchmarks", label: "Benchmarks" },
      { href: "/changelog", label: "Changelog" },
    ],
  },
  {
    title: "Resources",
    links: [
      { href: "/docs", label: "Docs" },
      { href: "/docs/cli/start", label: "CLI Reference" },
      { href: "/docs/http-api", label: "HTTP API" },
      { href: "/install.sh", label: "install.sh" },
    ],
  },
  {
    title: "Community",
    links: [
      { href: REPO_URL, label: "GitHub", external: true },
      { href: `${REPO_URL}/issues`, label: "Issues", external: true },
      { href: `${REPO_URL}/releases`, label: "Releases", external: true },
    ],
  },
  {
    title: "Project",
    links: [
      { href: `${REPO_URL}/blob/main/LICENSE`, label: "MIT License", external: true },
      { href: "https://thakur.dev", label: "thakur.dev", external: true },
    ],
  },
];

export async function SiteFooter() {
  const version = await getPm0Version();

  return (
    <footer className="mt-auto border-t border-line">
      <div className="mx-auto max-w-[1120px] px-6 py-14">
        <div className="grid grid-cols-2 gap-10 sm:grid-cols-4">
          {COLUMNS.map((col) => (
            <div key={col.title}>
              <h3 className="mb-4 text-sm font-medium text-ink">{col.title}</h3>
              <ul className="flex flex-col gap-2.5">
                {col.links.map((link) => (
                  <li key={link.href + link.label}>
                    {link.external ? (
                      <a
                        href={link.href}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-sm text-ink-muted transition-colors hover:text-ink"
                      >
                        {link.label}
                      </a>
                    ) : (
                      <Link
                        href={link.href}
                        className="text-sm text-ink-muted transition-colors hover:text-ink"
                      >
                        {link.label}
                      </Link>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
        <div className="mt-12 flex flex-col gap-4 border-t border-line pt-6 sm:flex-row sm:items-center sm:justify-between">
          <p className="text-sm text-ink-faint">© 2026 pm0 contributors</p>
          <div className="flex items-center gap-4">
            <p className="font-mono text-xs text-ink-faint">
              MIT · v{version}
            </p>
            <span
              aria-hidden="true"
              className="hidden h-4 w-px bg-line-strong sm:block"
            />
            <ThemeToggle className="size-8" />
          </div>
        </div>
      </div>
    </footer>
  );
}
