import { getPm0Version } from "@/lib/github";
import { LogoWordmark } from "@/components/site/logo";

/**
 * Docs sidebar title. fumadocs wraps `nav.title` in its own <Link>, so this
 * must NOT render an anchor (nested <a> causes a hydration error).
 */
export async function DocsNavTitle() {
  const version = await getPm0Version();
  return (
    <span className="inline-flex items-center gap-2.5 text-ink">
      <LogoWordmark />
      <span className="hidden items-center gap-1.5 rounded-full border border-line px-2 py-0.5 font-mono text-[0.6875rem] text-ink-muted sm:inline-flex">
        <span className="size-1.5 rounded-full bg-online" aria-hidden="true" />
        v{version}
      </span>
    </span>
  );
}
