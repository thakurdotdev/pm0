// pm0 — Interactive Client Logic
document.addEventListener("DOMContentLoaded", () => {
  // 1. Copy to Clipboard handler with feedback
  document.addEventListener("click", async (e) => {
    const btn = e.target.closest("[data-copy]");
    if (!btn) return;
    const targetId = btn.getAttribute("data-copy");
    const targetEl = document.getElementById(targetId);
    if (!targetEl) return;

    const textToCopy = targetEl.getAttribute("data-value") || targetEl.textContent.trim();
    try {
      await navigator.clipboard.writeText(textToCopy);
      const originalHtml = btn.innerHTML;
      btn.classList.add("copied");
      btn.innerHTML = `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg> <span>Copied</span>`;
      setTimeout(() => {
        btn.classList.remove("copied");
        btn.innerHTML = originalHtml;
      }, 2000);
    } catch {
      const range = document.createRange();
      range.selectNodeContents(targetEl);
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
    }
  });

  // 2. Hero Terminal Preview Tabs
  const termTabs = document.querySelectorAll(".term-tab-btn");
  const termPanels = document.querySelectorAll(".terminal-panel");

  termTabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      const targetId = tab.getAttribute("data-term");
      termTabs.forEach((t) => t.classList.remove("active"));
      termPanels.forEach((p) => {
        p.classList.remove("active");
        p.style.display = "none";
      });

      tab.classList.add("active");
      const activePanel = document.getElementById(`panel-${targetId}`);
      if (activePanel) {
        activePanel.classList.add("active");
        activePanel.style.display = "block";
      }
    });
  });

  // 3. Benchmark Switcher (Cards vs Table)
  const benchBtns = document.querySelectorAll(".bench-btn");
  const benchCards = document.getElementById("bench-cards-view");
  const benchTable = document.getElementById("bench-table-view");

  benchBtns.forEach((btn) => {
    btn.addEventListener("click", () => {
      benchBtns.forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      const view = btn.getAttribute("data-view");
      if (view === "cards") {
        if (benchCards) {
          benchCards.classList.add("active");
          benchCards.style.display = "grid";
        }
        if (benchTable) {
          benchTable.classList.remove("active");
          benchTable.style.display = "none";
        }
      } else {
        if (benchCards) {
          benchCards.classList.remove("active");
          benchCards.style.display = "none";
        }
        if (benchTable) {
          benchTable.classList.add("active");
          benchTable.style.display = "block";
        }
      }
    });
  });

  // 4. Quickstart Tabs Switcher
  const qsButtons = document.querySelectorAll(".qs-menu-btn");
  const qsPanes = document.querySelectorAll(".qs-tab-content");

  qsButtons.forEach((btn) => {
    btn.addEventListener("click", () => {
      qsButtons.forEach((b) => b.classList.remove("active"));
      qsPanes.forEach((p) => {
        p.classList.remove("active");
        p.style.display = "none";
      });

      btn.classList.add("active");
      const target = btn.getAttribute("data-qs");
      const activePane = document.getElementById(`qs-${target}`);
      if (activePane) {
        activePane.classList.add("active");
        activePane.style.display = "block";
      }
    });
  });

  // 5. Dynamic Version & Docs Install Commands
  let liveVersion = (window.__PM0_VERSION__ || "0.1.3").replace(/^v/, "");

  function getDocCommand(type, ver) {
    const tag = `v${ver}`;
    switch (type) {
      case "curl-user":
        return "curl -fsSL https://pm0.thakur.dev/install.sh | bash";
      case "curl-root":
        return "curl -fsSL https://pm0.thakur.dev/install.sh | sudo bash";
      case "version":
        return `curl -fsSL https://pm0.thakur.dev/install.sh | PM0_VERSION=${tag} bash`;
      case "binary":
        return `curl -fsSL https://github.com/thakurdotdev/pm0/releases/download/${tag}/pm0-linux-amd64 -o /usr/local/bin/pm0 && chmod +x /usr/local/bin/pm0`;
      default:
        return "curl -fsSL https://pm0.thakur.dev/install.sh | bash";
    }
  }

  const docInstTabs = document.querySelectorAll(".docs-inst-tab");
  const docInstCmd = document.getElementById("doc-install-cmd");

  function refreshDocCmd(key) {
    if (!docInstCmd) return;
    const cmd = getDocCommand(key, liveVersion);
    docInstCmd.textContent = cmd;
    docInstCmd.setAttribute("data-value", cmd);
  }

  docInstTabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      docInstTabs.forEach((t) => t.classList.remove("active"));
      tab.classList.add("active");
      const key = tab.getAttribute("data-inst") || "curl-user";
      refreshDocCmd(key);
    });
  });

  // Dynamic version synchronization (syncs package.json / GitHub releases)
  async function syncVersion() {
    let ver = liveVersion;

    try {
      const res = await fetch("/api/version");
      if (res.ok) {
        const data = await res.json();
        if (data?.version) ver = data.version.replace(/^v/, "");
      }
    } catch {
      try {
        const gh = await fetch("https://api.github.com/repos/thakurdotdev/pm0/releases/latest");
        if (gh.ok) {
          const d = await gh.json();
          if (d?.tag_name) ver = d.tag_name.replace(/^v/, "");
        }
      } catch {}
    }

    liveVersion = ver;
    const tag = `v${ver}`;

    document.querySelectorAll("[data-pm0-version]").forEach((el) => {
      el.textContent = tag;
    });
    document.querySelectorAll("[data-pm0-live-pill]").forEach((el) => {
      el.textContent = `${tag} is live — Drop-in PM2 supervisor in Go`;
    });
    document.querySelectorAll("[data-pm0-table-header]").forEach((el) => {
      el.textContent = `pm0 ${tag}`;
    });
    document.querySelectorAll("[data-pm0-footer-version]").forEach((el) => {
      el.textContent = `${tag} · MIT Licensed`;
    });

    const activeTab = document.querySelector(".docs-inst-tab.active");
    if (activeTab) {
      refreshDocCmd(activeTab.getAttribute("data-inst") || "curl-user");
    }
  }

  syncVersion();
});
