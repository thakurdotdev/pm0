import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { source } from "@/lib/source";
import { DocsNavTitle } from "@/components/docs/nav-title";

export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <DocsLayout
      tree={source.pageTree}
      nav={{
        title: <DocsNavTitle />,
        // No theme toggle in the docs header / sidebar top bar — fumadocs'
        // built-in switch in the sidebar footer is the single docs control.
      }}
    >
      {children}
    </DocsLayout>
  );
}
