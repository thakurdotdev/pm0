import { source } from "@/lib/source";
import {
  DocsPage,
  DocsBody,
  DocsTitle,
  DocsDescription,
} from "fumadocs-ui/page";
import {
  MarkdownCopyButton,
  ViewOptionsPopover,
} from "fumadocs-ui/layouts/docs/page";
import { getPageMarkdownUrl } from "@/lib/shared";
import { notFound, redirect } from "next/navigation";
import { getMDXComponents } from "@/mdx-components";

export default async function Page(props: {
  params: Promise<{ slug?: string[] }>;
}) {
  const params = await props.params;
  if (!params.slug) redirect("/docs/introduction");
  const page = source.getPage(params.slug);
  if (!page) notFound();

  const markdownUrl = getPageMarkdownUrl(page).url;
  const MDXContent = page.data.body;

  return (
    <DocsPage
      toc={page.data.toc}
      editOnGithub={{
        owner: "thakurdotdev",
        repo: "pm0",
        sha: "main",
        path: `site/content/docs/${page.path}`,
      }}
    >
      <DocsTitle className="text-3xl font-semibold tracking-tight">
        {page.data.title}
      </DocsTitle>
      {page.data.description ? (
        <DocsDescription className="mb-4 text-[1.0625rem] leading-relaxed">
          {page.data.description}
        </DocsDescription>
      ) : null}
      <div className="flex flex-row flex-wrap items-center gap-2 border-b border-fd-border pb-6 mb-6">
        <MarkdownCopyButton markdownUrl={markdownUrl} />
        <ViewOptionsPopover
          markdownUrl={markdownUrl}
          githubUrl={`https://github.com/thakurdotdev/pm0/blob/main/site/content/docs/${page.path}`}
        />
      </div>
      <DocsBody>
        <MDXContent components={getMDXComponents()} />
      </DocsBody>
    </DocsPage>
  );
}

export function generateStaticParams() {
  return source.generateParams();
}

export async function generateMetadata(props: {
  params: Promise<{ slug?: string[] }>;
}) {
  const params = await props.params;
  const page = source.getPage(params.slug);
  if (!page) notFound();

  return {
    title: page.data.title,
    description: page.data.description,
  };
}
