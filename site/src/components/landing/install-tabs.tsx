"use client";

import { useState } from "react";
import {
  CodeBlockTab,
  CodeBlockTabs,
  CodeBlockTabsList,
  CodeBlockTabsTrigger,
} from "fumadocs-ui/components/codeblock";
import { CodeLines } from "@/components/site/code-block";
import { CopyButton } from "@/components/site/copy-button";

const TABS = [
  {
    value: "curl",
    code: "$ curl -fsSL https://pm0.thakur.dev/install.sh | bash",
  },
  {
    value: "sudo",
    code: "$ curl -fsSL https://pm0.thakur.dev/install.sh | sudo bash",
  },
  {
    value: "binary",
    code: `$ curl -fsSL -o pm0 \\
  https://github.com/thakurdotdev/pm0/releases/latest/download/pm0-linux-amd64 \\
  && sudo install -m 0755 pm0 /usr/local/bin/pm0`,
  },
  {
    value: "source",
    code: `$ git clone https://github.com/thakurdotdev/pm0.git \\
  && cd pm0 \\
  && make build \\
  && sudo make install  # requires Go >= 1.26`,
  },
] as const;

/**
 * Install command card on the hero — uses fumadocs' CodeBlockTabs (same
 * component family as docs code blocks) rendered on the always-dark pm0
 * code surface: attached tab row with accent underline, one copy button
 * for the active tab, thin visible scrollbar for long commands.
 */
export function InstallTabs() {
  const [active, setActive] = useState<string>("curl");
  const activeCode =
    TABS.find((tab) => tab.value === active)?.code ?? TABS[0].code;

  return (
    <CodeBlockTabs
      value={active}
      onValueChange={setActive}
      className="overflow-hidden border-line-strong bg-code-bg shadow-sm"
    >
      <div className="flex items-stretch justify-between gap-2 border-b border-line bg-code-chrome pr-1.5">
        <CodeBlockTabsList className="px-1.5">
          {TABS.map((tab) => (
            <CodeBlockTabsTrigger
              key={tab.value}
              value={tab.value}
              className="font-mono text-[0.8125rem]"
            >
              {tab.value}
            </CodeBlockTabsTrigger>
          ))}
        </CodeBlockTabsList>
        <div className="flex items-center">
          <CopyButton
            text={activeCode.replace(/^\$ /gm, "")}
            label={`Copy ${active} install command`}
          />
        </div>
      </div>

      {TABS.map((tab) => (
        <CodeBlockTab
          key={tab.value}
          value={tab.value}
          className="code-scroll overflow-x-auto px-4 py-3.5"
        >
          <CodeLines code={tab.code} mode="command" />
        </CodeBlockTab>
      ))}
    </CodeBlockTabs>
  );
}
