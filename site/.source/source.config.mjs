// source.config.ts
import { defineDocs, defineConfig } from "fumadocs-mdx/config";
var docs = defineDocs({ dir: "content/docs" });
var source_config_default = defineConfig({
  mdxOptions: {
    rehypePlugins: [
      // Dual theme (dark + light variant tokens are both emitted inline);
      // globals.css forces the dark palette so code stays dark in both themes.
    ]
  }
});
export {
  source_config_default as default,
  docs
};
