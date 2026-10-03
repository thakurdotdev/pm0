import { createFromSource } from "fumadocs-core/search/server";
import { source } from "@/lib/source";

export const revalidate = 3600;

export const { GET } = createFromSource(source);
