// source.config.ts
import { defineDocs, defineConfig } from "fumadocs-mdx/config";
var docs = defineDocs({ dir: "content/docs" });
var source_config_default = defineConfig({
  mdxOptions: {
    rehypeCodeOptions: {
      // Dual theme (dark + light variant tokens are both emitted inline);
      // globals.css forces the dark palette so code stays dark in both themes.
      keepBackground: false
    }
  }
});
export {
  source_config_default as default,
  docs
};
