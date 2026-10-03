import { DocsNavTitle } from "@/components/docs/nav-title";
import { GitHubMark } from "@/components/site/github-mark";
import { source } from "@/lib/source";
import { DocsLayout } from "fumadocs-ui/layouts/docs";

export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <DocsLayout
      tree={source.pageTree}
      nav={{
        title: <DocsNavTitle />,
      }}
      links={[
        {
          type: "icon",
          label: "GitHub",
          url: "https://github.com/thakurdotdev/pm0",
          icon: <GitHubMark className="size-[1.1rem]" aria-hidden="true" />,
          text: "GitHub",
          external: true,
        },
      ]}
    >
      {children}
    </DocsLayout>
  );
}
