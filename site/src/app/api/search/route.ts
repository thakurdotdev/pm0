import { createFromSource } from "fumadocs-core/search/server";
import { source } from "@/lib/source";

// Static export: the search index is written to out/api/search at build time and
// searched in the browser (RootProvider search type "static").
export const revalidate = false;

export const { staticGET: GET } = createFromSource(source);
