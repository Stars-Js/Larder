/* Larder 前端单页应用（无框架）。 */
(() => {
  "use strict";

  // ---------------- 基础工具 ----------------
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
  const view = $("#view");
  const navEl = $("#nav");
  const toasts = $("#toasts");

  const esc = (s) =>
    String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  async function api(path, opts = {}) {
    const res = await fetch(path, opts);
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = text; }
    if (!res.ok) {
      const msg = (data && data.error) || (typeof data === "string" ? data : `HTTP ${res.status}`);
      const err = new Error(msg);
      err.status = res.status;
      err.data = data;
      throw err;
    }
    return data;
  }

  function toast(msg, kind = "") {
    const t = document.createElement("div");
    t.className = "toast " + kind;
    t.textContent = msg;
    toasts.appendChild(t);
    setTimeout(() => { t.style.opacity = "0"; t.style.transition = "opacity .3s"; }, 3800);
    setTimeout(() => t.remove(), 4300);
  }

  function modal({ title, sub, body, actions, wide, xwide }) {
    const mask = document.createElement("div");
    mask.className = "modal-mask";
    mask.innerHTML = `<div class="modal ${wide ? "lg" : ""} ${xwide ? "xl" : ""}">
      <div class="m-head">
        <div><h3>${esc(title)}</h3>${sub ? `<p class="m-sub">${esc(sub)}</p>` : ""}</div>
        <button class="close-x" data-act="close" aria-label="关闭">✕</button>
      </div>
      <div class="m-body">${body || ""}</div>
      <div class="m-actions">${actions || `<button class="btn" data-act="close">关闭</button>`}</div>
    </div>`;
    document.body.appendChild(mask);
    const close = () => mask.remove();
    mask.addEventListener("click", (e) => { if (e.target === mask) close(); });
    $$("[data-act=close]", mask).forEach((b) => (b.onclick = close));
    const onKey = (e) => { if (e.key === "Escape") { close(); document.removeEventListener("keydown", onKey); } };
    document.addEventListener("keydown", onKey);
    return { mask, close, el: mask.querySelector(".modal") };
  }

  function setConn(ok, text, kind) {
    const dot = $("#statusDot");
    if (dot) dot.className = "dot " + (kind || (ok ? "ok" : "bad"));
    const tx = $("#statusText");
    if (tx) tx.textContent = text;
  }

  function setSideStatus(kind, text) {
    const dot = $("#sideDot");
    if (dot) dot.className = "dot " + (kind || "off");
    const tx = $("#sideMcpText");
    if (tx) tx.textContent = text;
  }

  function initSidebar() {
    const app = $("#app");
    if (!app) return;
    const collapse = $("#sideCollapse");
    const toggle = $("#sideToggle");
    const scrim = $("#sideScrim");
    const saved = localStorage.getItem("larder.sidebar") === "collapsed";
    app.classList.toggle("side-collapsed", saved);
    if (collapse) {
      collapse.onclick = () => {
        app.classList.toggle("side-collapsed");
        localStorage.setItem("larder.sidebar", app.classList.contains("side-collapsed") ? "collapsed" : "expanded");
      };
    }
    const open = () => { app.classList.add("side-open"); if (scrim) scrim.hidden = false; };
    const close = () => { app.classList.remove("side-open"); if (scrim) scrim.hidden = true; };
    if (toggle) toggle.onclick = () => (app.classList.contains("side-open") ? close() : open());
    if (scrim) scrim.onclick = close;
    if (navEl) navEl.querySelectorAll("a").forEach((a) => a.addEventListener("click", close));
    window.addEventListener("resize", () => { if (window.innerWidth > 900) close(); });
  }

  function initTheme() {
    const btn = $("#themeToggle");
    const root = document.documentElement;
    const meta = document.querySelector('meta[name="theme-color"]');
    const apply = (theme, persist) => {
      root.dataset.theme = theme;
      if (persist) {
        try { localStorage.setItem("larder.theme", theme); } catch {}
      }
      if (meta) meta.content = theme === "dark" ? "#17141f" : "#f8f5fc";
      // 原生窗口标题栏跟随应用主题；普通浏览器里失败可忽略。
      fetch("/api/system/window-theme", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ theme }),
      }).catch(() => {});
      if (btn) {
        const dark = theme === "dark";
        btn.title = dark ? "切换到亮色模式" : "切换到暗色模式";
        btn.setAttribute("aria-label", btn.title);
        btn.setAttribute("aria-pressed", dark ? "true" : "false");
      }
    };
    apply(root.dataset.theme === "dark" ? "dark" : "light", false);
    if (btn) {
      btn.onclick = () => apply(root.dataset.theme === "dark" ? "light" : "dark", true);
    }
    const media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
    if (media && media.addEventListener) {
      media.addEventListener("change", (e) => {
        let saved = null;
        try { saved = localStorage.getItem("larder.theme"); } catch {}
        if (!saved) apply(e.matches ? "dark" : "light", false);
      });
    }
  }

  function fmtTime(s) {
    if (!s) return "";
    try {
      const d = new Date(s);
      if (isNaN(d.getTime())) return "";
      const pad = (n) => String(n).padStart(2, "0");
      return `${d.getMonth() + 1}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    } catch { return ""; }
  }

  function chip(text, kind, dot) {
    return `<span class="chip ${kind || ""}">${dot ? `<i class="dot"></i>` : ""}${esc(text)}</span>`;
  }

  // ---------------- 自定义下拉菜单 ----------------
  // 原生 select 保留取值与 change 事件，只把弹层换成与界面一致的自定义菜单。
  let cselect = null;

  function cselectClose() {
    if (!cselect) return;
    cselect.menu.remove();
    cselect.select.setAttribute("aria-expanded", "false");
    cselect = null;
    document.removeEventListener("pointerdown", cselectOutside, true);
    document.removeEventListener("keydown", cselectKeys, true);
    window.removeEventListener("resize", cselectClose);
    window.removeEventListener("scroll", cselectClose);
  }

  function cselectOutside(e) {
    if (cselect && !cselect.menu.contains(e.target)) cselectClose();
  }

  function cselectActive(items, idx) {
    items.forEach((el, i) => el.classList.toggle("active", i === idx));
    if (items[idx]) items[idx].scrollIntoView({ block: "nearest" });
  }

  function cselectKeys(e) {
    if (!cselect) return;
    const items = $$(".cselect-opt", cselect.menu);
    const idx = items.findIndex((el) => el.classList.contains("active"));
    if (e.key === "Escape") {
      e.preventDefault();
      const select = cselect.select;
      cselectClose();
      select.focus();
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const next = e.key === "ArrowDown" ? Math.min(items.length - 1, idx + 1) : Math.max(0, idx - 1);
      cselectActive(items, next);
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      if (items[idx]) items[idx].click();
    } else if (e.key === "Tab") {
      cselectClose();
    }
  }

  function cselectOpen(select) {
    if (!select || select.disabled) return;
    cselectClose();
    const rect = select.getBoundingClientRect();
    const width = Math.min(Math.max(rect.width, 240), 420);
    const menu = document.createElement("div");
    menu.className = "cselect-menu";
    menu.setAttribute("role", "listbox");
    menu.style.width = width + "px";
    menu.style.left = Math.max(8, Math.min(rect.left, window.innerWidth - width - 10)) + "px";
    const roomBelow = window.innerHeight - rect.bottom;
    const roomAbove = rect.top;
    // 优先向下展开；下方不足 160px 且上方更宽裕时才向上。
    const openUp = roomBelow < 160 && roomAbove > roomBelow;
    const available = Math.max(96, (openUp ? roomAbove : roomBelow) - 12);
    menu.style.maxHeight = Math.min(320, available) + "px";
    menu.style.top = (rect.bottom + 6) + "px";
    menu.innerHTML = Array.from(select.options).map((o, i) => `
      <button type="button" class="cselect-opt ${o.value === select.value ? "selected" : ""}" data-i="${i}" role="option" aria-selected="${o.value === select.value}">
        <span class="cselect-label">${esc(o.textContent)}</span><span class="cselect-check">✓</span>
      </button>`).join("");
    document.body.appendChild(menu);
    if (openUp) menu.style.top = Math.max(8, rect.top - menu.offsetHeight - 6) + "px";
    cselect = { select, menu };
    select.setAttribute("aria-expanded", "true");
    $$(".cselect-opt", menu).forEach((el) => {
      el.onmouseenter = () => cselectActive($$(".cselect-opt", menu), Number(el.dataset.i));
      el.onclick = () => {
        const opt = select.options[Number(el.dataset.i)];
        if (opt) {
          select.value = opt.value;
          select.dispatchEvent(new Event("change", { bubbles: true }));
        }
        cselectClose();
        select.focus();
      };
    });
    const selected = menu.querySelector(".selected") || menu.querySelector(".cselect-opt");
    if (selected) cselectActive($$(".cselect-opt", menu), Number(selected.dataset.i));
    select.focus();
    setTimeout(() => {
      if (!cselect || cselect.menu !== menu) return;
      document.addEventListener("pointerdown", cselectOutside, true);
    }, 0);
    document.addEventListener("keydown", cselectKeys, true);
    window.addEventListener("resize", cselectClose);
    window.addEventListener("scroll", cselectClose, { passive: true });
  }

  // 捕获阶段拦截原生弹层；鼠标、键盘都能用自己的玻璃菜单。
  document.addEventListener("pointerdown", (e) => {
    const select = e.target && e.target.closest ? e.target.closest("select.select") : null;
    if (!select || select.disabled) return;
    e.preventDefault();
    if (cselect && cselect.select === select) { cselectClose(); return; }
    cselectOpen(select);
  }, true);
  // 阻止个别 WebView 版本在 pointerdown 之后仍打开原生弹层。
  document.addEventListener("mousedown", (e) => {
    const select = e.target && e.target.closest ? e.target.closest("select.select") : null;
    if (select && !select.disabled) e.preventDefault();
  }, true);
  document.addEventListener("keydown", (e) => {
    const select = document.activeElement;
    if (!select || !select.matches || !select.matches("select.select")) return;
    if (cselect && cselect.select === select) return;
    if (e.key === "Enter" || e.key === " " || e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      cselectOpen(select);
    }
  }, true);

  function fileIcon(name) {
    const ext = (name.split(".").pop() || "").toLowerCase();
    const label = ({ pdf: "PDF", docx: "W", doc: "W", pptx: "P", xlsx: "X", xls: "X", md: "MD", txt: "TXT", html: "H", csv: "CSV" }[ext]) || "FILE";
    return `<span class="d-ico" title="${esc(ext || "file")}"><span style="font-size:9.5px;font-weight:750;letter-spacing:.02em">${esc(label)}</span></span>`;
  }

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast("已复制", "ok");
      return;
    } catch { /* 回退到 execCommand，兼容 WebView2 权限限制 */ }
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand("copy");
      ta.remove();
      toast(ok ? "已复制" : "复制失败，请手动选择文本", ok ? "ok" : "err");
    } catch {
      toast("复制失败，请手动选择文本", "err");
    }
  }

  // ---------------- 路由 ----------------
  const routes = [];
  let currentKey = "";
  let pollTimer = null;
  function clearPoll() { if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; } }

  function route(pattern, handler) {
    const keys = [];
    const rx = new RegExp("^" + pattern.replace(/:[a-zA-Z]+/g, (m) => { keys.push(m.slice(1)); return "([^/]+)"; }) + "$");
    routes.push({ rx, keys, handler });
  }

  async function dispatch() {
    // 离开设置页前把待自动保存的修改提交，避免丢配置。
    if (currentKey === "/settings" && (settingsSaveTimer || settingsDirty)) {
      try { await flushSettingsSave(); } catch { /* 忽略，下一页照常渲染 */ }
    }
    clearPoll();
    const hash = location.hash.replace(/^#/, "") || "/libraries";
    const [path] = hash.split("?");
    currentKey = path;
    for (const r of routes) {
      const m = path.match(r.rx);
      if (m) {
        const params = {};
        r.keys.forEach((k, i) => (params[k] = decodeURIComponent(m[i + 1])));
        try {
          await r.handler(params);
        } catch (err) {
          view.innerHTML = `<div class="wrap"><div class="empty">
            <div class="ico">!</div><h3>出错了</h3><p>${esc(err.message)}</p>
            <button class="btn" onclick="location.hash='#/libraries'">返回知识库</button>
          </div></div>`;
        }
        return;
      }
    }
    view.innerHTML = `<div class="wrap"><div class="empty"><div class="ico">?</div><h3>页面不存在</h3></div></div>`;
  }

  function markNav(name, text) {
    $$(".nav-item", navEl).forEach((a) => a.classList.toggle("active", a.dataset.route === name));
    const labels = { libraries: "知识库", search: "检索", settings: "设置", usage: "用量", logs: "日志" };
    const c = $("#crumb");
    if (c) c.textContent = text || labels[name] || "";
  }

  // ================= 知识库列表 =================
  function parseProvider(v) {
    if (!v) return "未配置";
    return v === "plain" ? "内置" : v;
  }

  function kbStatusChips(k) {
    const parts = [];
    parts.push(chip(`解析 · ${parseProvider(k.parse_method)}`, "stage-parse", true));
    if (k.embed_model) parts.push(chip(`嵌入 · ${k.embed_model}`, "stage-embed", true));
    parts.push(chip(`重排 · ${k.rerank_status === "on" ? (k.rerank_model || "开") : "关"}`, "stage-rerank", true));
    const total = k.documents || 0;
    if (total > 0) {
      parts.push(chip(`已解析 ${k.parsed_docs || 0}/${total}`, (k.parsed_docs || 0) === total ? "ok" : "warn"));
      parts.push(chip(`已嵌入 ${k.embedded_docs || 0}/${total}`, (k.embedded_docs || 0) === total ? "ok" : "warn"));
    }
    if (k.failed_docs > 0) parts.push(chip(`${k.failed_docs} 个失败`, "err"));
    else if (k.pending_docs > 0) parts.push(chip(`${k.pending_docs} 个处理中`, "warn"));
    return parts.join("");
  }

  route("/libraries", async () => {
    markNav("libraries");
    const data = await api("/api/kbs");
    const kbs = data.kbs || [];
    const cards = kbs.map((k) => `
      <div class="kb-card" data-id="${esc(k.id)}" tabindex="0">
        <div class="kb-actions">
          <button class="btn ghost icon sm" data-act="settings" title="知识库设置" aria-label="知识库设置">
            <svg viewBox="0 0 20 20" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><circle cx="10" cy="10" r="2.5"/><path d="M10 3v1.8M10 15.2V17M3 10h1.8M15.2 10H17M5.1 5.1l1.3 1.3M13.6 13.6l1.3 1.3M14.9 5.1l-1.3 1.3M6.4 13.6l-1.3 1.3"/></svg>
          </button>
          <button class="btn ghost icon sm" data-act="del" title="删除知识库" aria-label="删除知识库">
            <svg viewBox="0 0 20 20" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4.5 6h11M8 6V4.5h4V6M6 6l.7 9.5h6.6L14 6M8.5 8.5v5M11.5 8.5v5"/></svg>
          </button>
        </div>
        <div class="kb-top">
          <span class="kb-ico">
            <svg viewBox="0 0 24 24" width="19" height="19" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 5.5A1.5 1.5 0 0 1 5.5 4H10l1.6 2H18.5A1.5 1.5 0 0 1 20 7.5v11A1.5 1.5 0 0 1 18.5 20h-13A1.5 1.5 0 0 1 4 18.5z"/></svg>
          </span>
          <div style="min-width:0">
            <div class="kb-name" title="${esc(k.name)}">${esc(k.name)}</div>
            <div class="kb-sub">${k.kind === "zotero" ? `Zotero · ${esc(k.zotero_collection || "文献库")} · ` : ""}${k.documents} 篇文档 · ${fmtTime(k.created_at)}</div>
          </div>
        </div>
        <div class="kb-meta">
          <span><b>${k.documents}</b> 文档</span>
          <span><b>${k.chunks}</b> 切块</span>
          <span>${k.dim ? `维度 <b>${k.dim}</b>` : "尚未建立索引"}</span>
        </div>
        <div class="chips">${kbStatusChips(k)}</div>
      </div>`).join("");

    view.innerHTML = `<div class="wrap">
      <div class="page-head">
        <div>
          <div class="eyebrow">Libraries</div>
          <h1 class="title">知识库</h1>
          <p class="desc">每个知识库拥有独立的文档与向量索引。进入后可拖入文件，流水线会依次完成解析、切块、向量化，检索时可再重排。</p>
        </div>
        <div class="head-actions"><button class="btn" id="btnZoteroKb">从 Zotero 新建</button><button class="btn primary" id="btnNewKb">＋ 新建知识库</button></div>
      </div>
      <div class="kb-grid">
        ${cards}
        <div class="kb-card kb-new" id="cardNew" tabindex="0">
          <svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
          <div style="font-weight:600;font-size:13.5px">新建知识库</div>
          <div class="hint">为不同主题建立独立索引</div>
        </div>
      </div>
    </div>`;

    $("#btnNewKb").onclick = promptNewKb;
    $("#btnZoteroKb").onclick = () => { location.hash = "#/zotero"; };
    $("#cardNew").onclick = promptNewKb;
    $$(".kb-card[data-id]").forEach((card) => {
      const id = card.dataset.id;
      card.onclick = (e) => {
        if (e.target.closest("[data-act]")) return;
        location.hash = "#/library/" + encodeURIComponent(id);
      };
      card.onkeydown = (e) => { if (e.key === "Enter") location.hash = "#/library/" + encodeURIComponent(id); };
      card.querySelector("[data-act=del]").onclick = (e) => {
        e.stopPropagation();
        confirmDeleteKb(id, card.querySelector(".kb-name").textContent);
      };
      card.querySelector("[data-act=settings]").onclick = (e) => {
        e.stopPropagation();
        openKbSettings(id);
      };
    });
  });

  function promptNewKb() {
    const m = modal({
      title: "新建知识库",
      sub: "为知识库起一个名字，稍后可在其中拖入文件。",
      body: `<div class="field"><label for="kbName">名称</label>
        <input class="input" id="kbName" placeholder="例如：产品文档" autofocus></div>`,
      actions: `<button class="btn" data-act="cancel">取消</button>
        <button class="btn primary" data-act="ok">创建</button>`,
    });
    const input = m.el.querySelector("#kbName");
    input.focus();
    const submit = async () => {
      const name = input.value.trim();
      if (!name) { input.focus(); return; }
      const btn = m.el.querySelector("[data-act=ok]");
      btn.disabled = true;
      try {
        const info = await api("/api/kbs", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name }),
        });
        m.close();
        toast("已创建知识库「" + info.name + "」", "ok");
        location.hash = "#/library/" + encodeURIComponent(info.id);
        dispatch();
      } catch (err) {
        toast(err.message, "err");
        btn.disabled = false;
      }
    };
    m.el.querySelector("[data-act=ok]").onclick = submit;
    m.el.querySelector("[data-act=cancel]").onclick = m.close;
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") submit(); });
  }

  function confirmDeleteKb(id, name) {
    const m = modal({
      title: "删除知识库",
      sub: `确定删除「${name}」？其中的文档、切块与向量索引将一并删除，且不可恢复。`,
      body: `<div class="card card-pad" style="background:var(--red-soft);border-color:transparent;color:var(--red);font-size:12.5px">
        删除后无法恢复。若只是解析失败，建议先进入知识库修复设置并点击「重新处理」。</div>`,
      actions: `<button class="btn" data-act="cancel">取消</button>
        <button class="btn danger" data-act="ok">删除知识库</button>`,
    });
    m.el.querySelector("[data-act=cancel]").onclick = m.close;
    m.el.querySelector("[data-act=ok]").onclick = async () => {
      try {
        await api("/api/kbs/" + encodeURIComponent(id), { method: "DELETE" });
        m.close();
        toast("已删除知识库", "ok");
        dispatch();
      } catch (err) { toast(err.message, "err"); }
    };
  }

  // ================= Zotero 文献库 =================
  const ZOTERO_STYLES = [
    ["gb-t-7714-numeric", "GB/T 7714 数字"],
    ["gb-t-7714-author-date", "GB/T 7714 著者-年份"],
    ["apa", "APA 7"],
    ["ieee", "IEEE"],
    ["vancouver", "Vancouver"],
    ["mla", "MLA"],
  ];

  function zStyleOptions(selected) {
    return ZOTERO_STYLES.map(([v, label]) =>
      `<option value="${v}" ${v === selected ? "selected" : ""}>${label}</option>`).join("");
  }

  function zFlattenCollections(tree, depth = 0, out = []) {
    (tree || []).forEach((c) => {
      out.push({ key: c.key, name: c.name, depth, count: c.item_count || 0 });
      zFlattenCollections(c.children || [], depth + 1, out);
    });
    return out;
  }

  async function saveZoteroSettings(patch) {
    const cur = await api("/api/zotero/settings");
    // GET 会返回只读状态字段；PUT 只回传可写设置，避免后端严格解码报 unknown field。
    const { local_api_key, write_authorized, key_masked, ...writable } = cur || {};
    return api("/api/zotero/settings", {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...writable, ...patch }),
    });
  }

  async function pollZoteroJob(kbId, el) {
    if (!el) return;
    const tick = async () => {
      try {
        const s = await api("/api/kbs/" + encodeURIComponent(kbId) + "/zotero/status");
        const job = s.job || {};
        const parts = [`共 ${s.total || 0} 条`, `成功 ${s.synced || 0}`, `失败 ${s.failed || 0}`];
        if (job.state === "running") {
          parts.push(`同步中 ${job.done || 0}/${job.total || 0}`);
          if (job.current) parts.push(job.current);
        } else if (job.state) {
          parts.push(`任务 ${job.state}`);
        }
        if (s.last_sync) parts.push(`上次同步 ${fmtTime(s.last_sync)}`);
        el.textContent = parts.join(" · ");
        if (job.state === "running") setTimeout(tick, 1500);
      } catch (err) {
        el.textContent = err.message;
      }
    };
    tick();
  }

  async function openZoteroSettings(kbId) {
    let s;
    try { s = await api("/api/kbs/" + encodeURIComponent(kbId) + "/zotero/status"); }
    catch (err) { toast(err.message, "err"); return; }
    const m = modal({
      title: "文献库设置",
      sub: "解析来源与 Markdown 落盘位置；修改后对新检索立即生效。参考文献格式与引用显示方式请在「设置 → 引用格式」里调整。",
      body: `<div style="display:grid;gap:14px">
        <div class="field"><label>解析来源</label><select class="input" id="zsParse">
          <option value="auto" ${s.parse_mode === "auto" ? "selected" : ""}>优先 PDF 版面解析（含图片）</option>
          <option value="layout" ${s.parse_mode === "layout" ? "selected" : ""}>只用 PDF 版面解析</option>
          <option value="zotero" ${s.parse_mode === "zotero" ? "selected" : ""}>只用 Zotero 全文缓存（快，无图片）</option>
        </select></div>
        <div class="field"><label>解析后 MD</label><select class="input" id="zsMd">
          <option value="item" ${s.md_mode === "item" || s.md_mode === "storage" ? "selected" : ""}>同词条下（Zotero 子附件）</option>
          <option value="larder" ${s.md_mode === "larder" ? "selected" : ""}>Zotero/larder 文件夹</option>
          <option value="off" ${s.md_mode === "off" ? "selected" : ""}>不保存</option>
        </select>
        <span class="hint">“同词条下”会在 Zotero 词条下创建 Markdown 子附件，需要先在文献库页完成本地写入授权；图片放在 Markdown 同目录的 image/。</span></div>
      </div>`,
      actions: `<button class="btn" data-act="cancel">取消</button><button class="btn primary" data-act="ok">保存</button>`,
    });
    m.el.querySelector("[data-act=cancel]").onclick = m.close;
    m.el.querySelector("[data-act=ok]").onclick = async () => {
      try {
        await api("/api/kbs/" + encodeURIComponent(kbId) + "/zotero/settings", {
          method: "PATCH", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            parse_mode: m.el.querySelector("#zsParse").value,
            md_mode: m.el.querySelector("#zsMd").value,
          }),
        });
        m.close();
        toast("已保存文献库设置", "ok");
        refreshLibrary(kbId);
      } catch (err) { toast(err.message, "err"); }
    };
  }

  async function syncZoteroKb(kbId, el, force = false) {
    try {
      await api("/api/kbs/" + encodeURIComponent(kbId) + "/zotero/sync", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(force ? { force: true } : {}),
      });
      toast(force ? "已开始强制重新解析" : "Zotero 同步已启动", "ok");
      pollZoteroJob(kbId, el);
    } catch (err) { toast(err.message, "err"); }
  }

  route("/zotero", async () => {
    markNav("zotero");
    const [status, zcfg, kbsData] = await Promise.all([
      api("/api/zotero/status"), api("/api/zotero/settings"), api("/api/kbs"),
    ]);
    const kbs = (kbsData.kbs || []).filter((k) => k.kind === "zotero");
    const libCards = kbs.map((k) => `
      <div class="card card-pad" style="margin-bottom:12px">
        <div style="display:flex;gap:12px;align-items:flex-start;justify-content:space-between;flex-wrap:wrap">
          <div style="min-width:0">
            <div style="font-weight:600;font-size:15px">${esc(k.name)}</div>
            <div class="hint" style="margin-top:3px">Zotero 集合：${esc(k.zotero_collection || "—")} · ${k.documents || 0} 篇文档</div>
            <div class="hint" id="zj-${esc(k.id)}" style="margin-top:4px">正在读取同步状态…</div>
          </div>
          <div style="display:flex;gap:8px">
            <button class="btn sm" data-open="${esc(k.id)}">打开</button>
            <button class="btn primary sm" data-sync="${esc(k.id)}">同步</button>
          </div>
        </div>
      </div>`).join("");

    view.innerHTML = `<div class="wrap">
      <div class="page-head">
        <div>
          <div class="eyebrow">Zotero</div>
          <h1 class="title">文献库</h1>
          <p class="desc">把 Zotero 中的一个集合同步成 Larder 文献库：解析全文、切块、嵌入，并保留作者、年份、DOI 与引用信息。PDF 不复制，只保存派生索引。</p>
        </div>
        <div class="head-actions"><button class="btn" id="btnZRefresh">刷新</button></div>
      </div>

      <section class="settings-section">
        <div class="card group">
          <div class="list-head"><h2>Zotero 联动</h2><span class="hint">${status.api_available ? "本地 API 可用" : "本地 API 不可用，将回退 SQLite 快照"}</span></div>
          <div class="card-pad" style="display:grid;gap:14px">
            <label style="display:flex;align-items:center;gap:10px;font-size:13.5px">
              <input type="checkbox" id="zEnabled" ${zcfg.enabled ? "checked" : ""}>
              开启 Zotero 联动
            </label>
            <div class="field"><label>Zotero 数据目录</label>
              <div style="display:flex;gap:8px;flex-wrap:wrap">
                <input class="input" id="zDataDir" style="flex:1;min-width:240px" value="${esc(zcfg.data_dir || status.detected_data_dir || status.data_dir || "")}" placeholder="自动读取 Zotero 设置；读取不到再选择文件夹">
                <button class="btn" id="btnZBrowse">选择…</button>
                <button class="btn" id="btnZDetect">检测集合</button>
              </div>
              <div class="hint" style="margin-top:6px">
                自动读取：${esc(status.detected_data_dir || "未检测到，请手动填写")} · 本地 API：${esc(zcfg.api_base || "")}${status.api_error ? "（" + esc(status.api_error) + "）" : ""}
              </div>
            </div>
            <div class="field"><label>Zotero 本地写入</label>
              <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
                <span class="chip ${zcfg.write_authorized ? "ok" : ""}" id="zWriteState">${zcfg.write_authorized ? "已授权" : "未授权"}</span>
                <button class="btn sm" id="btnZAuthorize">授权写入 Zotero</button>
                <button class="btn ghost sm" id="btnZRevoke" ${zcfg.write_authorized ? "" : "hidden"}>取消授权</button>
              </div>
              <div class="hint" style="margin-top:6px">“同词条下”需要在 Zotero 10+ 中创建子附件。点授权后在 Zotero 弹窗选择“始终允许”；未授权时 Markdown 仍会写进 PDF 同目录，但不会出现在词条下。</div>
            </div>
            <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px">
              <div class="field"><label>新文献库名称</label><input class="input" id="zName" placeholder="留空则使用集合名"></div>
              <div class="field"><label>解析后 MD</label>
                <select class="input" id="zMdMode">
                  <option value="item" ${zcfg.md_mode === "item" || zcfg.md_mode === "storage" ? "selected" : ""}>同词条下（Zotero 子附件）</option>
                  <option value="larder" ${zcfg.md_mode === "larder" ? "selected" : ""}>Zotero/larder 文件夹</option>
                  <option value="off" ${zcfg.md_mode === "off" ? "selected" : ""}>不保存</option>
                </select>
              </div>
              <div class="field"><label>解析来源</label>
                <select class="input" id="zParseMode">
                  <option value="auto" ${zcfg.parse_mode === "auto" ? "selected" : ""}>优先 PDF 版面解析（含图片）</option>
                  <option value="layout" ${zcfg.parse_mode === "layout" ? "selected" : ""}>只用 PDF 版面解析</option>
                  <option value="zotero" ${zcfg.parse_mode === "zotero" ? "selected" : ""}>只用 Zotero 全文缓存（快，无图片）</option>
                </select>
              </div>
            </div>
            <div class="hint">参考文献格式、正文引用标记和文末参考文献请在「设置 → 引用格式」里统一配置；这里只管理解析与同步。</div>
            <div style="display:flex;gap:18px;flex-wrap:wrap">
              <label style="display:flex;align-items:center;gap:10px;font-size:13.5px">
                <input type="checkbox" id="zSub" checked> 包含子集合
              </label>
              <label style="display:flex;align-items:center;gap:10px;font-size:13.5px">
                <input type="checkbox" id="zWatch" ${zcfg.watch ? "checked" : ""}> 监听 Zotero 变更并自动同步
              </label>
            </div>
            <div id="zCollections" class="hint">点击「检测集合」读取 Zotero 文件夹。</div>
            <div><button class="btn primary" id="btnZCreate" disabled>创建文献库并开始同步</button></div>
          </div>
        </div>
      </section>

      <section class="settings-section">
        <div class="list-head"><h2>已有文献库</h2><span class="hint">${kbs.length} 个</span></div>
        ${libCards || `<div class="empty"><h3>还没有 Zotero 文献库</h3><p>选择 Zotero 集合后创建，Larder 会在后台逐篇解析并嵌入。</p></div>`}
      </section>
    </div>`;

    $("#btnZRefresh").onclick = () => dispatch();
    $("#zEnabled").onchange = async (e) => {
      try { await saveZoteroSettings({ enabled: e.target.checked }); toast(e.target.checked ? "已开启 Zotero 联动" : "已关闭 Zotero 联动", "ok"); }
      catch (err) { toast(err.message, "err"); e.target.checked = !e.target.checked; }
    };
    $("#zWatch").onchange = async (e) => {
      try { await saveZoteroSettings({ watch: e.target.checked }); toast(e.target.checked ? "已开启 Zotero 变更监听" : "已关闭自动同步", "ok"); }
      catch (err) { toast(err.message, "err"); e.target.checked = !e.target.checked; }
    };

    let collections = [];
    const renderCollections = () => {
      const box = $("#zCollections");
      if (!collections.length) { box.textContent = "没有读到集合。请确认数据目录正确，且 Zotero 本地 API 已开启或 zotero.sqlite 存在。"; return; }
      box.innerHTML = `<div style="max-height:260px;overflow:auto;border:1px solid var(--line);border-radius:12px;padding:8px">
        ${collections.map((c, i) => `<label style="display:flex;align-items:center;gap:9px;padding:6px 8px;border-radius:8px;cursor:pointer">
          <input type="radio" name="zCollection" value="${esc(c.key)}" data-name="${esc(c.name)}" ${i === 0 ? "checked" : ""}>
          <span style="padding-left:${c.depth * 16}px">${esc(c.name)}</span>
          <span class="hint" style="margin-left:auto">${c.count || 0} 篇</span>
        </label>`).join("")}
      </div>`;
    };
    $("#btnZBrowse").onclick = async () => {
      const btn = $("#btnZBrowse");
      btn.disabled = true;
      try {
        const data = await api("/api/system/pick-folder", { method: "POST" });
        if (data.path) {
          $("#zDataDir").value = data.path;
          toast("已选择 Zotero 数据目录", "ok");
        }
      } catch (err) { toast(err.message, "err"); }
      finally { btn.disabled = false; }
    };
    $("#btnZDetect").onclick = async () => {
      const btn = $("#btnZDetect");
      btn.disabled = true;
      try {
        const dir = $("#zDataDir").value.trim();
        await saveZoteroSettings({ data_dir: dir });
        const data = await api("/api/zotero/collections?data_dir=" + encodeURIComponent(dir));
        collections = zFlattenCollections(data.collections || []);
        renderCollections();
        $("#btnZCreate").disabled = collections.length === 0;
        toast(`读取到 ${collections.length} 个集合`, "ok");
      } catch (err) { toast(err.message, "err"); }
      finally { btn.disabled = false; }
    };

    $("#btnZAuthorize").onclick = async () => {
      const btn = $("#btnZAuthorize");
      btn.disabled = true;
      btn.textContent = "等待 Zotero 授权…";
      try {
        const r = await api("/api/zotero/authorize-write", { method: "POST" });
        if (!r.remember) {
          // “允许一次”的密钥只能用于一次写入，无法批量补挂；提示重新选择“始终允许”。
          try { await api("/api/zotero/authorize-write", { method: "DELETE" }); } catch {}
          $("#zWriteState").textContent = "未授权";
          $("#zWriteState").classList.remove("ok");
          $("#btnZRevoke").hidden = true;
          toast("你选择了“允许一次”。请重新点授权，并在 Zotero 弹窗选择“始终允许”，否则无法批量挂载。", "err");
          return;
        }
        $("#zWriteState").textContent = "已授权";
        $("#zWriteState").classList.add("ok");
        $("#btnZRevoke").hidden = false;
        toast(r.message || "已授权 Zotero 本地写入", "ok");
        // 授权后立即把已有解析 Markdown 补挂到词条下。
        let repaired = 0, created = 0, failed = 0, expired = false;
        for (const k of kbs) {
          try {
            const rr = await api(`/api/kbs/${encodeURIComponent(k.id)}/zotero/repair-attachments`, { method: "POST" });
            repaired += (rr.linked || 0) + (rr.created || 0);
            created += rr.created || 0;
            failed += rr.failed || 0;
            if (rr.message) toast(rr.message, "err");
            if (rr.auth_expired) expired = true;
          } catch (err) { failed++; }
        }
        if (expired) {
          $("#zWriteState").textContent = "授权已失效";
          $("#zWriteState").classList.remove("ok");
          $("#btnZRevoke").hidden = true;
        } else if (repaired || created) toast(`已处理 ${repaired} 个词条附件（新建 ${created}）`, failed ? "err" : "ok");
      } catch (err) { toast(err.message, "err"); }
      finally { btn.disabled = false; btn.textContent = "授权写入 Zotero"; }
    };
    $("#btnZRevoke").onclick = async () => {
      try {
        await api("/api/zotero/authorize-write", { method: "DELETE" });
        $("#zWriteState").textContent = "未授权";
        $("#zWriteState").classList.remove("ok");
        $("#btnZRevoke").hidden = true;
        toast("已取消 Zotero 本地写入授权", "ok");
      } catch (err) { toast(err.message, "err"); }
    };
    $("#btnZCreate").onclick = async () => {
      const picked = $("input[name=zCollection]:checked");
      if (!picked) { toast("请先选择一个 Zotero 集合", "err"); return; }
      const btn = $("#btnZCreate");
      btn.disabled = true;
      try {
        const dir = $("#zDataDir").value.trim();
        await saveZoteroSettings({
          data_dir: dir, md_mode: $("#zMdMode").value,
          parse_mode: $("#zParseMode").value,
          watch: $("#zWatch").checked,
        });
        const info = await api("/api/zotero/libraries", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: $("#zName").value.trim(), data_dir: dir,
            collection_key: picked.value, collection_name: picked.dataset.name,
            include_subcollections: $("#zSub").checked,
            md_mode: $("#zMdMode").value, parse_mode: $("#zParseMode").value,
            sync_now: true,
          }),
        });
        toast("已创建文献库「" + info.name + "」，同步已在后台开始", "ok");
        location.hash = "#/library/" + encodeURIComponent(info.id);
      } catch (err) { toast(err.message, "err"); btn.disabled = false; }
    };

    $$("[data-open]").forEach((b) => b.onclick = () => { location.hash = "#/library/" + encodeURIComponent(b.dataset.open); });
    $$("[data-sync]").forEach((b) => b.onclick = () => syncZoteroKb(b.dataset.sync, $("#zj-" + CSS.escape(b.dataset.sync))));
    kbs.forEach((k) => pollZoteroJob(k.id, $("#zj-" + CSS.escape(k.id))));
  });

  // ================= 知识库详情 =================
  function stageChip(label, stage, status) {
    let mark = "";
    if (status === "ok") mark = ` <span class="stage-mark">✓</span>`;
    else if (status === "error") mark = ` <span class="stage-mark err">✕</span>`;
    else if (status === "running") mark = ` <span class="stage-mark">…</span>`;
    return `<span class="chip stage-${stage}">${esc(label)}${mark}</span>`;
  }

  function docChips(d, info) {
    const parts = [];
    const provider = d.parse_method || (info && info.parse_method);
    parts.push(stageChip(`解析 · ${parseProvider(provider)}`, "parse", d.parse_status));
    const embedModel = d.embed_model || (info && info.embed_model) || "未配置";
    parts.push(stageChip(`嵌入 · ${embedModel}`, "embed", d.embed_status));
    const on = d.rerank_status === "on";
    const model = info && info.rerank_model;
    parts.push(on ? stageChip(`重排 · ${model || "开"}`, "rerank", "ok") : chip("重排 · 关", "stage-rerank"));
    return parts.join("");
  }

  function docRow(d, info) {
    const note = [];
    if (d.parse_status === "error" && d.parse_note) note.push("解析失败：" + d.parse_note);
    if (d.embed_status === "error" && d.embed_note) note.push("嵌入失败：" + d.embed_note);
    const busy = d.parse_status === "running" || d.embed_status === "running";
    return `<div class="doc-row" data-id="${esc(d.id)}">
      ${fileIcon(d.source || d.title || "")}
      <div class="d-main">
        <div class="d-title" title="${esc(d.title || "")}">${esc(d.title || "(无标题)")}</div>
        <div class="d-src" title="${esc(d.source || "")}">${esc(d.source || "")}</div>
        <div class="d-chips">${docChips(d, info)}</div>
        ${note.length ? `<div class="d-note">${esc(note.join("；"))}</div>` : ""}
      </div>
      <div class="d-chunks">${d.chunks || 0} 块</div>
      <div class="d-actions">
        <button class="btn ghost icon sm" data-act="open" title="查看 Markdown 原文 / Chunks" aria-label="查看文档">
          <svg viewBox="0 0 20 20" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M5 3.5h6l4 4v9H5z"/><path d="M11 3.5v4h4M7.5 11h5M7.5 13.5h3.5"/></svg>
        </button>
        <button class="btn ghost icon sm" data-act="retry" title="重新处理（使用已保存的原文件）" aria-label="重新处理" ${busy ? "disabled" : ""}>
          <svg viewBox="0 0 20 20" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M16 10a6 6 0 1 1-1.8-4.3M16 4v3.4h-3.4"/></svg>
        </button>
        <button class="btn ghost icon sm" data-act="del" title="删除文档" aria-label="删除文档">
          <svg viewBox="0 0 20 20" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4.5 6h11M8 6V4.5h4V6M6 6l.7 9.5h6.6L14 6M8.5 8.5v5M11.5 8.5v5"/></svg>
        </button>
      </div>
    </div>`;
  }

  function pipelineHTML(info) {
    const total = info.doc_count || 0;
    const parseVal = info.parse_mode && String(info.parse_mode).startsWith("auto+")
      ? `${parseProvider(info.parse_method)} · 内置优先`
      : parseProvider(info.parse_method);
    const parseBad = (info.failed_docs || 0) > 0;
    const embedVal = info.embed_model ? `${info.embed_model}` : "未配置向量模型";
    const rerankOn = info.rerank_status === "on";
    return `<div class="pipe">
      <div class="pipe-step stage-parse">
        <div class="pipe-step-head"><span class="pipe-idx">1</span><span class="pipe-name">解析</span></div>
        <div class="pipe-val" title="${esc(parseVal)}">${esc(parseVal)}</div>
        <div class="pipe-foot">
          ${chip(`已解析 ${info.parsed_docs || 0}/${total}`, (info.parsed_docs || 0) === total && total > 0 ? "ok" : (parseBad ? "err" : "warn"))}
          ${info.failed_docs > 0 ? chip(`${info.failed_docs} 失败`, "err") : ""}
        </div>
      </div>
      <div class="pipe-step stage-embed">
        <div class="pipe-step-head"><span class="pipe-idx">2</span><span class="pipe-name">向量化</span></div>
        <div class="pipe-val" title="${esc(embedVal)}">${esc(embedVal)}</div>
        <div class="pipe-foot">
          ${chip(`已嵌入 ${info.embedded_docs || 0}/${total}`, (info.embedded_docs || 0) === total && total > 0 ? "ok" : "warn")}
          ${chip(info.dim ? `维度 ${info.dim}` : "无索引", info.dim ? "info" : "off")}
        </div>
      </div>
      <div class="pipe-step stage-rerank">
        <div class="pipe-step-head"><span class="pipe-idx">3</span><span class="pipe-name">重排</span></div>
        <div class="pipe-val" title="${esc(rerankOn ? (info.rerank_model || "已启用") : "未启用")}">${esc(rerankOn ? (info.rerank_model || "已启用") : "未启用")}</div>
        <div class="pipe-foot">
          ${chip(rerankOn ? "检索时应用" : "仅向量召回", "stage-rerank")}
          ${chip(info.rerank_status === "on" ? "开" : "关", rerankOn ? "ok" : "off")}
        </div>
      </div>
    </div>`;
  }

  function libHeadMeta(info) {
    return `<span><b>${info.doc_count || 0}</b> 篇文档</span>
      <span style="color:var(--faint)">·</span>
      <span><b>${info.chunk_count || 0}</b> 个切块</span>
      <span style="color:var(--faint)">·</span>
      <span>${info.dim ? `向量维度 <b>${info.dim}</b>` : "尚未建立索引"}</span>`;
  }

  function libChips(info) {
    const parts = [];
    parts.push(chip(`解析 · ${parseProvider(info.parse_method)}`, "stage-parse", true));
    if (info.embed_model) parts.push(chip(`嵌入 · ${info.embed_model}`, "stage-embed", true));
    parts.push(chip(`重排 · ${info.rerank_status === "on" ? (info.rerank_model || "开") : "关"}`, "stage-rerank", true));
    const total = info.doc_count || 0;
    if (total) {
      parts.push(chip(`已解析 ${info.parsed_docs || 0}/${total}`, (info.parsed_docs || 0) === total ? "ok" : "warn"));
      parts.push(chip(`已嵌入 ${info.embedded_docs || 0}/${total}`, (info.embedded_docs || 0) === total ? "ok" : "warn"));
    }
    if (info.failed_docs > 0) parts.push(chip(`${info.failed_docs} 个失败`, "err"));
    else if (info.pending_docs > 0) parts.push(chip(`${info.pending_docs} 个处理中`, "warn"));
    return parts.join("");
  }

  async function fetchLibrary(id) {
    const [info, docsData] = await Promise.all([
      api("/api/kbs/" + encodeURIComponent(id)),
      api("/api/kbs/" + encodeURIComponent(id) + "/documents"),
    ]);
    return { info, docs: docsData.documents || [] };
  }

  function paintLibrary(info, docs) {
    if (currentKey !== "/library/" + info.id) return;
    const m = $("#libMeta");
    if (m) m.innerHTML = libHeadMeta(info);
    const c = $("#libChips");
    if (c) c.innerHTML = libChips(info);
    const p = $("#libPipe");
    if (p) p.innerHTML = pipelineHTML(info);
    const l = $("#docList");
    if (l) {
      l.innerHTML = docs.length
        ? docs.map((d) => docRow(d, info)).join("")
        : `<div class="empty"><div class="ico">
            <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3.5h8l4 4v13H6z"/><path d="M14 3.5v4h4M9 12h6M9 15.5h4"/></svg>
          </div><h3>还没有文档</h3><p>把文件拖入上方区域即可开始入库。支持 PDF、Word、PowerPoint、Excel、Markdown、HTML、EPUB 与纯文本。</p></div>`;
      bindDocActions(info.id);
    }
    const count = $("#docCount");
    if (count) count.textContent = docs.length;
    // 有待处理文档时自动轮询。
    if ((info.pending_docs || 0) > 0) schedulePoll(info.id);
  }

  function bindDocActions(kbId) {
    $$("#docList .doc-row").forEach((row) => {
      const docId = row.dataset.id;
      const title = row.querySelector(".d-title").textContent;
      row.onclick = (e) => {
        if (e.target.closest("[data-act]")) return;
        openDocument(kbId, docId);
      };
      const open = row.querySelector("[data-act=open]");
      if (open) open.onclick = (e) => { e.stopPropagation(); openDocument(kbId, docId); };
      const del = row.querySelector("[data-act=del]");
      if (del) del.onclick = (e) => {
        e.stopPropagation();
        confirmDeleteDocument(kbId, docId, title, () => refreshLibrary(kbId));
      };
      const retry = row.querySelector("[data-act=retry]");
      if (retry) retry.onclick = async (e) => {
        e.stopPropagation();
        retry.disabled = true;
        toast("正在重新处理…", "");
        try {
          const res = await api(`/api/kbs/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}/reprocess`, { method: "POST" });
          toast(`已重新处理（${res.chunks || 0} 块）`, "ok");
        } catch (err) {
          toast("重新处理失败：" + err.message, "err");
        } finally {
          retry.disabled = false;
          await refreshLibrary(kbId);
        }
      };
    });
  }

  function confirmDeleteDocument(kbId, docId, title, after) {
    const m = modal({
      title: "删除文档",
      sub: `确定删除「${title}」？其切块与向量会一并删除。`,
      body: "",
      actions: `<button class="btn" data-act="cancel">取消</button><button class="btn danger" data-act="ok">删除</button>`,
    });
    m.el.querySelector("[data-act=cancel]").onclick = m.close;
    m.el.querySelector("[data-act=ok]").onclick = async () => {
      try {
        await api(`/api/kbs/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}`, { method: "DELETE" });
        m.close();
        toast("已删除文档", "ok");
        if (after) await after();
      } catch (err) { toast(err.message, "err"); }
    };
  }

  function chunkHTML(c) {
    return `<div class="chunk-item">
      <div class="chunk-head">
        <span class="chunk-no">#${(Number(c.ordinal) || 0) + 1}</span>
        ${c.heading ? `<span class="chunk-heading">${esc(c.heading)}</span>` : ""}
      </div>
      <div class="chunk-text">${esc(c.text || "")}</div>
    </div>`;
  }

  function mdFileName(title) {
    const base = String(title || "document").replace(/[\\/:*?"<>|]+/g, "_").slice(0, 80);
    return base + ".md";
  }

  function downloadText(name, text) {
    const blob = new Blob([text], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1500);
  }

  function isTableSep(line) {
    return /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?\s*$/.test(line || "");
  }

  // 轻量 Markdown 渲染：标题、段落、列表、表格、引用、代码块、常用行内格式。
  function renderMarkdown(md) {
    const lines = String(md || "").replace(/\r\n?/g, "\n").split("\n");
    const inline = (raw) => {
      let t = esc(raw);
      t = t.replace(/`([^`]+)`/g, (_m, c) => `<code>${c}</code>`);
      t = t.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
      t = t.replace(/\*([^*\n]+)\*/g, "<em>$1</em>");
      t = t.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (m, text, url) => {
        if (!/^(https?:|mailto:)/i.test(url)) return text;
        return `<a href="${url}" target="_blank" rel="noreferrer">${text}</a>`;
      });
      return t;
    };
    const out = [];
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (/^\s*$/.test(line)) { i++; continue; }
      let m = line.match(/^\s*```([\w-]*)\s*$/);
      if (m) {
        i++;
        const buf = [];
        while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) { buf.push(lines[i]); i++; }
        if (i < lines.length) i++;
        out.push(`<pre class="md-code"><code>${esc(buf.join("\n"))}</code></pre>`);
        continue;
      }
      m = line.match(/^(#{1,6})\s+(.*)$/);
      if (m) {
        const l = m[1].length;
        out.push(`<h${l}>${inline(m[2])}</h${l}>`);
        i++;
        continue;
      }
      if (/^\s*([-*_])\1{2,}\s*$/.test(line)) { out.push("<hr>"); i++; continue; }
      if (line.includes("|") && i + 1 < lines.length && isTableSep(lines[i + 1])) {
        const parseRow = (s) => s.trim().replace(/^\||\|$/g, "").split("|").map((x) => x.trim());
        const head = parseRow(line);
        i += 2;
        const rows = [];
        while (i < lines.length && lines[i].includes("|") && !/^\s*$/.test(lines[i])) {
          rows.push(parseRow(lines[i]));
          i++;
        }
        let t = `<div class="md-table-wrap"><table><thead><tr>${head.map((h) => `<th>${inline(h)}</th>`).join("")}</tr></thead><tbody>`;
        for (const r of rows) t += `<tr>${r.map((c) => `<td>${inline(c)}</td>`).join("")}</tr>`;
        t += "</tbody></table></div>";
        out.push(t);
        continue;
      }
      if (/^\s*>\s?/.test(line)) {
        const buf = [];
        while (i < lines.length && /^\s*>\s?/.test(lines[i])) { buf.push(lines[i].replace(/^\s*>\s?/, "")); i++; }
        out.push(`<blockquote>${buf.map(inline).join("<br>")}</blockquote>`);
        continue;
      }
      m = line.match(/^\s*!\[([^\]]*)\]\(([^)\s]+)\)\s*$/);
      if (m) {
        const url = m[2];
        if (/^(https?:|\/api\/)/i.test(url)) {
          out.push(`<img class="md-img" src="${esc(url)}" alt="${esc(m[1])}" loading="lazy">`);
        } else {
          out.push(`<p>${inline(line)}</p>`);
        }
        i++;
        continue;
      }
      m = line.match(/^\s*([-*+]|\d+\.)\s+(.*)$/);
      if (m) {
        const ordered = /^\d+\./.test(m[1]);
        const items = [];
        while (i < lines.length) {
          const mm = lines[i].match(/^\s*([-*+]|\d+\.)\s+(.*)$/);
          if (!mm) break;
          items.push(inline(mm[2]));
          i++;
        }
        out.push(`<${ordered ? "ol" : "ul"}>${items.map((x) => `<li>${x}</li>`).join("")}</${ordered ? "ol" : "ul"}>`);
        continue;
      }
      const buf = [line];
      i++;
      while (i < lines.length && !/^\s*$/.test(lines[i]) && !/^(#{1,6})\s+/.test(lines[i]) &&
        !/^\s*([-*+]|\d+\.)\s+/.test(lines[i]) && !/^\s*>/.test(lines[i]) && !/^\s*```/.test(lines[i]) &&
        !(lines[i].includes("|") && i + 1 < lines.length && isTableSep(lines[i + 1]))) {
        buf.push(lines[i]);
        i++;
      }
      out.push(`<p>${inline(buf.join(" "))}</p>`);
    }
    return out.join("\n");
  }

  function escapeRegExp(s) {
    return String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }

  function queryTokens(query) {
    const out = [];
    const re = /[A-Za-z0-9_]+|[一-鿿]{2,}/g;
    let m;
    while ((m = re.exec(String(query || "")))) {
      if (m[0].length >= 2) out.push(m[0]);
    }
    return [...new Set(out)].sort((a, b) => b.length - a.length);
  }

  // 在已渲染的 HTML 文本节点上高亮关键词，避免破坏标签结构。
  function highlightKeywords(root, query) {
    const tokens = queryTokens(query);
    if (!tokens.length) return;
    const re = new RegExp("(" + tokens.map(escapeRegExp).join("|") + ")", "gi");
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null);
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    for (const node of nodes) {
      const text = node.nodeValue || "";
      if (!text) continue;
      re.lastIndex = 0;
      if (!re.test(text)) continue;
      re.lastIndex = 0;
      const frag = document.createDocumentFragment();
      let last = 0;
      let m;
      while ((m = re.exec(text)) !== null) {
        if (m.index > last) frag.appendChild(document.createTextNode(text.slice(last, m.index)));
        const mk = document.createElement("mark");
        mk.textContent = m[0];
        frag.appendChild(mk);
        last = m.index + m[0].length;
        if (m[0].length === 0) re.lastIndex++;
      }
      if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
      node.parentNode.replaceChild(frag, node);
    }
  }

  async function openDocument(kbId, docId) {
    let data;
    try {
      data = await api(`/api/kbs/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}/content`);
    } catch (err) {
      toast("读取文档内容失败：" + err.message, "err");
      return;
    }
    const doc = data.document || {};
    const chunks = data.chunks || [];
    const md = data.markdown || "";
    const hasFile = !!data.has_file;
    const fileName = data.file_name || doc.source || "document";
    const fileKind = String(data.file_kind || "").toLowerCase();
    const isPdf = fileKind === "pdf" || /\.pdf$/i.test(doc.source || "") || /\.pdf$/i.test(doc.origin_path || "");
    const fileURL = `/api/kbs/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}/file`;
    const busy = doc.parse_status === "running" || doc.embed_status === "running";
    const rendered = renderMarkdown(md);

    const tabButtons = [
      `<button class="tab active" data-ptab="md">Markdown 原文</button>`,
      `<button class="tab" data-ptab="render">渲染</button>`,
      `<button class="tab" data-ptab="chunks">Chunks (${chunks.length})</button>`,
    ];
    if (hasFile) tabButtons.push(`<button class="tab" data-ptab="file">${isPdf ? "PDF 原文" : "源文件"}</button>`);

    const filePane = hasFile ? `<div class="tab-pane" data-ppane="file" hidden>
        <div class="preview-actions">
          <a class="btn sm" href="${fileURL}" target="_blank" rel="noreferrer">在新标签打开</a>
          <a class="btn sm" href="${fileURL}?download=1">下载原文件</a>
          ${doc.origin_type !== "url" ? `<button class="btn sm" id="btnOpenFileDir">打开文件目录</button>` : ""}
        </div>
        ${isPdf
          ? `<iframe class="pdf-frame" data-src="${fileURL}" title="PDF 原文"></iframe>`
          : `<div class="file-card">
              <span class="f-ico">${esc((fileName.split(".").pop() || "FILE").toUpperCase().slice(0, 4))}</span>
              <div><div class="f-name">${esc(fileName)}</div><div class="f-sub">该格式无法内嵌预览，可打开或下载原文件。</div></div>
            </div>`}
      </div>` : "";

    const body = `<div class="preview-meta">
        <span>${chunks.length} 个切块</span>
        ${doc.parse_method && doc.parse_method !== "plain" ? chip(doc.parse_method, "stage-parse") : ""}
        ${data.reconstructed ? `<span class="chip warn">旧文档：Markdown 由切块重组</span>` : ""}
        <span class="hint" style="margin-left:auto">${esc(doc.source || "")}</span>
      </div>
      <div class="tabs" role="tablist">${tabButtons.join("")}</div>
      <div class="tab-pane" data-ppane="md">
        <div class="preview-actions">
          <button class="btn sm" id="btnCopyMd">复制 Markdown</button>
          <button class="btn sm" id="btnExportMd">导出 Markdown + image</button>
          ${data.has_md ? `<button class="btn sm" id="btnOpenMdDir">打开解析目录</button>` : ""}
        </div>
        <pre class="md-box" id="mdBox">${esc(md || "（没有可显示的 Markdown）")}</pre>
      </div>
      <div class="tab-pane" data-ppane="render" hidden>
        <div class="preview-actions">
          <button class="btn sm" id="btnExportMd2">导出 Markdown + image</button>
          ${data.has_md ? `<button class="btn sm" id="btnOpenMdDir2">打开解析目录</button>` : ""}
        </div>
        <div class="md-render">${rendered || "<p>（没有可渲染的内容）</p>"}</div>
      </div>
      <div class="tab-pane" data-ppane="chunks" hidden>
        <div class="chunk-list">${chunks.length ? chunks.map(chunkHTML).join("") : `<div class="empty"><p>还没有切块</p></div>`}</div>
      </div>
      ${filePane}`;

    const m = modal({
      title: doc.title || "文档",
      sub: `${doc.source || ""} · ${chunks.length} 块`,
      body,
      xwide: true,
      actions: `<button class="btn danger" data-act="del">删除</button>
        <button class="btn" data-act="retry" ${busy ? "disabled" : ""}>重新处理</button>
        <button class="btn" data-act="close">关闭</button>`,
    });

    m.el.querySelectorAll("[data-ptab]").forEach((tab) => {
      tab.onclick = () => {
        const which = tab.dataset.ptab;
        m.el.querySelectorAll("[data-ptab]").forEach((t) => t.classList.toggle("active", t === tab));
        m.el.querySelectorAll("[data-ppane]").forEach((p) => { p.hidden = p.dataset.ppane !== which; });
        if (which === "file") {
          const frame = m.el.querySelector("iframe.pdf-frame");
          if (frame && !frame.getAttribute("src") && frame.dataset.src) frame.src = frame.dataset.src;
        }
      };
    });

    const copyBtn = m.el.querySelector("#btnCopyMd");
    if (copyBtn) copyBtn.onclick = () => copyText(md);
    const exportZip = `/api/kbs/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}/export.zip`;
    const exp1 = m.el.querySelector("#btnExportMd");
    if (exp1) exp1.onclick = () => { window.location.href = exportZip; toast("正在导出 Markdown + image/", "ok"); };
    const exp2 = m.el.querySelector("#btnExportMd2");
    if (exp2) exp2.onclick = () => { window.location.href = exportZip; toast("正在导出 Markdown + image/", "ok"); };
    const openDir = async (kind) => {
      try {
        const r = await api(`/api/kbs/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}/open-folder`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ kind }),
        });
        toast("已打开：" + (r.path || ""), "ok");
      } catch (err) { toast(err.message, "err"); }
    };
    const openMd = m.el.querySelector("#btnOpenMdDir");
    if (openMd) openMd.onclick = () => openDir("md");
    const openMd2 = m.el.querySelector("#btnOpenMdDir2");
    if (openMd2) openMd2.onclick = () => openDir("md");
    const openFile = m.el.querySelector("#btnOpenFileDir");
    if (openFile) openFile.onclick = () => openDir("pdf");

    m.el.querySelector("[data-act=del]").onclick = () => {
      m.close();
      confirmDeleteDocument(kbId, docId, doc.title || "文档", () => refreshLibrary(kbId));
    };
    const retry = m.el.querySelector("[data-act=retry]");
    if (retry) retry.onclick = async () => {
      retry.disabled = true;
      retry.textContent = "处理中…";
      try {
        const res = await api(`/api/kbs/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}/reprocess`, { method: "POST" });
        toast(`已重新处理（${res.chunks || 0} 块）`, "ok");
        m.close();
        await refreshLibrary(kbId);
      } catch (err) {
        toast("重新处理失败：" + err.message, "err");
        retry.disabled = false;
        retry.textContent = "重新处理";
      }
    };
  }

  function schedulePoll(id) {
    clearPoll();
    pollTimer = setTimeout(async () => {
      pollTimer = null;
      if (currentKey !== "/library/" + id) return;
      try {
        const { info, docs } = await fetchLibrary(id);
        paintLibrary(info, docs);
      } catch { /* 路由切换或服务重启 */ }
    }, 2500);
  }

  async function refreshLibrary(id) {
    const { info, docs } = await fetchLibrary(id);
    paintLibrary(info, docs);
  }

  route("/library/:id", async ({ id }) => {
    markNav("libraries");
    const { info, docs } = await fetchLibrary(id);
    const crumbEl = $("#crumb");
    if (crumbEl) crumbEl.textContent = "知识库 / " + (info.name || id);
    view.innerHTML = `<div class="wrap">
      <div class="page-head">
        <div style="min-width:0">
          <a class="eyebrow" href="#/libraries" style="color:var(--muted)">
            <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M10 3 5 8l5 5"/></svg>
            知识库
          </a>
          <h1 class="title" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(info.name || id)}</h1>
          <p class="desc" id="libMeta">${libHeadMeta(info)}</p>
          <div class="chips" id="libChips">${libChips(info)}</div>
        </div>
        <div class="head-actions">
          <button class="btn" id="btnRefresh">刷新</button>
          <button class="btn" id="btnKbSettings">知识库设置</button>
          <button class="btn" id="btnRename">重命名</button>
        </div>
      </div>

      ${info.kind === "zotero" ? `<div class="card card-pad" style="margin-bottom:12px;display:flex;gap:12px;align-items:center;justify-content:space-between;flex-wrap:wrap">
        <div style="min-width:0">
          <div style="font-weight:600">Zotero 文献库 · ${esc(info.zotero_collection || "未命名集合")}</div>
          <div class="hint" id="zoteroJob" style="margin-top:4px">正在读取同步状态…</div>
        </div>
        <div style="display:flex;gap:8px">
          <a class="btn sm" href="/api/kbs/${encodeURIComponent(info.id)}/bibliography.bib" target="_blank" rel="noreferrer">导出 .bib</a>
          <button class="btn sm" id="btnZoteroSettings">引用设置</button>
          <button class="btn sm" id="btnZoteroRepair" title="把已有解析 Markdown 挂到对应 Zotero 词条下，并清理重复图片">修复词条附件</button>
          <button class="btn sm" id="btnZoteroForce" title="忽略 dateModified，重新解析全部条目（用于切换版面解析后刷新）">强制重解析</button>
          <button class="btn primary sm" id="btnZoteroSync">同步 Zotero</button>
        </div>
      </div>` : ""}

      <div id="libPipe">${pipelineHTML(info)}</div>

      <div style="height:16px"></div>

      <section class="settings-section">
        <div class="sec-head"><h2>添加文档</h2><p>拖入或选择文件，系统会显示每个环节的处理状态。</p></div>
        <div class="dropzone" id="drop" tabindex="0" role="button" aria-label="添加文档">
          <div class="dz-icon">
            <svg viewBox="0 0 24 24" width="21" height="21" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 16V4m0 0L7.5 8.5M12 4l4.5 4.5"/><path d="M4 15.5v2A2.5 2.5 0 0 0 6.5 20h11a2.5 2.5 0 0 0 2.5-2.5v-2"/></svg>
          </div>
          <div class="dz-main">
            <div class="dz-title">把文件拖到这里，或 <strong>点击选择</strong></div>
            <div class="dz-hint">支持 PDF / Word / PowerPoint / Excel / Markdown / HTML / EPUB / 纯文本；文字版 PDF 由内置解析器处理，扫描件自动调用已配置的云 OCR。</div>
            <div class="dz-status" id="dzStatus"></div>
            <div class="progress" id="progress" style="display:none"><i></i></div>
          </div>
          <input type="file" id="fileInput" multiple hidden>
        </div>
      </section>

      <section class="settings-section">
        <div class="card group">
          <div class="list-head">
            <h2>文档 <span style="color:var(--muted);font-weight:500">（<span id="docCount">${docs.length}</span>）</span></h2>
            <button class="btn ghost sm" id="btnReloadDocs">刷新列表</button>
          </div>
          <div class="doc-list" id="docList"></div>
        </div>
      </section>
    </div>`;

    $("#btnRefresh").onclick = () => refreshLibrary(id);
    $("#btnReloadDocs").onclick = () => refreshLibrary(id);
    $("#btnKbSettings").onclick = () => openKbSettings(id);
    $("#btnRename").onclick = () => renameKb(id, info.name);
    setupDropzone(id);
    paintLibrary(info, docs);
    if (info.kind === "zotero") {
      $("#btnZoteroSync").onclick = () => syncZoteroKb(id, $("#zoteroJob"));
      $("#btnZoteroForce").onclick = () => syncZoteroKb(id, $("#zoteroJob"), true);
      $("#btnZoteroSettings").onclick = () => openZoteroSettings(id);
      $("#btnZoteroRepair").onclick = async () => {
        const btn = $("#btnZoteroRepair");
        btn.disabled = true;
        btn.textContent = "修复中…";
        try {
          const r = await api(`/api/kbs/${encodeURIComponent(id)}/zotero/repair-attachments`, { method: "POST" });
          toast(r.message || `修复完成：新建 ${r.created || 0}，已存在 ${r.linked || 0}，跳过 ${r.skipped || 0}，失败 ${r.failed || 0}`, r.failed ? "err" : "ok");
          if (r.errors && r.errors.length) console.warn("Zotero 附件修复失败", r.errors);
        } catch (err) { toast(err.message, "err"); }
        finally { btn.disabled = false; btn.textContent = "修复词条附件"; }
      };
      pollZoteroJob(id, $("#zoteroJob"));
    }
  });

  function renameKb(id, cur) {
    const m = modal({
      title: "重命名知识库",
      body: `<div class="field"><label for="nm">名称</label><input class="input" id="nm" value="${esc(cur || "")}"></div>`,
      actions: `<button class="btn" data-act="cancel">取消</button><button class="btn primary" data-act="ok">保存</button>`,
    });
    const input = m.el.querySelector("#nm");
    input.focus(); input.select();
    m.el.querySelector("[data-act=cancel]").onclick = m.close;
    m.el.querySelector("[data-act=ok]").onclick = async () => {
      const name = input.value.trim();
      if (!name) return;
      try {
        await api("/api/kbs/" + encodeURIComponent(id), {
          method: "PATCH", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name }),
        });
        m.close(); toast("已重命名", "ok"); dispatch();
      } catch (err) { toast(err.message, "err"); }
    };
  }

  // ---------------- 拖放 / 上传 ----------------
  function setupDropzone(kbId) {
    const dz = $("#drop"), input = $("#fileInput");
    if (!dz || !input) return;
    dz.onclick = () => input.click();
    dz.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); input.click(); } };
    input.onchange = () => { if (input.files.length) uploadFiles(kbId, input.files); input.value = ""; };
    ["dragenter", "dragover"].forEach((ev) =>
      dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("drag"); }));
    ["dragleave", "drop"].forEach((ev) =>
      dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("drag"); }));
    dz.addEventListener("drop", (e) => {
      if (e.dataTransfer && e.dataTransfer.files.length) uploadFiles(kbId, e.dataTransfer.files);
    });
  }

  function uploadOne(kbId, file, onProgress) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", `/api/kbs/${encodeURIComponent(kbId)}/ingest/upload`);
      xhr.responseType = "text";
      xhr.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(e.loaded / e.total); };
      xhr.onload = () => {
        let data = null;
        try { data = xhr.responseText ? JSON.parse(xhr.responseText) : null; } catch { data = xhr.responseText; }
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(data);
        } else {
          const msg = (data && data.error) || (typeof data === "string" ? data : `HTTP ${xhr.status}`);
          const err = new Error(msg);
          err.status = xhr.status;
          err.data = data;
          reject(err);
        }
      };
      xhr.onerror = () => reject(new Error("网络错误，上传失败"));
      const fd = new FormData();
      fd.append("file", file, file.name);
      fd.append("title", file.name.replace(/\.[^.]+$/, ""));
      fd.append("source", file.name);
      xhr.send(fd);
    });
  }

  async function uploadFiles(kbId, files) {
    const list = Array.from(files);
    if (!list.length) return;
    const prog = $("#progress"), bar = prog ? prog.querySelector("i") : null, status = $("#dzStatus");
    if (prog) prog.style.display = "block";
    const failures = [];
    for (let i = 0; i < list.length; i++) {
      const f = list[i];
      if (status) status.textContent = `正在处理「${f.name}」（${i + 1}/${list.length}）：解析 → 切块 → 向量化…`;
      try {
        const res = await uploadOne(kbId, f, (p) => {
          if (bar) bar.style.width = Math.round(((i + p * 0.35) / list.length) * 100) + "%";
          if (status) status.textContent = `正在上传「${f.name}」 ${Math.round(p * 100)}%…`;
        });
        if (res && (res.skipped ?? res.Skipped)) {
          toast(`「${f.name}」内容未变，已跳过`, "");
        } else {
          const n = res.chunks ?? res.Chunks ?? 0;
          toast(`已入库「${f.name}」（${n} 块 · 解析✓ 嵌入✓${res.rerank_status === "on" ? " · 重排开" : ""}）`, "ok");
        }
      } catch (err) {
        failures.push(f.name);
        toast(`「${f.name}」处理失败：${err.message}`, "err");
      }
      if (bar) bar.style.width = Math.round(((i + 1) / list.length) * 100) + "%";
    }
    if (status) status.textContent = failures.length ? `${failures.length} 个文件失败，可在下方列表查看原因并重试` : "";
    setTimeout(() => { if (prog) { prog.style.display = "none"; if (bar) bar.style.width = "0"; } }, 900);
    try { await refreshLibrary(kbId); } catch { /* ignore */ }
  }

  // ---------------- 知识库设置 ----------------
  async function openKbSettings(id) {
    let data;
    try {
      data = await api("/api/kbs/" + encodeURIComponent(id) + "/settings");
    } catch (err) {
      toast(err.message, "err");
      return;
    }
    const s = data.settings || {};
    const raw = data.raw || {};
    const rawRerank = Object.prototype.hasOwnProperty.call(raw, "rerank_enabled") ? raw.rerank_enabled : null;
    const rerankOn = rawRerank === null || rawRerank === undefined ? s.rerank_enabled === true : rawRerank === true;
    const rawParse = raw.parse_provider || "global";
    const m = modal({
      title: "知识库设置",
      sub: "这些参数只作用于当前知识库；解析与向量服务本身在「设置」中配置。",
      wide: true,
      body: `<div class="group" style="border:1px solid var(--line);border-radius:10px;overflow:hidden">
        <div class="row"><div class="row-main"><div class="row-title">名称</div><div class="row-sub">显示在知识库列表与 MCP 工具中。</div></div>
          <div class="row-control" style="width:240px"><input class="input" id="kbSetName" value="${esc(s.name || "")}"></div></div>

        <div class="row"><div class="row-main"><div class="row-title">解析方式</div><div class="row-sub">全局解析服务为 ${esc(data.parse_service || "-")}；外部服务地址与密钥在「设置」中填写。</div></div>
          <div class="row-control" style="width:190px"><select class="select" id="kbParse">
            <option value="global" ${rawParse === "global" || rawParse === "" ? "selected" : ""}>跟随全局（${esc(parseProvider(data.parse_service))}）</option>
            <option value="plain" ${rawParse === "plain" ? "selected" : ""}>内置解析 · 无需模型</option>
            <option value="mineru" ${rawParse === "mineru" ? "selected" : ""}>mineru</option>
            <option value="docling" ${rawParse === "docling" ? "selected" : ""}>docling</option>
            <option value="tika" ${rawParse === "tika" ? "selected" : ""}>tika</option>
          </select></div></div>

        <div class="row"><div class="row-main"><div class="row-title">检索时重排</div><div class="row-sub">关闭后只做向量召回；开启需要全局重排服务已配置。</div></div>
          <div class="row-control"><label class="switch"><input type="checkbox" id="kbRerank" ${rerankOn ? "checked" : ""}><span class="track"></span><span class="thumb"></span></label></div></div>

        <div class="row stack"><div class="row-main"><div class="row-title">切块参数</div><div class="row-sub">影响新入库文档；已有文档需点「重新处理」后生效。</div></div>
          <div class="grid3">
            <div class="field"><label>单块上限（字符）</label><input class="input" type="number" id="kbMax" min="100" max="8000" value="${s.chunk_max_runes || 1200}"></div>
            <div class="field"><label>块间重叠</label><input class="input" type="number" id="kbOverlap" min="0" max="4000" value="${s.chunk_overlap || 0}"></div>
            <div class="field"><label>最小块（字符）</label><input class="input" type="number" id="kbMin" min="0" max="500" value="${s.chunk_min_runes || 0}"></div>
          </div></div>

        <div class="row stack"><div class="row-main"><div class="row-title">检索参数</div><div class="row-sub">重排开启时，先召回候选条数，再重排取前 N 条。</div></div>
          <div class="grid2">
            <div class="field"><label>返回条数 top_k</label><input class="input" type="number" id="kbTopK" min="1" max="50" value="${s.top_k || 8}"></div>
            <div class="field"><label>候选条数</label><input class="input" type="number" id="kbCand" min="1" max="200" value="${s.candidate_k || 40}"></div>
          </div></div>
      </div>
      <p class="hint" style="margin:10px 2px 0">当前向量模型：${esc((data.embed_service && data.embed_service.model) || "-")}；重排模型：${esc((data.rerank_service && data.rerank_service.model) || "-")}。</p>`,
      actions: `<button class="btn" data-act="cancel">取消</button><button class="btn primary" data-act="ok">保存设置</button>`,
    });
    m.el.querySelector("[data-act=cancel]").onclick = m.close;
    m.el.querySelector("[data-act=ok]").onclick = async () => {
      const payload = {
        name: m.el.querySelector("#kbSetName").value.trim(),
        parse_provider: m.el.querySelector("#kbParse").value,
        rerank_enabled: m.el.querySelector("#kbRerank").checked,
        chunk_max_runes: Number(m.el.querySelector("#kbMax").value) || 0,
        chunk_overlap: Number(m.el.querySelector("#kbOverlap").value) || 0,
        chunk_min_runes: Number(m.el.querySelector("#kbMin").value) || 0,
        top_k: Number(m.el.querySelector("#kbTopK").value) || 0,
        candidate_k: Number(m.el.querySelector("#kbCand").value) || 0,
      };
      if (!payload.name) { toast("名称不能为空", "err"); return; }
      const btn = m.el.querySelector("[data-act=ok]");
      btn.disabled = true;
      try {
        await api("/api/kbs/" + encodeURIComponent(id) + "/settings", {
          method: "PUT", headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        m.close(); toast("知识库设置已保存", "ok");
        dispatch();
      } catch (err) {
        toast(err.message, "err");
        btn.disabled = false;
      }
    };
  }

  // ================= 日志 =================
  function fmtLogTime(s) {
    try {
      const d = new Date(s);
      if (isNaN(d.getTime())) return "";
      const pad = (n) => String(n).padStart(2, "0");
      return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
    } catch { return ""; }
  }

  route("/logs", async () => {
    markNav("logs");
    const data = await api("/api/logs?limit=200");
    const events = data.events || [];
    const cards = events.map((e, idx) => {
      const bad = e.status === "error";
      const kindClass = bad ? "err" : (String(e.kind).startsWith("MCP") ? "stage-rerank" : (e.kind === "入库" || e.kind === "重新处理" ? "stage-embed" : (e.kind === "检索" ? "stage-parse" : "")));
      const snips = e.snippets || [];
      return `<div class="log-item" data-log="${idx}" title="点击查看完整日志">
        <div class="log-item-top">
          <span class="chip ${kindClass}">${esc(e.kind || "事件")}</span>
          <span class="log-time">${esc(fmtLogTime(e.time))}</span>
          <span class="chip ${bad ? "err" : "ok"}">${bad ? "失败" : "成功"}</span>
          ${e.duration_ms ? `<span class="log-dur">${esc(String(e.duration_ms))} ms</span>` : ""}
        </div>
        <div class="log-title">${esc(e.title || "—")}</div>
        <div class="log-sub">${esc(e.kb || "—")}${snips.length ? ` · ${snips.length} 个命中片段` : ""}${e.detail ? ` · ${esc(e.detail)}` : ""}</div>
      </div>`;
    }).join("");
    view.innerHTML = `<div class="wrap">
      <div class="page-head">
        <div><div class="eyebrow">Activity</div><h1 class="title">交互日志</h1>
          <p class="desc">检索、入库、重新处理、设置与 MCP 调用的最近记录；点击任意一条查看命中片段与原文。保存在 data/activity.log。</p></div>
        <div class="head-actions"><button class="btn" id="btnLogRefresh">刷新</button></div>
      </div>
      ${cards ? `<div class="log-list">${cards}</div>` : `<div class="card"><div class="empty"><div class="ico">∅</div><h3>还没有交互记录</h3><p>完成一次检索或入库后，这里会出现记录。</p></div></div>`}
    </div>`;
    $$("[data-log]", view).forEach((row) => {
      row.onclick = () => {
        const e = events[Number(row.dataset.log)];
        if (!e) return;
        const bad = e.status === "error";
        const snips = e.snippets || [];
        const snipHTML = snips.map((sn) => `
          <div class="log-snip">
            <div class="log-snip-meta">${esc(sn.kb_id || e.kb || "")}${sn.doc ? ` · ${esc(sn.doc)}` : ""}${sn.source ? ` · ${esc(sn.source)}` : ""}${sn.heading ? ` · ${esc(sn.heading)}` : ""}</div>
            <div class="log-snip-text">${esc(sn.text || "")}</div>
            ${sn.kb_id && sn.doc_id ? `<button class="btn sm log-open-doc" data-kb="${esc(sn.kb_id)}" data-doc="${esc(sn.doc_id)}">查看原文档</button>` : ""}
          </div>`).join("");
        const m = modal({
          title: "交互详情",
          sub: fmtLogTime(e.time) + " · " + (e.kind || "事件"),
          wide: true,
          body: `<div class="card card-pad" style="display:grid;gap:9px">
            <div><span class="hint">知识库</span><div>${esc(e.kb || "—")}</div></div>
            <div><span class="hint">内容</span><div style="word-break:break-word">${esc(e.title || "—")}</div></div>
            <div><span class="hint">状态 / 耗时</span><div>${bad ? "失败" : "成功"}${e.duration_ms ? " · " + esc(String(e.duration_ms)) + " ms" : ""}</div></div>
            <div><span class="hint">详情</span><pre class="md-box" style="max-height:160px;margin:5px 0 0">${esc(e.detail || "无")}</pre></div>
            ${snips.length ? `<div><span class="hint">命中片段</span><div class="log-snips">${snipHTML}</div></div>` : `<div class="hint">这条记录没有保存片段（旧日志或非检索操作）。</div>`}
          </div>`,
          actions: `<button class="btn" data-act="close">关闭</button>`,
        });
        $$(".log-open-doc", m.el).forEach((btn) => {
          btn.onclick = () => {
            const kb = btn.dataset.kb, doc = btn.dataset.doc;
            if (!kb || !doc) return;
            m.close();
            openDocument(kb, doc);
          };
        });
      };
    });
    const focusMatch = location.hash.match(/[?&]event=([^&]+)/);
    if (focusMatch) {
      const focus = decodeURIComponent(focusMatch[1]);
      const idx = events.findIndex((x) => x.id === focus);
      if (idx >= 0) {
        const row = view.querySelector(`[data-log="${idx}"]`);
        if (row) row.click();
        history.replaceState(null, "", "#/logs");
      }
    }
    const refresh = $("#btnLogRefresh");
    if (refresh) refresh.onclick = () => dispatch();
  });

  // ================= 用量 =================
  function fmtInt(n) {
    return Number(n || 0).toLocaleString("zh-CN");
  }

  function fmtTokens(n) {
    n = Number(n || 0);
    const trim = (v) => v.toFixed(v >= 100 ? 0 : 1).replace(/\.0$/, "");
    if (n >= 1e6) return trim(n / 1e6) + "M";
    if (n >= 1e3) return trim(n / 1e3) + "K";
    return fmtInt(n);
  }

  function usageHTML(data, days) {
    const pad = (n) => String(n).padStart(2, "0");
    const byDay = new Map((data.by_day || []).map((d) => [d.day, d]));
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const series = [];
    for (let i = days - 1; i >= 0; i--) {
      const d = new Date(today);
      d.setDate(today.getDate() - i);
      const key = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
      const row = byDay.get(key) || { parse: 0, embed_tokens: 0, rerank_tokens: 0, total_tokens: 0 };
      series.push({ key, ...row });
    }
    const max = Math.max(1, ...series.map((d) => Number(d.total_tokens || 0)));
    const parseCount = Number(data.parse_count || 0);
    const parseFailed = Number(data.parse_failed || 0);
    const embedTokens = Number(data.embed_tokens || 0);
    const rerankTokens = Number(data.rerank_tokens || 0);
    const totalTokens = Number(data.total_tokens || 0);
    const kbRows = (data.by_kb || []).map((k) => `
      <tr>
        <td>${esc(k.name || k.id)}</td>
        <td class="num">${fmtInt(k.parse_count)}</td>
        <td class="num">${fmtTokens(k.embed_tokens)}</td>
        <td class="num">${fmtTokens(k.rerank_tokens)}</td>
        <td class="num">${fmtTokens(k.total_tokens)}</td>
      </tr>`).join("");
    const typeLabel = { parse: "解析", embed: "嵌入", rerank: "重排" };
    const modelRows = (data.by_model || []).map((m) => `
      <tr>
        <td><span class="chip stage-${m.kind === "embed" ? "embed" : m.kind === "rerank" ? "rerank" : "parse"}">${typeLabel[m.kind] || m.kind}</span></td>
        <td class="mono">${esc(m.kind === "parse" && m.model === "plain" ? "内置" : (m.model || "—"))}</td>
        <td class="num">${fmtInt(m.count)}</td>
        <td class="num">${fmtTokens(m.tokens)}</td>
      </tr>`).join("");
    const chartBars = series.map((d, idx) => {
      const total = Number(d.total_tokens || 0);
      const pct = total > 0 ? Math.max(3, (total / max) * 100) : 0;
      const title = `${d.key} · 解析 ${d.parse || 0} 次 · 嵌入 ${fmtTokens(d.embed_tokens)} · 重排 ${fmtTokens(d.rerank_tokens)}`;
      const label = (idx % 5 === 0 || idx === series.length - 1) ? d.key.slice(5) : "";
      return `<div class="u-day" title="${esc(title)}">
        <div class="u-bars"><i class="u-bar" style="height:${pct}%"></i></div>
        <span class="u-date">${esc(label)}</span>
      </div>`;
    }).join("");

    return `<div class="wrap">
      <div class="page-head">
        <div><div class="eyebrow">Usage</div><h1 class="title">用量</h1>
          <p class="desc">统计解析次数与嵌入/重排的 token 消耗；历史记录按字符估算，新请求使用服务返回的 usage。</p></div>
        <div class="head-actions">
          ${[7, 30, 90].map((d) => `<button class="btn sm ${d === days ? "primary" : ""}" data-usage-days="${d}">最近 ${d} 天</button>`).join("")}
        </div>
      </div>

      <div class="usage-cards">
        <div class="usage-card stage-parse">
          <div class="u-label">解析次数</div>
          <div class="u-value">${fmtInt(parseCount)}</div>
          <div class="u-sub">失败 ${fmtInt(parseFailed)} · ${fmtTokens(data.parse_chars)} 字符</div>
        </div>
        <div class="usage-card stage-embed">
          <div class="u-label">嵌入 Token</div>
          <div class="u-value">${fmtTokens(embedTokens)}</div>
          <div class="u-sub">${fmtInt(data.embed_count)} 次调用</div>
        </div>
        <div class="usage-card stage-rerank">
          <div class="u-label">重排 Token</div>
          <div class="u-value">${fmtTokens(rerankTokens)}</div>
          <div class="u-sub">${fmtInt(data.rerank_count)} 次调用</div>
        </div>
        <div class="usage-card accent">
          <div class="u-label">总 Token</div>
          <div class="u-value">${fmtTokens(totalTokens)}</div>
          <div class="u-sub">嵌入 + 重排</div>
        </div>
      </div>

      <section class="settings-section">
        <div class="sec-head"><h2>每日活动</h2><p>最近 ${days} 天的 token 消耗。</p></div>
        <div class="card card-pad">
          <div class="usage-chart">${chartBars}</div>
        </div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>按知识库</h2><p>各知识库的解析次数与 token 消耗。</p></div>
        <div class="card">
          ${kbRows ? `<table class="usage-table"><thead><tr><th>知识库</th><th class="num">解析</th><th class="num">嵌入 Token</th><th class="num">重排 Token</th><th class="num">合计</th></tr></thead><tbody>${kbRows}</tbody></table>` : `<div class="empty"><div class="ico">∅</div><h3>还没有用量记录</h3><p>入库或检索后，这里会开始累积。</p></div>`}
        </div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>按模型</h2><p>解析服务、嵌入模型与重排模型的使用量。</p></div>
        <div class="card">
          ${modelRows ? `<table class="usage-table"><thead><tr><th>类型</th><th>模型 / 服务</th><th class="num">次数</th><th class="num">Token</th></tr></thead><tbody>${modelRows}</tbody></table>` : `<div class="empty"><div class="ico">∅</div><h3>还没有模型用量</h3><p>完成一次入库或检索后出现。</p></div>`}
        </div>
      </section>
    </div>`;
  }

  route("/usage", async () => {
    markNav("usage");
    const load = async (days) => {
      const data = await api("/api/usage?days=" + days);
      view.innerHTML = usageHTML(data, days);
      $$("[data-usage-days]").forEach((btn) => {
        btn.onclick = () => load(Number(btn.dataset.usageDays));
      });
    };
    await load(30);
  });

  // ================= 检索 =================
  route("/search", async () => {
    markNav("search");
    const data = await api("/api/kbs");
    const kbs = data.kbs || [];
    if (!kbs.length) {
      view.innerHTML = `<div class="wrap"><div class="page-head"><div><div class="eyebrow">Search</div><h1 class="title">语义检索</h1>
        <p class="desc">选择知识库，输入问题，返回带来源引用的相关片段。</p></div></div>
        <div class="card"><div class="empty"><div class="ico">∅</div><h3>还没有知识库</h3><p>先创建一个知识库并入库文档，再来检索。</p>
        <button class="btn primary" onclick="location.hash='#/libraries'">去创建知识库</button></div></div></div>`;
      return;
    }
    view.innerHTML = `<div class="wrap">
      <div class="page-head"><div><div class="eyebrow">Search</div><h1 class="title">语义检索</h1>
        <p class="desc">先用向量召回相关片段；启用重排时，再按问题与片段的相关性精排。结果附带文档标题、章节与来源。</p></div></div>
      <div class="searchbar">
        <select class="select" id="kbSel" aria-label="知识库">
          ${kbs.map((k) => `<option value="${esc(k.id)}">${esc(k.name)}</option>`).join("")}
        </select>
        <input class="input" id="q" placeholder="输入问题，回车检索…" autocomplete="off">
        <button class="btn primary" id="btnSearch">检索</button>
      </div>
      <div id="searchMeta"></div>
      <div id="results"></div>
    </div>`;

    const doSearch = async () => {
      const q = $("#q").value.trim();
      const kb = $("#kbSel").value;
      if (!q) { $("#q").focus(); return; }
      const box = $("#results"), meta = $("#searchMeta");
      box.innerHTML = `<div class="loading-center"><span class="spinner"></span></div>`;
      meta.innerHTML = "";
      const btn = $("#btnSearch");
      btn.disabled = true;
      try {
        const res = await api("/api/kbs/" + encodeURIComponent(kb) + "/search", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ query: q }),
        });
        const hits = res.results || [];
        const rerankErr = String(res.rerank_error || "");
        const rerankErrShort = rerankErr.length > 180 ? rerankErr.slice(0, 180) + "…" : rerankErr;
        meta.innerHTML = `<div class="search-meta">
          ${chip(`找到 ${hits.length} 条`, hits.length ? "ok" : "off")}
          ${res.reranked ? chip("重排已应用", "accent") : (res.rerank_error ? chip("重排失败，已退回向量召回", "warn") : chip("仅向量召回", "off"))}
          ${res.reranked && res.rerank_model ? chip(res.rerank_model, "info") : ""}
          ${chip(`top_k ${res.top_k || ""}`, "off")}
        </div>
        ${res.rerank_error ? `<div class="hint err" style="margin:-6px 0 14px">重排服务不可用：${esc(rerankErrShort)}。以上为向量召回结果。</div>` : ""}`;
        if (!hits.length) {
          box.innerHTML = `<div class="card"><div class="empty"><div class="ico">∅</div><h3>没有命中</h3>
            <p>换个说法，或先入库更多文档。若知识库中文档显示解析/嵌入失败，请先在知识库设置中修复并重新处理。</p></div></div>`;
          return;
        }
        box.innerHTML = hits.map((h) => `
          <div class="result">
            <div class="r-head">
              <div class="r-title">${esc(h.doc_title || "(无标题)")}</div>
              <div class="r-score">${Number(h.score || 0).toFixed(4)}</div>
            </div>
            ${h.heading ? `<div class="r-heading">${esc(h.heading)}</div>` : ""}
            <div class="r-render">${renderMarkdown(h.text) || esc(h.text)}</div>
            <div class="r-src">${esc(h.source || "")}${h.vector_distance !== undefined ? ` · 距离 ${Number(h.vector_distance).toFixed(4)}` : ""}</div>
          </div>`).join("");
        highlightKeywords(box, q);
      } catch (err) {
        meta.innerHTML = "";
        box.innerHTML = `<div class="card"><div class="empty"><div class="ico">!</div><h3>检索失败</h3><p>${esc(err.message)}</p></div></div>`;
      } finally {
        btn.disabled = false;
      }
    };
    $("#btnSearch").onclick = doSearch;
    $("#q").addEventListener("keydown", (e) => { if (e.key === "Enter") doSearch(); });
    $("#q").focus();
  });

  // ================= 设置 =================
  let settingsCache = null;
  let settingsSaveTimer = null;
  let settingsSavePromise = null;
  let settingsSaving = false;
  let settingsDirty = false;
  let citationSaveTimer = null;
  let citationSaving = false;
  let citationDirty = false;
  let citationProfiles = {};
  let citationPlatforms = [];
  let citationProfile = localStorage.getItem("larder.citationProfile") || "default";
  const CITE_PREVIEW_FALLBACK = {
    mark: "[1]",
    reference: "张三, 李四, 王五. 组内研究进展[J]. 计算机学报, 2026, 49(3): 1-12.",
    reference_line: "[1] 张三, 李四, 王五. 组内研究进展[J]. 计算机学报, 2026, 49(3): 1-12.",
  };
  const citationPreviewCache = new Map();
  let citationPreviewSeq = 0;
  const CITE_FIELDS = [
    { id: "link", label: "链接", sample: "http://127.0.0.1:8765/r/知识库/文档" },
    { id: "reference", label: "文献词条", sample: "张三等. 论文标题[J]. 期刊, 2026." },
    { id: "file", label: "文献文件名", sample: "论文标题.pdf" },
    { id: "text", label: "原文", sample: "ATE 0.203 m，等价性检验 p = 0.35…" },
  ];
  const CITE_DEFAULT_FIELDS = ["link", "reference", "file", "text"];
  const CITE_NUMERIC_STYLES = new Set(["gb-t-7714-numeric", "ieee", "vancouver"]);

  function defaultCiteStyle() {
    return { mode: "markdown", fields: CITE_DEFAULT_FIELDS.slice(), style: "gb-t-7714-numeric" };
  }

  function normalizeCiteStyle(value, fallback) {
    const v = String(value || "").trim();
    if (ZOTERO_STYLES.some(([id]) => id === v)) return v;
    const f = String(fallback || "").trim();
    return ZOTERO_STYLES.some(([id]) => id === f) ? f : "gb-t-7714-numeric";
  }

  function normalizeCitationProfiles(raw, fallbackStyle) {
    const list = citationPlatforms.length ? citationPlatforms : [{ id: "default", name: "默认" }];
    const out = {};
    const fallback = normalizeCiteStyle(fallbackStyle, "gb-t-7714-numeric");
    for (const p of list) {
      const src = raw && raw[p.id] ? raw[p.id] : {};
      const mode = p.id === "zcode" ? "plain" : (src.mode === "plain" ? "plain" : "markdown");
      // fields 只描述悬浮内容：纯文本模式不使用，但切换回悬浮时应保留。
      const srcFields = Array.isArray(src.fields) ? src.fields : CITE_DEFAULT_FIELDS.slice();
      let fields = srcFields.filter((f) => CITE_FIELDS.some((x) => x.id === f));
      if (fields.includes("link")) {
        fields = ["link", ...fields.filter((f) => f !== "link")];
      }
      out[p.id] = { mode, fields, style: normalizeCiteStyle(src.style, fallback) };
    }
    return out;
  }

  function citationStyle() {
    return citationProfiles[citationProfile] || defaultCiteStyle();
  }

  async function loadCitationPreview() {
    const style = normalizeCiteStyle(citationStyle().style, "gb-t-7714-numeric");
    if (citationPreviewCache.has(style)) {
      renderCitationEditor();
      return;
    }
    const seq = ++citationPreviewSeq;
    try {
      const data = await api("/api/citation/preview?style=" + encodeURIComponent(style));
      if (seq !== citationPreviewSeq) return;
      citationPreviewCache.set(style, data || CITE_PREVIEW_FALLBACK);
    } catch (err) {
      if (seq !== citationPreviewSeq) return;
      // 接口失败时使用内置样例，避免设置页不可用。
      citationPreviewCache.set(style, CITE_PREVIEW_FALLBACK);
    }
    renderCitationEditor();
  }

  function citationPreviewData() {
    const st = citationStyle();
    const style = normalizeCiteStyle(st.style, "gb-t-7714-numeric");
    const preview = citationPreviewCache.get(style) || CITE_PREVIEW_FALLBACK;
    const mark = preview.mark || "[1]";
    const reference = preview.reference_line || preview.reference || "";
    const bodyHTML = st.mode === "markdown" && CITE_NUMERIC_STYLES.has(style)
      ? `<sup>${esc(mark)}</sup>`
      : esc(mark);
    if (st.mode === "markdown") {
      const title = st.fields
        .filter((id) => id !== "link")
        .map((id) => {
          const f = CITE_FIELDS.find((x) => x.id === id);
          return f ? `【${f.label}】…` : "";
        }).join("");
      let hover = title;
      if (!st.fields.includes("link")) {
        hover = "（未勾选“链接”，正文只显示 [1]，没有悬浮内容）";
      } else if (!title) {
        hover = "（未勾选显示内容，悬停没有内容）";
      }
      return { mode: "markdown", body: mark, bodyHTML, hover, reference, style };
    }
    // 纯文本只输出参考文献标准规定的文内标记，不附加任何自定义字段。
    return { mode: "plain", body: mark, bodyHTML, hover: "", reference, style };
  }

  function renderCitationEditor() {
    const el = $("#citationEditor");
    if (!el) return;
    const st = citationStyle();
    const isHover = st.mode === "markdown";
    if (st.fields.includes("link")) {
      st.fields = ["link", ...st.fields.filter((f) => f !== "link")];
    }
    const previews = citationPreviewData();
    const plainMark = previews.body || "[1]";
    let fieldsHTML = "";
    if (isHover) {
      const checks = CITE_FIELDS.map((f) => `<label class="cite-check"><input type="checkbox" data-cite-field="${f.id}" ${st.fields.includes(f.id) ? "checked" : ""}><span>${f.label}</span></label>`).join("");
      const sourceFields = st.fields.slice();
      const linkFixed = sourceFields[0] === "link";
      const order = sourceFields.map((id, i) => {
        const f = CITE_FIELDS.find((x) => x.id === id);
        if (!f) return "";
        const upDisabled = i === 0 || (linkFixed && i === 1);
        const downDisabled = i === sourceFields.length - 1 || (id === "link" && linkFixed);
        return `<div class="cite-order-item"><span>${f.label}${id === "link" && linkFixed ? "（固定第一项）" : ""}</span><span class="cite-order-btns">
          <button class="btn ghost icon sm" data-cite-up="${id}" ${upDisabled ? "disabled" : ""} aria-label="上移">↑</button>
          <button class="btn ghost icon sm" data-cite-down="${id}" ${downDisabled ? "disabled" : ""} aria-label="下移">↓</button>
        </span></div>`;
      }).join("");
      fieldsHTML = `<div class="field" style="margin-top:12px">
          <label>悬浮内容（链接、文献词条、文献文件名、原文；勾选并上下排序；链接固定第一项）</label>
          <div class="cite-checks">${checks}</div>
          <div class="cite-order">${order || `<span class="hint">没有悬浮字段，悬停时不显示内容。</span>`}</div>
        </div>`;
    }
    const refPreview = previews.reference || "（暂无参考文献预览）";
    el.innerHTML = `<div class="field">
        <label>参考文献格式（当前客户端）</label>
        <select class="input" id="citeReferenceStyle">${zStyleOptions(previews.style)}</select>
        <span class="hint">只影响当前客户端 / agent 的正文标记和文末参考文献；切换平台可分别设置。</span>
      </div>
      <div class="field" style="margin-top:12px">
        <label>显示方式</label>
        <div class="cite-modes">
          <button class="btn sm ${isHover ? "primary" : ""}" data-cite-mode="markdown" ${citationProfile === "zcode" ? "disabled" : ""}>悬浮链接</button>
          <button class="btn sm ${!isHover ? "primary" : ""}" data-cite-mode="plain">纯文本</button>
        </div>
        <span class="hint">${citationProfile === "zcode" ? "ZCode 会移除 Markdown 链接，因此固定使用纯文本引用。" : `悬浮链接：正文显示上标引用标记；勾选「链接」后，悬停按下面顺序显示悬浮内容。纯文本：按参考文献格式只输出文内标记和文末参考文献，页面下方不提供悬浮内容设置。`}</span>
      </div>
      ${fieldsHTML}
      <div class="field" style="margin-top:12px">
        <label>预览</label>
        <div class="cite-preview-label">正文预览</div>
        <pre class="cite-preview">${previews.bodyHTML}</pre>
        ${isHover ? `
        <div class="cite-preview-label" style="margin-top:10px">悬浮预览</div>
        <pre class="cite-preview">${esc(previews.hover)}</pre>` : ""}
        <div class="cite-preview-label" style="margin-top:10px">文末参考文献预览</div>
        <pre class="cite-preview">${esc("## 参考文献\n\n" + refPreview)}</pre>
        <span class="hint">${isHover
          ? "正文预览只显示文内引用标记；悬浮预览显示勾选字段；文末参考文献严格按所选格式输出。"
          : `纯文本严格按照参考文献格式输出：正文只有 ${plainMark}，文末输出完整参考文献；不再附加链接、文献词条、文件名或原文。`}</span>
      </div>`;
    const styleSel = $("#citeReferenceStyle");
    if (styleSel) {
      styleSel.onchange = () => {
        st.style = styleSel.value;
        markCitationDirty();
        loadCitationPreview();
      };
    }
    $$("[data-cite-mode]", el).forEach((btn) => {
      btn.onclick = () => {
        st.mode = btn.dataset.citeMode;
        markCitationDirty(); renderCitationEditor();
      };
    });
    $$("[data-cite-field]", el).forEach((cb) => {
      cb.onchange = () => {
        const id = cb.dataset.citeField;
        if (cb.checked) {
          if (!st.fields.includes(id)) {
            if (id === "link") {
              st.fields.unshift("link");
            } else {
              st.fields.push(id);
            }
          }
        } else {
          st.fields = st.fields.filter((x) => x !== id);
        }
        markCitationDirty(); renderCitationEditor();
      };
    });
    $$("[data-cite-up]", el).forEach((btn) => {
      btn.onclick = () => {
        const i = st.fields.indexOf(btn.dataset.citeUp);
        if (i > 0) { [st.fields[i - 1], st.fields[i]] = [st.fields[i], st.fields[i - 1]]; markCitationDirty(); renderCitationEditor(); }
      };
    });
    $$("[data-cite-down]", el).forEach((btn) => {
      btn.onclick = () => {
        const i = st.fields.indexOf(btn.dataset.citeDown);
        if (i >= 0 && i < st.fields.length - 1) { [st.fields[i], st.fields[i + 1]] = [st.fields[i + 1], st.fields[i]]; markCitationDirty(); renderCitationEditor(); }
      };
    });
  }

  function markCitationDirty() {
    citationDirty = true;
    setSaveState("修改后自动保存…", "saving");
    if (citationSaveTimer) clearTimeout(citationSaveTimer);
    citationSaveTimer = setTimeout(() => {
      citationSaveTimer = null;
      saveCitationNow();
    }, 60);
  }

  async function saveCitationNow() {
    if (citationSaving) return;
    citationSaving = true;
    try {
      while (citationDirty) {
        citationDirty = false;
        try {
          const res = await api("/api/citation", {
            method: "PUT", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ profiles: citationProfiles }),
          });
          if (settingsCache) settingsCache.citation = res;
          setSaveState("已自动保存 · " + new Date().toLocaleTimeString(), "ok");
        } catch (err) {
          citationDirty = true;
          setSaveState("保存失败：" + err.message, "err");
          break;
        }
      }
    } finally {
      citationSaving = false;
      if (citationDirty && !citationSaveTimer) {
        citationSaveTimer = setTimeout(() => {
          citationSaveTimer = null;
          saveCitationNow();
        }, 1200);
      }
    }
  }

  function renderCitationPlatforms() {
    const wrap = $("#citationPlatforms");
    if (!wrap) return;
    wrap.innerHTML = citationPlatforms.map((p) => `<button class="btn sm ${p.id === citationProfile ? "primary" : ""}" data-cite-platform="${esc(p.id)}">${esc(p.name)}</button>`).join("");
    $$("[data-cite-platform]", wrap).forEach((btn) => {
      btn.onclick = () => {
        citationProfile = btn.dataset.citePlatform;
        localStorage.setItem("larder.citationProfile", citationProfile);
        renderCitationPlatforms();
        renderCitationEditor();
        loadCitationPreview();
      };
    });
  }

  function bindCitationSection(s) {
    citationPlatforms = (s.citation_platforms && s.citation_platforms.length) ? s.citation_platforms : [
      { id: "default", name: "默认" }, { id: "zcode", name: "ZCode" }, { id: "cherrystudio", name: "Cherry Studio" },
      { id: "claude-desktop", name: "Claude Desktop" }, { id: "claude-code", name: "Claude Code" },
      { id: "cursor", name: "Cursor" }, { id: "dsh", name: "DSH" },
    ];
    const fallbackStyle = (s.citation && s.citation.profiles && s.citation.profiles.default && s.citation.profiles.default.style) || (s.citation && s.citation.style) || (s.zotero && s.zotero.cite_style) || "gb-t-7714-numeric";
    citationProfiles = normalizeCitationProfiles(s.citation && s.citation.profiles, fallbackStyle);
    if (!citationProfiles[citationProfile]) citationProfile = "default";
    renderCitationPlatforms();
    renderCitationEditor();
    loadCitationPreview();
  }

  const settingsKeys = { parse: [], embed: [], rerank: [] };
  const keysDirty = { parse: false, embed: false, rerank: false };
  const keyLoadError = { parse: "", embed: "", rerank: "" };
  const revealedKeys = {};

  function setSaveState(text, kind) {
    const el = $("#saveState");
    if (!el) return;
    el.className = "save-state " + (kind || "");
    el.innerHTML = `<i></i><span>${esc(text)}</span>`;
  }

  function maskKey(k) {
    if (!k) return "";
    if (k.length <= 8) return "••••••••";
    return k.slice(0, 2) + "****" + k.slice(-4);
  }

  function keyHelpURL(kind) {
    const base = (svcValue(kind, "base_url") || "").toLowerCase();
    if (kind === "parse") {
      if (base.includes("mineru")) return "https://mineru.net/apiManage";
      return "";
    }
    if (base.includes("siliconflow")) return "https://cloud.siliconflow.cn/account/ak";
    if (base.includes("openai")) return "https://platform.openai.com/api-keys";
    if (base.includes("moonshot")) return "https://platform.moonshot.cn/console/api-keys";
    if (base.includes("bigmodel")) return "https://open.bigmodel.cn/usercenter/apikeys";
    if (base.includes("dashscope")) return "https://bailian.console.aliyun.com/";
    return "";
  }

  const KEY_ICONS = {
    copy: `<svg viewBox="0 0 20 20" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="7" y="7" width="9" height="9" rx="1.6"/><path d="M4.5 13H4a1.5 1.5 0 0 1-1.5-1.5V4A1.5 1.5 0 0 1 4 2.5h7.5A1.5 1.5 0 0 1 13 4v.5"/></svg>`,
    edit: `<svg viewBox="0 0 20 20" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M13.5 3.5l3 3L7 16H4v-3z"/><path d="M11.5 5.5l3 3"/></svg>`,
    trash: `<svg viewBox="0 0 20 20" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M4.5 6h11M8 6V4.5h4V6M6 6l.7 9.5h6.6L14 6M8.5 8.5v5M11.5 8.5v5"/></svg>`,
    eye: `<svg viewBox="0 0 20 20" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 10S5.5 4.5 10 4.5 17.5 10 17.5 10 14.5 15.5 10 15.5 2.5 10 2.5 10z"/><circle cx="10" cy="10" r="2.3"/></svg>`,
    eyeoff: `<svg viewBox="0 0 20 20" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 10S5.5 4.5 10 4.5c1.8 0 3.4 1 4.7 2.2M17.5 10S14.5 15.5 10 15.5c-1.9 0-3.6-1.1-4.9-2.4M3 3l14 14"/></svg>`,
    key: `<svg viewBox="0 0 20 20" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="7" cy="8" r="2.6"/><path d="M9 8h8M14 8v2.5M16.5 8v2"/></svg>`,
  };

  function keyManagerHTML(kind) {
    return `<div class="key-head">
      <span class="label">API 密钥</span>
      <a class="link" data-key-help="${kind}" target="_blank" rel="noreferrer" hidden>获取密钥</a>
    </div>
    <div class="key-list" id="keys-${kind}"><div class="key-empty">正在读取密钥…</div></div>`;
  }

  function clearRevealed(kind) {
    Object.keys(revealedKeys).forEach((k) => { if (k.startsWith(kind + ":")) delete revealedKeys[k]; });
  }

  function keyRowHTML(kind, key, index) {
    const show = !!revealedKeys[kind + ":" + index];
    return `<div class="key-row ${index === 0 ? "active" : ""}" data-key-index="${index}">
      <span class="key-state" title="${index === 0 ? "当前使用" : "未使用"}"></span>
      <span class="key-val">${esc(show ? key : maskKey(key))}</span>
      ${index === 0 ? `<span class="key-cur">当前</span>` : ""}
      <div class="key-actions">
        <button class="key-btn" data-key-act="copy" title="复制">${KEY_ICONS.copy}</button>
        <button class="key-btn" data-key-act="edit" title="修改">${KEY_ICONS.edit}</button>
        <button class="key-btn" data-key-act="del" title="删除">${KEY_ICONS.trash}</button>
        <button class="key-btn" data-key-act="eye" title="${show ? "隐藏" : "显示"}">${show ? KEY_ICONS.eyeoff : KEY_ICONS.eye}</button>
        <button class="key-btn" data-key-act="use" title="设为当前" ${index === 0 ? "disabled" : ""}>${KEY_ICONS.key}</button>
        <button class="btn sm" data-key-act="test">检测</button>
      </div>
    </div>`;
  }

  function renderKeyManager(kind) {
    const box = view.querySelector("#keys-" + kind);
    if (!box) return;
    const keys = settingsKeys[kind] || [];
    const rows = keys.map((k, i) => keyRowHTML(kind, k, i)).join("");
    const err = keyLoadError[kind];
    box.innerHTML = `${err ? `<div class="key-empty err">${esc(err)}</div>` : ""}
      ${rows || `<div class="key-empty">还没有密钥，添加一个后即可使用。</div>`}
      <div class="key-add">
        <input class="input mono" id="newKey-${kind}" type="password" autocomplete="off" placeholder="粘贴新的 API 密钥">
        <button class="btn sm" data-key-act="add">添加</button>
      </div>`;
    const help = keyHelpURL(kind);
    const link = view.querySelector(`[data-key-help="${kind}"]`);
    if (link) {
      link.hidden = !help;
      if (help) link.href = help;
    }
    bindKeyActions(kind);
  }

  function bindKeyActions(kind) {
    const box = view.querySelector("#keys-" + kind);
    if (!box) return;
    box.querySelectorAll("[data-key-act]").forEach((btn) => {
      btn.onclick = async (e) => {
        e.stopPropagation();
        const act = btn.dataset.keyAct;
        const row = btn.closest(".key-row");
        const idx = row ? Number(row.dataset.keyIndex) : -1;
        const keys = settingsKeys[kind] || [];
        if (act === "add") {
          const inp = box.querySelector(`#newKey-${kind}`);
          const val = inp ? inp.value.trim() : "";
          if (!val) { if (inp) inp.focus(); return; }
          settingsKeys[kind] = keys.concat([val]);
          keysDirty[kind] = true;
          clearRevealed(kind);
          renderKeyManager(kind);
          scheduleSettingsSave(120);
          return;
        }
        if (idx < 0 || !keys[idx]) return;
        if (act === "copy") { await copyText(keys[idx]); return; }
        if (act === "eye") {
          revealedKeys[kind + ":" + idx] = !revealedKeys[kind + ":" + idx];
          renderKeyManager(kind);
          return;
        }
        if (act === "use") {
          if (idx === 0) return;
          const k = keys.splice(idx, 1)[0];
          keys.unshift(k);
          keysDirty[kind] = true;
          clearRevealed(kind);
          renderKeyManager(kind);
          scheduleSettingsSave(120);
          return;
        }
        if (act === "del") {
          keys.splice(idx, 1);
          keysDirty[kind] = true;
          clearRevealed(kind);
          renderKeyManager(kind);
          scheduleSettingsSave(120);
          return;
        }
        if (act === "edit") { openKeyEdit(kind, idx); return; }
        if (act === "test") { await testKey(kind, keys[idx]); return; }
      };
    });
  }

  function openKeyEdit(kind, idx) {
    const keys = settingsKeys[kind] || [];
    const cur = keys[idx] || "";
    const m = modal({
      title: "修改 API 密钥",
      sub: "密钥只保存在本机 larder.toml。",
      body: `<div class="field"><label>密钥</label><input class="input mono" id="editKey" value="${esc(cur)}"></div>`,
      actions: `<button class="btn" data-act="cancel">取消</button><button class="btn primary" data-act="ok">保存</button>`,
    });
    const input = m.el.querySelector("#editKey");
    input.focus(); input.select();
    m.el.querySelector("[data-act=cancel]").onclick = m.close;
    m.el.querySelector("[data-act=ok]").onclick = () => {
      const val = input.value.trim();
      if (!val) { toast("密钥不能为空", "err"); return; }
      keys[idx] = val;
      keysDirty[kind] = true;
      clearRevealed(kind);
      m.close();
      renderKeyManager(kind);
      scheduleSettingsSave(120);
    };
  }

  async function testKey(kind, key) {
    const base = svcValue(kind, "base_url");
    if (!base) { toast("请先填写 API 地址", "err"); return; }
    const model = kind === "parse" ? "" : svcValue(kind, "model");
    toast("正在检测密钥…", "");
    try {
      const res = await api("/api/services/test", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ kind, base_url: base, api_key: key, model, path: (settingsCache[kind] || {}).path || "" }),
      });
      const label = kind === "embed" ? "向量服务" : kind === "rerank" ? "重排服务" : "解析服务";
      let extra = "";
      if (res.dim) extra = `，维度 ${res.dim}`;
      if (res.latency_ms !== undefined) extra += `，${res.latency_ms}ms`;
      toast(`${label}密钥有效${extra}`, "ok");
    } catch (err) {
      toast("密钥检测失败：" + err.message, "err");
    }
  }

  async function loadKeys(kind) {
    try {
      const res = await api(`/api/settings/keys?service=${kind}`);
      settingsKeys[kind] = res.keys || [];
      keyLoadError[kind] = "";
    } catch (err) {
      keyLoadError[kind] = "无法读取已保存密钥：" + err.message;
      settingsKeys[kind] = [];
    }
    renderKeyManager(kind);
  }

  function modelField(kind, svc) {
    const isRerank = kind === "rerank";
    const cur = svc.model || "";
    return `<div class="field">
      <label>${isRerank ? "重排模型" : "向量模型"}</label>
      <div style="display:flex;gap:8px">
        <select class="select mono" data-svc="${kind}" data-field="model" aria-label="${isRerank ? "重排模型" : "向量模型"}">
          ${cur ? `<option value="${esc(cur)}" selected>${esc(cur)}</option>` : `<option value="">（填写地址和密钥后自动获取）</option>`}
        </select>
        <button class="btn sm" data-fetch="${kind}" title="拉取模型列表">刷新模型</button>
      </div>
      <span class="hint" data-fetch-status="${kind}"></span>
    </div>`;
  }

  function svcRows(kind, svc) {
    return `<div class="row stack">
      <div class="field"><label>API 地址</label>
        <input class="input mono" data-svc="${kind}" data-field="base_url" value="${esc(svc.base_url || "")}"
          placeholder="https://api.siliconflow.cn/v1"></div>
      ${modelField(kind, svc)}
      ${keyManagerHTML(kind)}
    </div>`;
  }

  route("/settings", async () => {
    markNav("settings");
    const s = await api("/api/settings");
    settingsCache = s;
    const srv = s.server || {};
    const parse = s.parse || {};
    const embed = s.embed || {};
    const rerank = s.rerank || {};
    const mcpEndpoint = "http://" + (srv.addr || "127.0.0.1:8765") + (srv.mcp_path || "/mcp");

    view.innerHTML = `<div class="wrap">
      <div class="page-head">
        <div><div class="eyebrow">Settings</div><h1 class="title">设置</h1>
          <p class="desc">填写 API 地址与密钥即可，修改后自动保存。解析、向量化、重排三个服务都通过 API 接入；模型会自动获取并选择。</p></div>
        <div class="head-actions"><span class="save-state" id="saveState"><i></i><span>修改后自动保存</span></span></div>
      </div>

      <section class="settings-section">
        <div class="sec-head"><h2>文档处理流水线</h2><p>每个文件依次经过以下环节，各环节状态会记录到文档上。</p></div>
        <div class="pipe">
          <div class="pipe-step stage-parse"><div class="pipe-step-head"><span class="pipe-idx">1</span><span class="pipe-name">解析</span></div>
            <div class="pipe-val" id="pipeParseVal">${esc(parseProvider(parse.provider))}</div>
            <div class="pipe-foot" id="pipeParseFoot">${chip(parse.provider === "plain" ? "内置解析 · 无需模型" : "内置优先 + 云 OCR 兜底", "stage-parse")}</div></div>
          <div class="pipe-step stage-embed"><div class="pipe-step-head"><span class="pipe-idx">2</span><span class="pipe-name">切块 + 向量化</span></div>
            <div class="pipe-val" id="pipeEmbedVal">${esc(embed.model || "未配置模型")}</div>
            <div class="pipe-foot" id="pipeEmbedFoot">${chip(embed.dim ? `维度 ${embed.dim}` : "维度自动探测", "stage-embed")}</div></div>
          <div class="pipe-step stage-rerank"><div class="pipe-step-head"><span class="pipe-idx">3</span><span class="pipe-name">重排</span></div>
            <div class="pipe-val" id="pipeRerankVal">${esc(rerank.enabled ? (rerank.model || "已启用") : "未启用")}</div>
            <div class="pipe-foot" id="pipeRerankFoot">${chip(rerank.enabled ? "检索时应用" : "仅向量召回", "stage-rerank")}</div></div>
        </div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>解析服务</h2><p>只填 API 地址和密钥。填 mineru.net 会自动使用 MinerU 云 OCR；留空则只用内置解析（无需模型）。</p></div>
        <div class="card group">
          <div class="row stack">
            <div class="field"><label>API 地址</label>
              <input class="input mono" data-svc="parse" data-field="base_url" value="${esc(parse.base_url || "")}"
                placeholder="https://mineru.net（留空 = 仅内置解析）"></div>
            ${keyManagerHTML("parse")}
            <p class="hint" id="parseHint"></p>
            <p class="hint">内置解析是程序自带的纯 Go 文本抽取，不使用模型，不依赖本机 Python/Office；扫描件才需要 MinerU 云 OCR。</p>
          </div>
        </div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>向量化服务</h2><p>OpenAI 兼容的 /v1/embeddings，是入库流程的必需环节。填好地址与密钥后会自动获取并选择模型。</p></div>
        <div class="card group">${svcRows("embed", embed)}</div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>重排服务</h2><p>可选的 OpenAI 兼容 /v1/rerank。没有配置时检索退化为纯向量召回，仍可正常使用。</p></div>
        <div class="card group">
          <div class="row">
            <div class="row-main"><div class="row-title">启用重排</div><div class="row-sub">检索时对候选片段重新排序；每个知识库还可以单独开关。</div></div>
            <div class="row-control">
              <label class="switch"><input type="checkbox" data-svc="rerank" data-field="enabled" ${rerank.enabled ? "checked" : ""}><span class="track"></span><span class="thumb"></span></label>
            </div>
          </div>
          ${svcRows("rerank", rerank)}
        </div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>MCP 服务</h2><p>供 Claude Desktop / Claude Code / Cursor 等客户端接入的知识库工具服务。开关后立即生效。</p></div>
        <div class="card group">
          <div class="row">
            <div class="row-main"><div class="row-title">启动 MCP 服务器</div><div class="row-sub">关闭后 /mcp 返回 404，已经连接的客户端会断开。</div></div>
            <div class="row-control">
              <span class="badge ${srv.mcp_enabled ? "on" : "off"}" id="mcpBadge">${srv.mcp_enabled ? "已启动" : "已停止"}</span>
              <label class="switch"><input type="checkbox" id="mcpToggle" ${srv.mcp_enabled ? "checked" : ""}><span class="track"></span><span class="thumb"></span></label>
            </div>
          </div>
          <div class="row">
            <div class="row-main"><div class="row-title">一键注入 Agent</div>
              <div class="row-sub">把当前 MCP 端点写入 Claude Code / Claude Desktop / Cursor / ZCode / Cherry Studio / DSH，或复制通用 JSON。</div></div>
            <div class="row-control"><button class="btn" id="btnMcpInject">选择 Agent</button></div>
          </div>
          <div class="row stack">
            <div class="field"><label>MCP 端点（Streamable HTTP）</label>
              <div class="copy-row">
                <input class="input mono" id="mcpEndpoint" value="${esc(mcpEndpoint)}" readonly>
                <button class="btn sm" id="btnCopyMcp">复制</button>
              </div>
            </div>
            <p class="hint">Claude Code 示例：<code style="font-family:var(--code);font-size:11.5px">claude mcp add --transport http larder http://${esc(mcpEndpoint.replace(/^https?:\/\//, ""))}</code></p>
            <p class="hint">首次检索如果存在多个文献库，ZCode 等支持 MCP elicitation 的客户端会弹出可点击选项；不支持的客户端回退为库列表，也可调用 <code>larder_select_library</code> 选择。回答右上角会带 📚 Larder · 知识库 小标，引用片段含文档名、文件与章节。</p>
            <p class="hint">MCP 会返回 Markdown 右对齐小标（表格形式），Cherry 会把它显示在回答右上角；更新 MCP 后请在 Cherry 中重连或重启，并新建会话。</p>
            <p class="hint">ZCode 会移除 Markdown 链接，因此 ZCode 固定使用纯文本引用；正文只保留文内标记，文末按所选参考文献格式输出。修改后请重新注入 ZCode 以更新地址。</p>
          </div>
        </div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>引用格式</h2><p>按客户端 / agent 分别设置参考文献格式与显示方式，各平台的格式互不影响。纯文本严格按该平台的参考文献格式输出：正文只有文内引用标记，文末输出完整参考文献；悬浮链接额外支持自定义悬浮内容，并显示正文、悬浮和文末参考文献三个预览。保存后请在该平台重新执行一次一键注入，使 MCP 地址带上对应的平台标识。</p></div>
        <div class="card card-pad">
          <div class="citation-platforms" id="citationPlatforms"></div>
          <div id="citationEditor"></div>
        </div>
      </section>

      <section class="settings-section">
        <div class="sec-head"><h2>运行信息</h2><p>程序参数、数据与日志都在 exe 同级目录，便于备份和排查。</p></div>
        <div class="card card-pad">
          <dl class="kv">
            <dt>版本</dt><dd>${esc(srv.version || "")}</dd>
            <dt>数据目录</dt><dd>${esc(srv.data_dir || "")}</dd>
            <dt>参数文件</dt><dd>${esc(srv.config_path || "")}</dd>
            <dt>日志文件</dt><dd>${esc(srv.log_path || "")}</dd>
            <dt>监听地址</dt><dd>${esc(srv.addr || "")}</dd>
          </dl>
        </div>
      </section>
    </div>`;

    bindSettings(s);
    bindCitationSection(s);
    setSaveState("修改后自动保存", "");
  });

  function svcValue(kind, field) {
    const el = view.querySelector(`[data-svc="${kind}"][data-field="${field}"]`);
    if (!el) return "";
    if (el.type === "checkbox") return el.checked;
    if (el.type === "number") return Number(el.value) || 0;
    return el.value.trim();
  }

  function setFetchStatus(kind, text, cls) {
    const el = view.querySelector(`[data-fetch-status="${kind}"]`);
    if (!el) return;
    el.textContent = text || "";
    el.className = "hint" + (cls ? " " + cls : "");
  }

  function bestModel(kind, models) {
    if (!models.length) return "";
    if (kind === "rerank") return models.find((m) => /(rerank|ranker)/i.test(m)) || models[0];
    // 向量模型优先多语言 bge-m3，其次通用 embedding，避免默认选到纯英文模型。
    return models.find((m) => /bge-m3/i.test(m))
      || models.find((m) => /embedding/i.test(m))
      || models.find((m) => /(embed|bge|m3e|gte|e5|voyage|jina|nomic)/i.test(m))
      || models[0];
  }

  const modelCache = new Map();
  const MODEL_CACHE_MS = 5 * 60 * 1000;

  function applyModels(kind, models) {
    const sel = view.querySelector(`select[data-svc="${kind}"][data-field="model"]`);
    if (!sel) return;
    const cur = sel.value.trim();
    const list = [];
    if (cur) list.push(cur);
    for (const m of models) if (!list.includes(m)) list.push(m);
    sel.innerHTML = list.map((m) => `<option value="${esc(m)}">${esc(m)}</option>`).join("");
    const pick = cur && list.includes(cur) ? cur : bestModel(kind, models);
    if (pick) sel.value = pick;
    if (sel.value && sel.value !== cur) {
      settingsDirty = true;
      scheduleSettingsSave(250);
    }
  }

  async function fetchModels(kind, manual) {
    const base = svcValue(kind, "base_url");
    const key = (settingsKeys[kind] && settingsKeys[kind][0]) || "";
    if (!base) {
      if (manual) toast("请先填写 API 地址", "err");
      return;
    }
    const cacheKey = `${kind}|${base}|${key ? key.length + ":" + key.slice(-4) : ""}`;
    const cached = modelCache.get(cacheKey);
    if (!manual && cached && Date.now() - cached.at < MODEL_CACHE_MS) {
      applyModels(kind, cached.models);
      setFetchStatus(kind, `已使用缓存的 ${cached.models.length} 个可用模型`, "ok");
      return;
    }
    setFetchStatus(kind, "正在获取模型…", "");
    const btns = $$(`[data-fetch="${kind}"]`);
    btns.forEach((b) => (b.disabled = true));
    try {
      const res = await api("/api/models", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ base_url: base, api_key: key, kind }),
      });
      const models = res.models || [];
      modelCache.set(cacheKey, { at: Date.now(), models });
      applyModels(kind, models);
      setFetchStatus(kind, `已获取 ${models.length} 个可用模型，已自动选中`, "ok");
      if (manual) toast(`已获取 ${models.length} 个可用模型`, "ok");
    } catch (err) {
      setFetchStatus(kind, "获取模型失败：" + err.message, "err");
      if (manual) toast(err.message, "err");
    } finally {
      btns.forEach((b) => (b.disabled = false));
    }
  }

  let modelTimer = null;
  function scheduleModelFetch(kind) {
    if (modelTimer) clearTimeout(modelTimer);
    modelTimer = setTimeout(() => { modelTimer = null; fetchModels(kind, false); }, 700);
  }

  function updateParseHint() {
    const el = $("#parseHint");
    if (!el) return;
    const base = svcValue("parse", "base_url");
    if (!base) {
      el.textContent = "留空：仅使用内置解析（纯 Go，无需模型）；内置支持 Markdown/文本/Office/文字版 PDF，扫描件需要配置 MinerU。";
      return;
    }
    const l = base.toLowerCase();
    const name = l.includes("mineru") ? "MinerU" : l.includes("docling") ? "Docling" : l.includes("tika") ? "Tika" : "MinerU（自定义地址按 MinerU 协议）";
    el.textContent = `已识别为 ${name}：内置解析器能处理的格式由程序自己处理，扫描件/矢量 PDF 自动上传该 API 做 OCR；不依赖本机模型、Python 或 Office。`;
  }

  async function openMcpInject() {
    let data;
    try {
      data = await api("/api/mcp/clients");
    } catch (err) {
      toast("读取 Agent 列表失败：" + err.message, "err");
      return;
    }
    const endpoint = data.endpoint || (settingsCache && settingsCache.server && settingsCache.server.mcp_url) || "";
    const clients = (data.clients || []).filter((c) => c.id !== "generic");
    const generic = JSON.stringify({ mcpServers: { larder: { url: endpoint } } }, null, 2);
    const enabled = !settingsCache || !settingsCache.server || settingsCache.server.mcp_enabled !== false;
    const rows = clients.map((c) => `
      <div class="agent-row">
        <div class="agent-main">
          <div class="agent-name">${esc(c.name)} ${c.available ? chip("可用", "ok") : chip("不可用", "off")}</div>
          <div class="agent-detail">${esc(c.detail || "")}</div>
          ${c.path ? `<div class="agent-path">${esc(c.path)}</div>` : ""}
          <div class="agent-detail" data-agent-result="${esc(c.id)}"></div>
        </div>
        <div class="agent-actions">
          <button class="btn sm" data-copy-agent="${esc(c.id)}" title="复制该客户端的 JSON 配置">复制 JSON</button>
          <button class="btn sm ${c.available ? "primary" : ""}" data-agent="${esc(c.id)}" ${c.available ? "" : "disabled"} style="${c.installed ? "display:none" : ""}">注入</button>
          <button class="btn sm" data-uninstall="${esc(c.id)}" title="从该客户端移除 larder" style="${c.installed ? "" : "display:none"}">取消注入</button>
        </div>
      </div>`).join("");

    const body = `${enabled ? "" : `<div class="hint err" style="margin-bottom:10px">MCP 当前已停止；注入后请回到设置页启用 MCP。</div>`}
      <div class="hint" style="margin-bottom:10px">当前端点：<code>${esc(endpoint)}</code></div>
      <div class="agent-list">${rows}</div>
      <div class="field" style="margin-top:16px">
        <label>通用 JSON 配置</label>
        <pre class="md-box" style="max-height:150px;margin:0">${esc(generic)}</pre>
        <div class="inline" style="justify-content:flex-end;margin-top:7px">
          <button class="btn sm" id="btnCopyGenericMcp">复制 JSON</button>
        </div>
        <span class="hint">通用 JSON 适用于支持远程 HTTP MCP 的客户端；Claude Desktop 请使用上面的自动写入（通过 mcp-remote 桥接，需要 Node.js）。</span>
      </div>`;
    const m = modal({
      title: "一键接入 Agent",
      sub: "会修改本机客户端的 MCP 配置文件；写入后重启对应客户端生效。",
      body,
      wide: true,
      actions: `<button class="btn" data-act="close">关闭</button>`,
    });
    const copyGeneric = m.el.querySelector("#btnCopyGenericMcp");
    if (copyGeneric) copyGeneric.onclick = () => copyText(generic);
    m.el.querySelectorAll("[data-copy-agent]").forEach((btn) => {
      btn.onclick = () => {
        const c = clients.find((x) => x.id === btn.dataset.copyAgent);
        const cfg = (c && c.config) || { mcpServers: { larder: { url: endpoint } } };
        copyText(JSON.stringify(cfg, null, 2));
      };
    });

    m.el.querySelectorAll("[data-agent]").forEach((btn) => {
      btn.onclick = async () => {
        const id = btn.dataset.agent;
        const old = btn.textContent;
        btn.disabled = true;
        btn.textContent = "写入中…";
        try {
          const res = await api("/api/mcp/install", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ client: id }),
          });
          toast(res.message || "已注入", "ok");
          btn.textContent = "已注入";
          btn.classList.remove("primary");
          btn.style.display = "none";
          const cancelBtn = m.el.querySelector(`[data-uninstall="${id}"]`);
          if (cancelBtn) cancelBtn.style.display = "";
          const box = m.el.querySelector(`[data-agent-result="${id}"]`);
          if (box) {
            box.classList.add("ok");
            box.textContent = (res.message || "已写入") + (res.path ? " 路径：" + res.path : "");
            if (res.config) {
              const pre = document.createElement("pre");
              pre.className = "md-box";
              pre.style.maxHeight = "120px";
              pre.style.marginTop = "7px";
              pre.textContent = JSON.stringify(res.config, null, 2);
              box.appendChild(pre);
            }
          }
        } catch (err) {
          toast(err.message, "err");
          btn.disabled = false;
          btn.textContent = old;
        }
      };
    });
    m.el.querySelectorAll("[data-uninstall]").forEach((btn) => {
      btn.onclick = async () => {
        const id = btn.dataset.uninstall;
        const c = clients.find((x) => x.id === id);
        const name = (c && c.name) || id;
        if (!confirm(`确定取消 ${name} 的 larder MCP 注入？`)) return;
        btn.disabled = true;
        const old = btn.textContent;
        btn.textContent = "取消中…";
        try {
          const res = await api("/api/mcp/uninstall", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ client: id }),
          });
          toast(res.message || "已取消注入", "ok");
          const inject = m.el.querySelector(`[data-agent="${id}"]`);
          if (inject) { inject.textContent = "注入"; inject.disabled = !(c && c.available); inject.classList.toggle("primary", !!(c && c.available)); inject.style.display = ""; }
          btn.style.display = "none";
          const box = m.el.querySelector(`[data-agent-result="${id}"]`);
          if (box) { box.classList.remove("ok"); box.textContent = res.message || "已取消注入"; }
        } catch (err) {
          toast(err.message, "err");
        } finally {
          btn.disabled = false;
          btn.textContent = old;
        }
      };
    });
  }

  function bindSettings(s) {
    const mcpToggle = $("#mcpToggle");
    if (mcpToggle) {
      mcpToggle.onchange = () => {
        const b = $("#mcpBadge");
        if (b) { b.className = "badge " + (mcpToggle.checked ? "on" : "off"); b.textContent = mcpToggle.checked ? "已启动" : "已停止"; }
        scheduleSettingsSave(200);
      };
    }
    const copyBtn = $("#btnCopyMcp");
    if (copyBtn) copyBtn.onclick = () => copyText($("#mcpEndpoint").value);
    const injectBtn = $("#btnMcpInject");
    if (injectBtn) injectBtn.onclick = openMcpInject;

    // 所有服务输入自动保存；select/checkbox 用 change，文本用 input。
    $$("[data-svc]").forEach((el) => {
      const kind = el.dataset.svc;
      const field = el.dataset.field;
      const ev = (el.tagName === "SELECT" || el.type === "checkbox") ? "change" : "input";
      el.addEventListener(ev, () => {
        if (kind === "parse" && field === "base_url") {
          updateParseHint();
          updateKeyHelp("parse");
        }
        if (field === "base_url") updateKeyHelp(kind);
        scheduleSettingsSave(550);
      });
    });

    // 输入 API 地址后自动拉取模型；模型下拉可以手动改。
    $$('[data-svc][data-field="base_url"]').forEach((el) => {
      const kind = el.dataset.svc;
      if (kind === "parse") return;
      el.addEventListener("input", () => scheduleModelFetch(kind));
    });

    $$("[data-fetch]").forEach((btn) => {
      btn.onclick = () => fetchModels(btn.dataset.fetch, true);
    });

    updateParseHint();

    // 读取三条服务的密钥列表（含明文，仅本机接口）；渲染多密钥管理。
    ["parse", "embed", "rerank"].forEach((kind) => loadKeys(kind));

    // 有地址就拉取模型列表，让下拉框直接可手动选择。
    ["embed", "rerank"].forEach((kind) => {
      if (svcValue(kind, "base_url")) fetchModels(kind, false);
    });
  }

  function updateKeyHelp(kind) {
    const link = view.querySelector(`[data-key-help="${kind}"]`);
    if (!link) return;
    const help = keyHelpURL(kind);
    link.hidden = !help;
    if (help) link.href = help;
  }

  function collectService(kind) {
    const base = settingsCache && settingsCache[kind] ? settingsCache[kind] : {};
    const out = {
      enabled: kind === "rerank" ? svcValue("rerank", "enabled") : true,
      provider: kind === "parse" ? "" : (base.provider || "openai"),
      base_url: svcValue(kind, "base_url"),
      model: kind === "parse" ? (base.model || "") : svcValue(kind, "model"),
      path: base.path || "",
      dim: kind === "embed" ? (base.dim || 0) : 0,
      timeout: base.timeout || "",
      max_retries: base.max_retries || 0,
      qps: base.qps || 0,
    };
    if (keysDirty[kind]) out.api_keys = (settingsKeys[kind] || []).slice();
    return out;
  }

  function updatePipelineSummary(res) {
    const pv = $("#pipeParseVal");
    if (pv) pv.textContent = parseProvider(res.parse.provider);
    const pf = $("#pipeParseFoot");
    if (pf) pf.innerHTML = chip(res.parse.provider === "plain" ? "内置解析 · 无需模型" : "内置优先 + 云 OCR 兜底", "stage-parse");
    const ev = $("#pipeEmbedVal");
    if (ev) ev.textContent = res.embed.model || "未配置模型";
    const ef = $("#pipeEmbedFoot");
    if (ef) ef.innerHTML = chip(res.embed.dim ? `维度 ${res.embed.dim}` : "维度自动探测", "stage-embed");
    const rv = $("#pipeRerankVal");
    if (rv) rv.textContent = res.rerank.enabled ? (res.rerank.model || "已启用") : "未启用";
    const rf = $("#pipeRerankFoot");
    if (rf) rf.innerHTML = chip(res.rerank.enabled ? "检索时应用" : "仅向量召回", "stage-rerank");
  }

  function scheduleSettingsSave(delay) {
    if (currentKey !== "/settings") return;
    settingsDirty = true;
    if (settingsSaveTimer) clearTimeout(settingsSaveTimer);
    setSaveState("修改后自动保存…", "saving");
    settingsSaveTimer = setTimeout(() => {
      settingsSaveTimer = null;
      saveSettingsNow();
    }, delay || 600);
  }

  async function saveSettingsNow() {
    if (currentKey !== "/settings") return null;
    if (settingsSaving) {
      settingsDirty = true;
      return settingsSavePromise;
    }
    const dirtyKinds = ["parse", "embed", "rerank"].filter((k) => keysDirty[k]);
    const parse = collectService("parse");
    const embed = collectService("embed");
    const rerank = collectService("rerank");
    if (!embed.base_url) {
      setSaveState("请填写向量服务 API 地址", "err");
      return null;
    }
    if (rerank.enabled && !rerank.base_url) {
      setSaveState("请填写重排服务 API 地址", "err");
      return null;
    }
    const mcpToggle = $("#mcpToggle");
    const payload = {
      server: { mcp_enabled: mcpToggle ? mcpToggle.checked : true },
      parse, embed, rerank,
      citation: { profiles: citationProfiles },
    };
    settingsSaving = true;
    settingsDirty = false;
    setSaveState("保存中…", "saving");
    const p = (async () => {
      try {
        const res = await api("/api/settings", {
          method: "PUT", headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        settingsCache = res;
        updatePipelineSummary(res);
        dirtyKinds.forEach((k) => {
          if (keysDirty[k]) return; // 保存期间又改过，等下一次保存
          clearRevealed(k);
          loadKeys(k);
        });
        setSaveState("已自动保存 · " + new Date().toLocaleTimeString(), "ok");
        checkHealth();
        return res;
      } catch (err) {
        settingsDirty = true;
        setSaveState("保存失败：" + err.message, "err");
        return null;
      } finally {
        settingsSaving = false;
        if (settingsDirty && !settingsSaveTimer && currentKey === "/settings") {
          settingsSaveTimer = setTimeout(() => {
            settingsSaveTimer = null;
            saveSettingsNow();
          }, 400);
        }
      }
    })();
    settingsSavePromise = p;
    await p;
    settingsSavePromise = null;
    return p;
  }

  async function flushSettingsSave() {
    if (settingsSaveTimer) {
      clearTimeout(settingsSaveTimer);
      settingsSaveTimer = null;
    }
    while (settingsSaving && settingsSavePromise) {
      await settingsSavePromise;
    }
    if (settingsDirty) {
      await saveSettingsNow();
      while (settingsSaving && settingsSavePromise) {
        await settingsSavePromise;
      }
    }
  }

  // ---------------- 健康检查 ----------------
  async function checkHealth() {
    try {
      const h = await api("/healthz");
      const mcp = !!h.mcp_enabled;
      setConn(true, mcp ? "MCP 已启动" : "MCP 已停止", mcp ? "ok" : "off");
      setSideStatus(mcp ? "ok" : "off", mcp ? (location.host + (h.mcp_path || "/mcp")) : "已停止");
    } catch {
      setConn(false, "无法连接服务", "bad");
      setSideStatus("bad", "服务未连接");
    }
  }

  // ---------------- 启动 ----------------
  window.addEventListener("hashchange", dispatch);
  window.addEventListener("DOMContentLoaded", () => {
    initTheme();
    initSidebar();
    checkHealth();
    dispatch();
    setInterval(checkHealth, 15000);
  });
})();
