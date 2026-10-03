// @ts-nocheck
import * as __fd_glob_32 from "../content/docs/troubleshooting.mdx?collection=docs"
import * as __fd_glob_31 from "../content/docs/quickstart.mdx?collection=docs"
import * as __fd_glob_30 from "../content/docs/migrating-from-pm2.mdx?collection=docs"
import * as __fd_glob_29 from "../content/docs/introduction.mdx?collection=docs"
import * as __fd_glob_28 from "../content/docs/installation.mdx?collection=docs"
import * as __fd_glob_27 from "../content/docs/http-api.mdx?collection=docs"
import * as __fd_glob_26 from "../content/docs/guides/zero-downtime-deploys.mdx?collection=docs"
import * as __fd_glob_25 from "../content/docs/guides/systemd.mdx?collection=docs"
import * as __fd_glob_24 from "../content/docs/guides/docker.mdx?collection=docs"
import * as __fd_glob_23 from "../content/docs/guides/cicd.mdx?collection=docs"
import * as __fd_glob_22 from "../content/docs/faq.mdx?collection=docs"
import * as __fd_glob_21 from "../content/docs/ecosystem-file.mdx?collection=docs"
import * as __fd_glob_20 from "../content/docs/core-concepts.mdx?collection=docs"
import * as __fd_glob_19 from "../content/docs/cli/unstartup.mdx?collection=docs"
import * as __fd_glob_18 from "../content/docs/cli/stop.mdx?collection=docs"
import * as __fd_glob_17 from "../content/docs/cli/status.mdx?collection=docs"
import * as __fd_glob_16 from "../content/docs/cli/startup.mdx?collection=docs"
import * as __fd_glob_15 from "../content/docs/cli/start.mdx?collection=docs"
import * as __fd_glob_14 from "../content/docs/cli/scale.mdx?collection=docs"
import * as __fd_glob_13 from "../content/docs/cli/save.mdx?collection=docs"
import * as __fd_glob_12 from "../content/docs/cli/resurrect.mdx?collection=docs"
import * as __fd_glob_11 from "../content/docs/cli/restart.mdx?collection=docs"
import * as __fd_glob_10 from "../content/docs/cli/reload.mdx?collection=docs"
import * as __fd_glob_9 from "../content/docs/cli/ping.mdx?collection=docs"
import * as __fd_glob_8 from "../content/docs/cli/logs.mdx?collection=docs"
import * as __fd_glob_7 from "../content/docs/cli/kill.mdx?collection=docs"
import * as __fd_glob_6 from "../content/docs/cli/jlist.mdx?collection=docs"
import * as __fd_glob_5 from "../content/docs/cli/describe.mdx?collection=docs"
import * as __fd_glob_4 from "../content/docs/cli/delete.mdx?collection=docs"
import * as __fd_glob_3 from "../content/docs/benchmarks.mdx?collection=docs"
import { default as __fd_glob_2 } from "../content/docs/meta.json?collection=docs"
import { default as __fd_glob_1 } from "../content/docs/guides/meta.json?collection=docs"
import { default as __fd_glob_0 } from "../content/docs/cli/meta.json?collection=docs"
import { server } from 'fumadocs-mdx/runtime/server';
import type * as Config from '../source.config';

const create = server<typeof Config, import("fumadocs-mdx/runtime/types").InternalTypeConfig & {
  DocData: {
  }
}>();

export const docs = await create.docs("docs", "content/docs", {"cli/meta.json": __fd_glob_0, "guides/meta.json": __fd_glob_1, "meta.json": __fd_glob_2, }, {"benchmarks.mdx": __fd_glob_3, "cli/delete.mdx": __fd_glob_4, "cli/describe.mdx": __fd_glob_5, "cli/jlist.mdx": __fd_glob_6, "cli/kill.mdx": __fd_glob_7, "cli/logs.mdx": __fd_glob_8, "cli/ping.mdx": __fd_glob_9, "cli/reload.mdx": __fd_glob_10, "cli/restart.mdx": __fd_glob_11, "cli/resurrect.mdx": __fd_glob_12, "cli/save.mdx": __fd_glob_13, "cli/scale.mdx": __fd_glob_14, "cli/start.mdx": __fd_glob_15, "cli/startup.mdx": __fd_glob_16, "cli/status.mdx": __fd_glob_17, "cli/stop.mdx": __fd_glob_18, "cli/unstartup.mdx": __fd_glob_19, "core-concepts.mdx": __fd_glob_20, "ecosystem-file.mdx": __fd_glob_21, "faq.mdx": __fd_glob_22, "guides/cicd.mdx": __fd_glob_23, "guides/docker.mdx": __fd_glob_24, "guides/systemd.mdx": __fd_glob_25, "guides/zero-downtime-deploys.mdx": __fd_glob_26, "http-api.mdx": __fd_glob_27, "installation.mdx": __fd_glob_28, "introduction.mdx": __fd_glob_29, "migrating-from-pm2.mdx": __fd_glob_30, "quickstart.mdx": __fd_glob_31, "troubleshooting.mdx": __fd_glob_32, });