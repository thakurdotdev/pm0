// @ts-nocheck
import * as __fd_glob_38 from "../content/docs/troubleshooting.mdx?collection=docs"
import * as __fd_glob_37 from "../content/docs/quickstart.mdx?collection=docs"
import * as __fd_glob_36 from "../content/docs/migrating-from-pm2.mdx?collection=docs"
import * as __fd_glob_35 from "../content/docs/introduction.mdx?collection=docs"
import * as __fd_glob_34 from "../content/docs/installation.mdx?collection=docs"
import * as __fd_glob_33 from "../content/docs/http-api/setup.mdx?collection=docs"
import * as __fd_glob_32 from "../content/docs/http-api/security.mdx?collection=docs"
import * as __fd_glob_31 from "../content/docs/http-api/integration.mdx?collection=docs"
import * as __fd_glob_30 from "../content/docs/http-api/index.mdx?collection=docs"
import * as __fd_glob_29 from "../content/docs/http-api/endpoints.mdx?collection=docs"
import * as __fd_glob_28 from "../content/docs/http-api/data-model.mdx?collection=docs"
import * as __fd_glob_27 from "../content/docs/guides/zero-downtime-deploys.mdx?collection=docs"
import * as __fd_glob_26 from "../content/docs/guides/systemd.mdx?collection=docs"
import * as __fd_glob_25 from "../content/docs/guides/docker.mdx?collection=docs"
import * as __fd_glob_24 from "../content/docs/guides/cicd.mdx?collection=docs"
import * as __fd_glob_23 from "../content/docs/faq.mdx?collection=docs"
import * as __fd_glob_22 from "../content/docs/ecosystem-file.mdx?collection=docs"
import * as __fd_glob_21 from "../content/docs/core-concepts.mdx?collection=docs"
import * as __fd_glob_20 from "../content/docs/cli/unstartup.mdx?collection=docs"
import * as __fd_glob_19 from "../content/docs/cli/stop.mdx?collection=docs"
import * as __fd_glob_18 from "../content/docs/cli/status.mdx?collection=docs"
import * as __fd_glob_17 from "../content/docs/cli/startup.mdx?collection=docs"
import * as __fd_glob_16 from "../content/docs/cli/start.mdx?collection=docs"
import * as __fd_glob_15 from "../content/docs/cli/scale.mdx?collection=docs"
import * as __fd_glob_14 from "../content/docs/cli/save.mdx?collection=docs"
import * as __fd_glob_13 from "../content/docs/cli/resurrect.mdx?collection=docs"
import * as __fd_glob_12 from "../content/docs/cli/restart.mdx?collection=docs"
import * as __fd_glob_11 from "../content/docs/cli/reload.mdx?collection=docs"
import * as __fd_glob_10 from "../content/docs/cli/ping.mdx?collection=docs"
import * as __fd_glob_9 from "../content/docs/cli/logs.mdx?collection=docs"
import * as __fd_glob_8 from "../content/docs/cli/kill.mdx?collection=docs"
import * as __fd_glob_7 from "../content/docs/cli/jlist.mdx?collection=docs"
import * as __fd_glob_6 from "../content/docs/cli/describe.mdx?collection=docs"
import * as __fd_glob_5 from "../content/docs/cli/delete.mdx?collection=docs"
import * as __fd_glob_4 from "../content/docs/benchmarks.mdx?collection=docs"
import { default as __fd_glob_3 } from "../content/docs/meta.json?collection=docs"
import { default as __fd_glob_2 } from "../content/docs/http-api/meta.json?collection=docs"
import { default as __fd_glob_1 } from "../content/docs/guides/meta.json?collection=docs"
import { default as __fd_glob_0 } from "../content/docs/cli/meta.json?collection=docs"
import { server } from 'fumadocs-mdx/runtime/server';
import type * as Config from '../source.config';

const create = server<typeof Config, import("fumadocs-mdx/runtime/types").InternalTypeConfig & {
  DocData: {
  }
}>();

export const docs = await create.docs("docs", "content/docs", {"cli/meta.json": __fd_glob_0, "guides/meta.json": __fd_glob_1, "http-api/meta.json": __fd_glob_2, "meta.json": __fd_glob_3, }, {"benchmarks.mdx": __fd_glob_4, "cli/delete.mdx": __fd_glob_5, "cli/describe.mdx": __fd_glob_6, "cli/jlist.mdx": __fd_glob_7, "cli/kill.mdx": __fd_glob_8, "cli/logs.mdx": __fd_glob_9, "cli/ping.mdx": __fd_glob_10, "cli/reload.mdx": __fd_glob_11, "cli/restart.mdx": __fd_glob_12, "cli/resurrect.mdx": __fd_glob_13, "cli/save.mdx": __fd_glob_14, "cli/scale.mdx": __fd_glob_15, "cli/start.mdx": __fd_glob_16, "cli/startup.mdx": __fd_glob_17, "cli/status.mdx": __fd_glob_18, "cli/stop.mdx": __fd_glob_19, "cli/unstartup.mdx": __fd_glob_20, "core-concepts.mdx": __fd_glob_21, "ecosystem-file.mdx": __fd_glob_22, "faq.mdx": __fd_glob_23, "guides/cicd.mdx": __fd_glob_24, "guides/docker.mdx": __fd_glob_25, "guides/systemd.mdx": __fd_glob_26, "guides/zero-downtime-deploys.mdx": __fd_glob_27, "http-api/data-model.mdx": __fd_glob_28, "http-api/endpoints.mdx": __fd_glob_29, "http-api/index.mdx": __fd_glob_30, "http-api/integration.mdx": __fd_glob_31, "http-api/security.mdx": __fd_glob_32, "http-api/setup.mdx": __fd_glob_33, "installation.mdx": __fd_glob_34, "introduction.mdx": __fd_glob_35, "migrating-from-pm2.mdx": __fd_glob_36, "quickstart.mdx": __fd_glob_37, "troubleshooting.mdx": __fd_glob_38, });