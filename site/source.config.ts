import { defineDocs, defineConfig } from "fumadocs-mdx/config";

export const docs = defineDocs({ dir: "content/docs" });

export default defineConfig({
  mdxOptions: {

    rehypePlugins: [
      // Dual theme (dark + light variant tokens are both emitted inline);
      // globals.css forces the dark palette so code stays dark in both themes.
    ],
  },
});
