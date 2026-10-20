"use strict";
// stampsort 桌面版界面：加入照片 → 自动识别 → 核对 → 导出。
const $ = (id) => document.getElementById(id);
const show = (el, on = true) => el.classList.toggle("hidden", !on);
const ICON = (name, cls = "ic") => `<svg class="${cls}"><use href="#i-${name}"/></svg>`;
const SRC = { ocr: "水印", exif: "EXIF", filename: "文件名", manual: "手动", mark: "已标注", none: "未识别" };
const WEEK = "日一二三四五六";

let state = { items: [], pending: 0, settings: {}, export: null, native: false, ocr: true };
let filter = "all";
let order = []; // 当前可见顺序（核对弹窗用）
const cards = new Map(); // id -> 卡片元素
const diags = [];

// ------------------------------------------------------------------ 接口

async function api(path, opts = {}) {
  const init = { ...opts, headers: { ...(opts.headers || {}) } };
  if (opts.json !== undefined) {
    init.body = JSON.stringify(opts.json);
    init.headers["Content-Type"] = "application/json";
    init.method = init.method || "POST";
  }
  const r = await fetch(path, init);
  let body = {};
  try { body = await r.json(); } catch {}
  if (!r.ok) {
    const err = new Error(body.error || r.statusText);
    err.status = r.status;
    err.body = body;
    throw err;
  }
  return body;
}

// 状态：SSE 只通知"有变化"，这里节流拉取完整状态
let fetching = false, dirty = false;
async function refresh() {
  if (fetching) { dirty = true; return; }
  fetching = true;
  try {
    state = await api("/api/state");
    render();
  } catch (e) {
    console.error(e);
  } finally {
    fetching = false;
    if (dirty) { dirty = false; setTimeout(refresh, 120); }
  }
}
function connect() {
  const es = new EventSource("/api/events");
  es.addEventListener("changed", refresh);
  es.onerror = () => { es.close(); setTimeout(connect, 1500); };
}

// ------------------------------------------------------------------ 渲染

// 水印识别、手动填写、以前导出时写进照片的时间都可信，其余（EXIF、文件名、未识别）需要核对
function isCheck(it) { return it.status === "ready" && !["ocr", "manual", "mark"].includes(it.source); }
function isBad(it) { return it.status === "error" || (it.status === "ready" && it.source === "none"); }

function dateLabel(t) {
  const [y, m, d] = t.slice(0, 10).split("-").map(Number);
  const wd = new Date(y, m - 1, d).getDay();
  return `${y}年${m}月${d}日 · 周${WEEK[wd]}`;
}

function render() {
  const items = state.items || [];
  const s = state.settings || {};
  const counts = { total: items.length, ready: 0, pending: 0, bad: 0, check: 0, err: 0 };
  for (const it of items) {
    if (it.status === "pending") counts.pending++;
    else if (it.status === "error") counts.err++;
    else counts.ready++;
    if (isBad(it)) counts.bad++;
    if (isCheck(it)) counts.check++;
  }
  show($("ocrWarn"), !state.ocr);
  show($("empty"), items.length === 0);
  show($("toolbar"), items.length > 0);

  // 统计
  const st = [`<span>共 <b>${counts.total}</b> 张</span>`];
  if (counts.pending) st.push(`<span class="run"><span class="spinner sm"></span>识别中 <b>${counts.pending}</b>${state.rate ? ` · 每秒 ${state.rate.toFixed(1)} 张` : ""}</span>`);
  if (counts.bad) st.push(`<span class="bad">未识别 <b>${counts.bad}</b></span>`);
  $("stats").innerHTML = st.join("");
  $("checkCount").textContent = counts.check;
  show($("checkCount"), counts.check > 0);

  renderGroups(items);
  renderSettings(s);
  renderUpdate(state.update || {});
  renderSummary(counts, s);
  renderExport();
}

function groupsOf(items) {
  const groups = [];
  const by = new Map();
  const add = (key, title, cls, it) => {
    let g = by.get(key);
    if (!g) { g = { key, title, cls, items: [] }; by.set(key, g); groups.push(g); }
    g.items.push(it);
  };
  const vis = items.filter((it) => filter === "all" || isCheck(it) || it.status === "error");
  // 固定顺序：待核对 → 识别中 → 按日期 → 无法打开
  for (const it of vis) if (it.status === "ready" && it.source === "none") add("check", "没识别出时间", "check", it);
  for (const it of vis) if (it.status === "pending") add("pending", "识别中", "pending", it);
  for (const it of vis) if (it.status === "ready" && it.source !== "none") add(it.time.slice(0, 10), dateLabel(it.time), "", it);
  for (const it of vis) if (it.status === "error") add("error", "无法打开", "check", it);
  return groups;
}

function renderGroups(items) {
  const groups = groupsOf(items);
  const host = $("groups");
  const seen = new Set();
  order = [];
  const frag = document.createDocumentFragment();
  for (const g of groups) {
    const sec = document.createElement("section");
    sec.className = "group " + g.cls;
    const head = document.createElement("div");
    head.className = "group-head";
    if (g.key === "check") head.innerHTML = `${g.title}<span class="tag">${g.items.length}</span><small>点开照片填写时间，否则会放进「未识别」</small>`;
    else if (g.key === "pending") head.innerHTML = `${g.title}<span class="tag">${g.items.length}</span>`;
    else if (g.key === "error") head.innerHTML = `${g.title}<span class="tag">${g.items.length}</span><small>文件损坏或格式不支持，导出时会跳过</small>`;
    else head.innerHTML = `${g.title}<small>${g.items.length} 张</small>`;
    const grid = document.createElement("div");
    grid.className = "grid";
    for (const it of g.items) {
      grid.append(card(it));
      seen.add(it.id);
      order.push(it.id);
    }
    sec.append(head, grid);
    frag.append(sec);
  }
  host.replaceChildren(frag);
  for (const id of [...cards.keys()]) if (!seen.has(id)) cards.delete(id);
  show($("filterEmpty"), filter === "check" && items.length > 0 && groups.length === 0);
}

// card 复用已有元素，只更新变化的部分（缩略图不会闪）
function card(it) {
  let el = cards.get(it.id);
  if (!el) {
    el = document.createElement("div");
    el.tabIndex = 0;
    el.dataset.id = it.id;
    el.innerHTML = `<div class="thumb"><img alt="" draggable="false"></div>
      <div class="meta"><span class="time"></span><span class="src"></span><span class="name"></span></div>
      <button class="rm" title="移出列表（不会删除原图）">${ICON("close")}</button>`;
    el.classList.add("enter");
    el.querySelector("img").onload = (e) => e.target.classList.add("loaded");
    cards.set(it.id, el);
  }
  const key = `${it.status}|${it.time}|${it.source}|${it.name}`;
  if (el.dataset.key === key) return el;
  el.dataset.key = key;
  el.className = "card" + (it.status === "pending" ? " pending" : "") + (isBad(it) ? " bad" : "");
  const img = el.querySelector("img");
  if (it.status === "ready" && !img.getAttribute("src")) img.src = `/api/thumb/${it.id}`;
  const time = el.querySelector(".time"), src = el.querySelector(".src");
  el.querySelector(".name").textContent = it.name;
  el.title = it.name + (it.raw ? `\n识别原文：${it.raw}` : "") + (it.err ? `\n${it.err}` : "");
  if (it.status === "pending") {
    time.textContent = "识别中…";
    src.className = "src hidden";
  } else if (it.status === "error") {
    time.textContent = "打不开";
    src.className = "src error";
    src.textContent = "错误";
  } else if (it.source === "none") {
    time.textContent = "点击填写时间";
    src.className = "src none";
    src.textContent = "未识别";
  } else {
    time.textContent = it.clock ? it.time.slice(11) : "仅日期";
    src.className = "src " + it.source;
    src.textContent = SRC[it.source] + (isCheck(it) ? " · 请核对" : "");
  }
  return el;
}

function renderSettings(s) {
  for (const name of ["mode", "layout"]) {
    const input = document.querySelector(`input[name=${name}][value="${s[name]}"]`);
    if (input && !input.checked) input.checked = true;
  }
  // 通用绑定：data-str / data-num 是下拉框，data-bool / data-inv（取反）是开关
  document.querySelectorAll("[data-str]").forEach((el) => { if (el !== document.activeElement) el.value = s[el.dataset.str]; });
  document.querySelectorAll("[data-num]").forEach((el) => { if (el !== document.activeElement) el.value = String(s[el.dataset.num]); });
  document.querySelectorAll("[data-bool]").forEach((el) => (el.checked = !!s[el.dataset.bool]));
  document.querySelectorAll("[data-inv]").forEach((el) => (el.checked = !s[el.dataset.inv]));
  const nf = document.querySelector(`input[name=newFolder][value="${s.newFolder ? 1 : 0}"]`);
  if (nf) nf.checked = true;
  $("bleed").checked = s.bleed > 0;
  for (const id of ["outDir", "outDir2"]) {
    const bdi = document.createElement("bdi");
    bdi.textContent = s.outDir || "";
    $(id).replaceChildren(bdi);
    $(id).title = s.outDir || "";
  }
  const print = s.mode !== "sort";
  document.querySelectorAll("[data-for-print]").forEach((el) => show(el, print));
  $("layoutHint").textContent = s.layout === "crop"
    ? "完全不变形，只从顶部裁到 3:2，不会裁到水印。"
    : "顶部少裁一点，水印不变形，主体轻微竖向压缩（约 7%）。";
  const ex = examplePaths(s);
  $("example").innerHTML = ex;
  $("example2").innerHTML = ex;
  // 识别
  const sel = $("workersSel");
  if (sel.options.length === 0) {
    sel.add(new Option(`自动（${state.cpu || "?"} 张，等于 CPU 核数）`, "0"));
    for (const n of [1, 2, 4, 6, 8, 12, 16]) sel.add(new Option(`${n} 张`, String(n)));
    sel.value = String(s.workers || 0);
  }
  $("hintState").innerHTML = state.hinted ? `<span class="ok-text">已学到</span>` : `<span class="muted">还没学到（认出第一张后自动记住）</span>`;
  $("verText").textContent = state.version || "";
}

// 路径示例：用第一张已识别的照片（没有就用示例时间）演示导出后放在哪里
const PAD = (n) => String(n).padStart(2, "0");
function examplePaths(s) {
  const it = (state.items || []).find((x) => x.status === "ready" && x.time && x.clock);
  const [d, t] = (it ? it.time : "2026-10-19 08:21").split(" ");
  const [y, m, day] = d.split("-");
  const sep = state.platform === "windows" ? "\\" : "/";
  const dirs = { ymd: [y, m, day], ym: [y, m], md: [m, day], d: [day], flat: [d] }[s.dirFormat] || [y, m, day];
  const name = { datetime: `${d}_${t.replace(":", "-")}`, time: t.replace(":", "-"), orig: it ? it.name.split("/").pop().replace(/\.[^.]+$/, "") : "IMG_0001" }[s.nameFormat];
  let root = (s.outDir || "").replace(/[\\/]+$/, "");
  if (s.newFolder) root += sep + "2026-10-19 103000 导出";
  const esc = (x) => x.replace(/&/g, "&amp;").replace(/</g, "&lt;");
  const line = (sub, dd, file) => `${esc(root)}${sub ? sep + "<b>" + sub + "</b>" : ""}${dd.length ? sep + "<b>" + dd.join(sep) + "</b>" : ""}${sep}${esc(file)}`;
  const lines = [];
  if (s.mode !== "print") lines.push(line(s.mode === "all" ? "分类" : "", dirs, name + ".jpg"));
  if (s.mode !== "sort") lines.push(line(s.mode === "all" ? "6寸" : "", s.mode === "print" && s.printFlat ? [] : dirs, name + "_6寸.jpg"));
  return "导出后：<br>" + lines.join("<br>");
}

function renderSummary(c, s) {
  const btn = $("exportBtn");
  const exporting = state.export && state.export.running;
  const n = c.ready;
  btn.disabled = n === 0 || c.pending > 0 || exporting;
  btn.textContent = exporting ? "正在导出…" : c.pending > 0 ? `识别中，请稍候（${c.pending}）` : n ? `导出 ${n} 张` : "导出";
  const what = { all: "分类 + 6 寸打印", sort: "按 年/月/日 分类", print: "6 寸打印文件" }[s.mode] || "";
  const lines = [];
  if (n) lines.push(`<b>${what}</b>${s.mode !== "sort" && s.pdf ? " + PDF" : ""}`);
  const bad = (state.items || []).filter((it) => it.status === "ready" && it.source === "none").length;
  if (n && bad && s.mode !== "print") lines.push(`<span class="warn">${bad} 张没识别出时间，会放进「未识别」</span>`);
  if (c.err) lines.push(`${c.err} 张打不开，会跳过`);
  $("summary").innerHTML = lines.join("<br>");
}

// ------------------------------------------------------------------ 加入照片

function toast(msg, kind = "info", opts = {}) {
  const el = document.createElement("div");
  el.className = "toast " + kind;
  const icon = { ok: "check", warn: "warn", err: "warn", info: "image" }[kind];
  el.innerHTML = `${ICON(icon)}<span></span>`;
  el.querySelector("span").textContent = msg;
  if (opts.action) {
    const a = document.createElement("a");
    a.textContent = opts.action;
    a.onclick = () => { opts.onAction(); el.remove(); };
    el.append(a);
  }
  if (opts.progress) {
    const bar = document.createElement("div");
    bar.className = "bar";
    bar.innerHTML = "<div></div>";
    el.append(bar);
  }
  $("toasts").append(el);
  if (!opts.sticky) setTimeout(() => el.remove(), opts.ms || 4000);
  return el;
}

function reportAdd(r, origin) {
  if (!r) return;
  const parts = [];
  if (r.added) parts.push(`已加入 ${r.added} 张`);
  if (r.dup) parts.push(`跳过 ${r.dup} 张重复的`);
  if (parts.length) toast(parts.join("，"), r.added ? "ok" : "info");
  else toast(`${origin}里没有找到 jpg / png 照片或 zip`, "warn", { action: "拖放诊断", onAction: openDiag });
  if (r.warnings && r.warnings.length) toast(r.warnings.slice(0, 3).join("；"), "warn", { ms: 7000 });
}

async function pick(kind) {
  $("addMenu").classList.add("hidden");
  try {
    const r = await api("/api/pick", { json: { kind } });
    if (kind !== "outdir" && (r.added !== undefined)) reportAdd(r, "选择的文件");
  } catch (e) {
    if (e.status === 501) return pickFallback(kind); // 非 Windows：用网页自带的选择框
    toast(e.message, "err");
  }
}

function pickFallback(kind) {
  if (kind === "outdir") {
    const p = prompt("输出目录（完整路径）", state.settings.outDir || "");
    if (p) saveSettings({ outDir: p });
    return;
  }
  const input = kind === "folder" ? $("dirInput") : $("fileInput");
  input.accept = kind === "zip" ? ".zip" : "image/jpeg,image/png,.zip";
  input.onchange = () => { upload([...input.files], "选择的文件"); input.value = ""; };
  input.click();
}

// upload 分批上传（每批最多 24 个或 200MB），显示进度
async function upload(files, origin = "拖入的内容") {
  files = files.filter((f) => /\.(jpe?g|png|zip)$/i.test(f.name) || /^image\/(jpeg|png)$/.test(f.type));
  if (!files.length) return reportAdd({}, origin);
  const total = files.reduce((s, f) => s + f.size, 0);
  const t = files.length > 6 ? toast(`正在加入 ${files.length} 个文件…`, "info", { sticky: true, progress: true }) : null;
  let sent = 0;
  const sum = { added: 0, dup: 0, warnings: [] };
  try {
    for (let i = 0; i < files.length;) {
      const fd = new FormData();
      let size = 0, n = 0;
      while (i < files.length && n < 24 && (n === 0 || size + files[i].size < 200 << 20)) {
        const f = files[i++];
        fd.append("files", f, uploadName(f));
        size += f.size; n++;
      }
      const r = await xhrUpload(fd, (loaded) => {
        if (t) t.querySelector(".bar > div").style.width = `${Math.round(((sent + loaded) / total) * 100)}%`;
      });
      sent += size;
      sum.added += r.added || 0;
      sum.dup += r.dup || 0;
      sum.warnings.push(...(r.warnings || []));
    }
    reportAdd(sum, origin);
  } catch (e) {
    toast("加入失败：" + e.message, "err");
  } finally {
    if (t) t.remove();
  }
}

// 粘贴的位图在 Chromium 里都叫 image.png，换个不重复的名字。
// 注意名字里不能出现日期数字，否则会被当成"文件名里的拍摄时间"。
let pasteSeq = 0;
function uploadName(f) {
  if (f.name && f.name !== "image.png" && f.name !== "image.jpg") return f.webkitRelativePath || f.relPath || f.name;
  return `粘贴图片-${++pasteSeq}-${Math.random().toString(36).slice(2, 7)}.${f.type === "image/jpeg" ? "jpg" : "png"}`;
}

function xhrUpload(fd, onProgress) {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open("POST", "/api/add/upload");
    x.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded);
    x.onload = () => {
      let b = {};
      try { b = JSON.parse(x.responseText); } catch {}
      x.status === 200 ? resolve(b) : reject(new Error(b.error || `HTTP ${x.status}`));
    };
    x.onerror = () => reject(new Error("网络错误"));
    x.send(fd);
  });
}

// ------------------------------------------------------------------ 拖放 / 粘贴（重点兼容微信）

function readEntry(entry, prefix = "") {
  return new Promise((resolve) => {
    if (entry.isFile) {
      entry.file((f) => { f.relPath = prefix + f.name; resolve([f]); }, () => resolve([]));
    } else if (entry.isDirectory) {
      const reader = entry.createReader();
      const all = [];
      const next = () => reader.readEntries(async (batch) => {
        if (!batch.length) {
          const nested = await Promise.all(all.map((e) => readEntry(e, prefix + entry.name + "/")));
          resolve(nested.flat());
          return;
        }
        all.push(...batch);
        next();
      }, () => resolve([]));
      next();
    } else resolve([]);
  });
}

// 从 uri-list / html / 纯文本里找图片地址（有的程序拖出来只给链接）
function extractURLs(uri, html, text) {
  const out = new Set();
  for (const line of (uri || "").split(/\r?\n/)) if (line && !line.startsWith("#")) out.add(line.trim());
  if (html) {
    const doc = new DOMParser().parseFromString(html, "text/html");
    doc.querySelectorAll("img[src]").forEach((img) => out.add(img.getAttribute("src")));
  }
  for (const line of (text || "").split(/\r?\n/)) {
    const s = line.trim().replace(/^"|"$/g, "");
    if (/^(https?|file):\/\//i.test(s)) out.add(s);
    else if (/^[a-zA-Z]:\\.+\.(jpe?g|png|zip)$/i.test(s)) out.add("file:///" + s.replace(/\\/g, "/"));
  }
  return [...out].filter((u) => /^(https?|file):/i.test(u));
}

function snapshotDT(dt, kind) {
  // DataTransfer 在 await 之后就失效了，先同步取出所有内容
  const entries = [], loose = [], items = [];
  for (const it of dt.items || []) {
    items.push({ kind: it.kind, type: it.type });
    if (it.kind !== "file") continue;
    const en = it.webkitGetAsEntry && it.webkitGetAsEntry();
    if (en) entries.push(en);
    else { const f = it.getAsFile(); if (f) loose.push(f); }
  }
  const files = [...(dt.files || [])];
  const get = (t) => { try { return dt.getData(t) || ""; } catch { return ""; } };
  const strings = { uri: get("text/uri-list"), html: get("text/html"), text: get("text/plain") };
  const diag = {
    kind, at: new Date().toLocaleString(), types: [...(dt.types || [])], items,
    files: files.map((f) => ({ name: f.name, size: f.size, type: f.type })),
    entries: entries.map((e) => ({ name: e.name, dir: e.isDirectory })),
    uri: strings.uri.slice(0, 600), html: strings.html.slice(0, 600), text: strings.text.slice(0, 300),
  };
  return { entries, loose, files, strings, diag };
}

async function handleTransfer(snap, origin) {
  let files = [...snap.loose];
  for (const en of snap.entries) files.push(...(await readEntry(en)));
  if (!files.length && snap.files.length) files = snap.files;
  snap.diag.result = files.length ? `files:${files.length}` : "no-files";
  if (files.length) return upload(files, origin);
  const urls = extractURLs(snap.strings.uri, snap.strings.html, snap.strings.text);
  snap.diag.result = urls.length ? `urls:${urls.length}` : "nothing";
  if (urls.length) {
    try { reportAdd(await api("/api/add/urls", { json: { urls } }), origin); }
    catch (e) { toast(e.message, "err"); }
    return;
  }
  reportAdd({}, origin);
}

function logDiag(diag) {
  diags.push(diag);
  if (diags.length > 10) diags.shift();
  api("/api/diag", { json: diag }).catch(() => {});
}

let dragDepth = 0;
window.addEventListener("dragenter", (e) => { e.preventDefault(); if (dragDepth++ === 0) show($("dropmask")); });
window.addEventListener("dragover", (e) => { e.preventDefault(); if (e.dataTransfer) e.dataTransfer.dropEffect = "copy"; });
window.addEventListener("dragleave", () => { if (--dragDepth <= 0) { dragDepth = 0; show($("dropmask"), false); } });
window.addEventListener("drop", (e) => {
  e.preventDefault();
  dragDepth = 0;
  show($("dropmask"), false);
  if (!e.dataTransfer) return;
  const snap = snapshotDT(e.dataTransfer, "drop");
  handleTransfer(snap, "拖入的内容").finally(() => logDiag(snap.diag));
});

document.addEventListener("paste", async (e) => {
  if (e.target instanceof Element && e.target.closest("input, textarea")) return;
  e.preventDefault();
  const snap = e.clipboardData ? snapshotDT(e.clipboardData, "paste") : null;
  // 先读系统剪贴板里的文件路径：微信/资源管理器"复制"的文件能拿到原图
  try {
    const r = await api("/api/paste-native", { method: "POST" });
    if (r.added || r.dup) {
      if (snap) { snap.diag.result = "native"; logDiag(snap.diag); }
      return reportAdd(r, "剪贴板");
    }
  } catch {}
  if (snap) handleTransfer(snap, "剪贴板").finally(() => logDiag(snap.diag));
});

// ------------------------------------------------------------------ 列表操作

$("groups").addEventListener("click", (e) => {
  const el = e.target.closest(".card");
  if (!el) return;
  const id = Number(el.dataset.id);
  if (e.target.closest(".rm")) {
    api("/api/remove", { json: { ids: [id] } }).catch((err) => toast(err.message, "err"));
    return;
  }
  openViewer(id);
});
$("groups").addEventListener("keydown", (e) => {
  const el = e.target.closest(".card");
  if (!el) return;
  if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openViewer(Number(el.dataset.id)); }
  if (e.key === "Delete") api("/api/remove", { json: { ids: [Number(el.dataset.id)] } });
});

document.querySelectorAll("input[name=filter]").forEach((r) => r.addEventListener("change", () => { filter = r.value; render(); }));

$("clearBtn").onclick = async () => {
  if (await confirmDlg("清空列表？", "只是从列表里移除，不会删除你电脑上的原图。", "清空", "取消")) {
    api("/api/remove", { json: { all: true } });
  }
};

$("addBtn").onclick = (e) => { e.stopPropagation(); $("addMenu").classList.toggle("hidden"); };
document.addEventListener("click", () => $("addMenu").classList.add("hidden"));
document.querySelectorAll("[data-pick]").forEach((b) => b.addEventListener("click", (e) => { e.stopPropagation(); pick(b.dataset.pick); }));

// ------------------------------------------------------------------ 设置

async function saveSettings(patch) {
  try {
    state.settings = await api("/api/settings", { method: "PUT", json: patch });
    render();
  } catch (e) { toast(e.message, "err"); }
}
document.querySelectorAll("input[name=mode], input[name=layout]").forEach((r) =>
  r.addEventListener("change", () => saveSettings({ [r.name]: r.value })));
document.querySelectorAll("input[name=newFolder]").forEach((r) => r.addEventListener("change", () => saveSettings({ newFolder: r.value === "1" })));
document.querySelectorAll("[data-str]").forEach((el) => el.addEventListener("change", () => saveSettings({ [el.dataset.str]: el.value })));
document.querySelectorAll("[data-num]").forEach((el) => el.addEventListener("change", () => saveSettings({ [el.dataset.num]: Number(el.value) })));
document.querySelectorAll("[data-bool]").forEach((el) => el.addEventListener("change", () => saveSettings({ [el.dataset.bool]: el.checked })));
document.querySelectorAll("[data-inv]").forEach((el) => el.addEventListener("change", () => saveSettings({ [el.dataset.inv]: !el.checked })));
$("bleed").onchange = () => saveSettings({ bleed: $("bleed").checked ? 2.5 : 0 });
document.querySelectorAll("[data-outdir]").forEach((b) => (b.onclick = () => pick("outdir")));
$("outOpen").onclick = () => api("/api/open", { json: { path: state.settings.outDir } }).catch((e) => toast(e.message, "err"));

// ------------------------------------------------------------------ 核对大图

// 弹窗按照片 id 跟踪当前项。不能记列表下标：改完时间后列表会重新排序（SSE 随时刷新），
// 同一个下标会指向别的照片，修改就会存到错的照片上。
let vId = -1, vKind = "orig";
const viewer = $("viewer");

function currentItem() { return (state.items || []).find((it) => it.id === vId); }

function openViewer(id) {
  if (order.indexOf(id) < 0) return;
  vId = id;
  if (!viewer.open) viewer.showModal();
  showViewerItem();
}

function showViewerItem() {
  const it = currentItem();
  if (!it) { viewer.close(); return; }
  const idx = order.indexOf(it.id);
  $("vName").textContent = it.name;
  $("vPos").textContent = `${idx + 1} / ${order.length}`;
  $("vPrev").disabled = idx <= 0;
  $("vNext").disabled = idx < 0 || idx === order.length - 1;
  loadViewerImage(it);
  const src = $("vSrc");
  if (it.status === "ready") {
    src.innerHTML = `<span class="src ${it.source}">${SRC[it.source] || ""}</span><span class="raw"></span>`;
    src.querySelector(".raw").textContent = it.raw ? `识别到「${it.raw}」` : it.source === "none" ? "左下角没找到时间，请手动填写" : "";
  } else src.innerHTML = `<span class="src error">${it.status === "pending" ? "识别中" : "打不开"}</span>`;
  $("vDate").value = it.time ? it.time.slice(0, 10) : "";
  $("vClock").value = it.time && it.clock ? it.time.slice(11, 16) : "";
  show($("vReset"), it.source === "manual");
  show($("vRedetect"), state.ocr && it.status === "ready" && !["manual", "ocr", "mark"].includes(it.source));
  $("vForm").querySelectorAll("input, button[type=submit]").forEach((el) => (el.disabled = it.status !== "ready"));
  if (it.status === "ready" && !viewer.contains(document.activeElement)) $("vDate").focus();
}

function loadViewerImage(it) {
  const img = $("vImg"), wrap = $("vImgWrap");
  wrap.classList.toggle("print", vKind === "print");
  show($("vPaper"), vKind === "print");
  if (it.status !== "ready") {
    img.removeAttribute("src");
    delete img.dataset.url; // 否则切回同一张时以为已加载，图片一直空白
    show($("vSpin"), it.status === "pending");
    return;
  }
  const url = `/api/preview/${it.id}?kind=${vKind}` + (vKind === "print" ? `&l=${state.settings.layout}` : "");
  if (img.dataset.url === url) return;
  img.dataset.url = url;
  img.style.opacity = 0.3;
  show($("vSpin"));
  img.onload = () => { img.style.opacity = 1; show($("vSpin"), false); };
  img.onerror = () => { show($("vSpin"), false); };
  img.src = url;
}

function step(d) {
  const n = order.indexOf(vId) + d;
  if (n < 0 || n >= order.length) return;
  vId = order[n];
  showViewerItem();
}
$("vPrev").onclick = () => step(-1);
$("vNext").onclick = () => step(1);
document.querySelectorAll("input[name=vkind]").forEach((r) => r.addEventListener("change", () => {
  vKind = r.value;
  const it = currentItem();
  if (it) loadViewerImage(it);
}));
viewer.addEventListener("keydown", (e) => {
  // 焦点在日期框里时 Esc 会被输入框吃掉，这里统一处理
  if (e.key === "Escape") { e.preventDefault(); viewer.close(); return; }
  if (e.target.matches("input") && (e.key === "ArrowLeft" || e.key === "ArrowRight")) return;
  if (e.key === "ArrowLeft") { e.preventDefault(); step(-1); }
  if (e.key === "ArrowRight") { e.preventDefault(); step(1); }
});
viewer.addEventListener("click", (e) => { if (e.target === viewer) viewer.close(); });

$("vForm").onsubmit = async (e) => {
  e.preventDefault();
  const it = currentItem();
  if (!it) return;
  const curID = it.id; // 先记下，后面的 await 期间列表可能重排
  const date = $("vDate").value, clock = $("vClock").value;
  const value = date + (clock ? " " + clock : "");
  const old = it.time || "";
  if (date && value !== old) {
    try {
      await api(`/api/items/${curID}`, { method: "PATCH", json: { time: value } });
    } catch (err) { return toast(err.message, "err"); }
  }
  // 跳到下一张需要核对的（优先排在后面的），没有就下一张
  await refresh();
  const idx = order.indexOf(curID);
  const items = state.items || [];
  const needs = (id) => id !== curID && items.find((x) => x.id === id && isCheck(x));
  const next = order.find((id, i) => i > idx && needs(id)) ?? order.find(needs);
  if (next !== undefined) { vId = next; showViewerItem(); }
  else if (idx >= 0 && idx < order.length - 1) { vId = order[idx + 1]; showViewerItem(); }
  else { viewer.close(); toast("核对完成", "ok"); }
};
$("vRedetect").onclick = async () => {
  const it = currentItem();
  if (!it) return;
  try {
    await api(`/api/items/${it.id}/redetect`, { method: "POST" });
  } catch (e) { return toast(e.message, "err"); }
  const curID = it.id;
  toast("正在重新识别…");
  // 等这张识别完再刷新弹窗
  for (let i = 0; i < 50; i++) {
    await new Promise((r) => setTimeout(r, 200));
    await refresh();
    const now = (state.items || []).find((x) => x.id === curID);
    if (now && now.status !== "pending") {
      toast(now.source === "ocr" ? `读到了：${now.time}` : "还是没读到水印，请手动填写", now.source === "ocr" ? "ok" : "warn");
      break;
    }
  }
  vId = curID;
  showViewerItem();
};
$("vReset").onclick = async () => {
  const it = currentItem();
  if (!it) return;
  await api(`/api/items/${it.id}`, { method: "PATCH", json: { time: "" } }).catch((e) => toast(e.message, "err"));
  await refresh();
  vId = it.id;
  showViewerItem();
};

// ------------------------------------------------------------------ 导出

$("exportBtn").onclick = async () => {
  const s = state.settings;
  const bad = (state.items || []).filter((it) => it.status === "ready" && it.source === "none").length;
  if (bad && s.mode !== "print") {
    const go = await confirmDlg("还有照片没有时间", `${bad} 张照片没识别出时间，导出后会放进「未识别」文件夹。要先去填写吗？`, "先去核对", "直接导出");
    if (go) {
      document.querySelector("input[name=filter][value=check]").click();
      return openViewer(order[0]);
    }
  }
  if (s.mode !== "print" || !s.printFlat) {
    // 层级里没有月（或年）而照片跨月（跨年）时，不同月份同一天的照片会混进同一个文件夹
    const ready = (state.items || []).filter((it) => it.status === "ready" && it.time);
    const months = [...new Set(ready.map((it) => it.time.slice(0, 7)))].sort();
    const years = [...new Set(months.map((m) => m.slice(0, 4)))];
    const label = (m) => `${Number(m.slice(0, 4))}年${Number(m.slice(5, 7))}月`;
    if (s.dirFormat === "d" && months.length > 1) {
      const fix = await confirmDlg("照片跨了好几个月", `这批照片包含 ${months.map(label).join("、")}。按「日」分文件夹会把不同月份同一天的照片放进同一个文件夹。`, years.length > 1 ? "改成 年/月/日" : "改成 月/日", "仍然导出");
      if (fix) await saveSettings({ dirFormat: years.length > 1 ? "ymd" : "md" });
    } else if (s.dirFormat === "md" && years.length > 1) {
      const fix = await confirmDlg("照片跨了年份", `这批照片包含 ${years.join("、")} 年。按「月/日」分会把不同年份同一天的照片放到一起。`, "改成 年/月/日", "仍然导出");
      if (fix) await saveSettings({ dirFormat: "ymd" });
    }
  }
  try {
    await api("/api/export", { method: "POST" });
    $("exportDlg").showModal();
    refresh();
  } catch (e) { toast(e.message, "err"); }
};

function renderExport() {
  const x = state.export;
  const dlg = $("exportDlg");
  if (!x || !dlg.open) return;
  show($("expRunning"), x.running);
  show($("expDone"), !x.running);
  if (x.running) {
    $("expBar").style.width = `${x.total ? Math.round((x.done / x.total) * 100) : 0}%`;
    $("expText").textContent = `${x.done} / ${x.total}` + (x.current ? `　${x.current}` : "");
    return;
  }
  const ok = !x.error;
  $("expIcon").className = "done-icon" + (ok && !x.failed ? "" : " warn");
  $("expIcon").innerHTML = ICON(ok && !x.failed ? "check" : "warn");
  $("expTitle").textContent = x.error ? (x.error === "已取消" ? "导出已取消" : "导出出错") : "导出完成";
  const chips = [`<span>成功 ${x.ok} 张</span>`];
  if (x.unknown) chips.push(`<span class="bad">未识别 ${x.unknown}</span>`);
  if (x.failed) chips.push(`<span class="bad">失败 ${x.failed}</span>`);
  if (x.skipped) chips.push(`<span>跳过 ${x.skipped}</span>`);
  chips.push(`<span>用时 ${x.elapsed.toFixed(1)} 秒</span>`);
  $("expChips").innerHTML = chips.join("");
  $("expPath").textContent = (x.error && x.error !== "已取消" ? x.error + "　" : "") + x.outDir;
  const list = $("expPdfs");
  list.replaceChildren(...(x.pdfs || []).map((p) => {
    const b = document.createElement("button");
    const parts = p.split(/[\\/]/);
    b.innerHTML = `${ICON("pdf")}<span></span>`;
    b.querySelector("span").textContent = parts.slice(-4).join(" / ");
    b.onclick = () => api("/api/open", { json: { path: p } }).catch((e) => toast(e.message, "err"));
    return b;
  }));
}
$("expCancel").onclick = () => api("/api/export/cancel", { method: "POST" });
$("expOpen").onclick = () => api("/api/open", { json: { path: state.export.outDir } }).catch((e) => toast(e.message, "err"));
$("expClear").onclick = () => { $("exportDlg").close(); api("/api/remove", { json: { all: true } }); };

// ------------------------------------------------------------------ 通用对话框

function confirmDlg(title, text, okText, cancelText) {
  const d = $("confirmDlg");
  $("cfTitle").textContent = title;
  $("cfText").textContent = text;
  $("cfOk").textContent = okText;
  $("cfCancel").textContent = cancelText;
  d.showModal();
  return new Promise((resolve) => {
    $("cfOk").onclick = () => { d.close(); resolve(true); };
    $("cfCancel").onclick = () => { d.close(); resolve(false); };
    d.oncancel = () => resolve(false);
  });
}
document.querySelectorAll("[data-close]").forEach((b) => b.addEventListener("click", () => b.closest("dialog").close()));

function openDiag() {
  $("diagText").textContent = diags.length ? JSON.stringify(diags.slice().reverse(), null, 2) : "还没有拖放记录。先试着从微信拖一张图片进来，或者 Ctrl+V 粘贴。";
  $("diagDlg").showModal();
}
$("diagBtn2").onclick = openDiag;
$("setBtn").onclick = () => { renderSettings(state.settings); $("setDlg").showModal(); };
$("openLogs").onclick = () => api("/api/open-logs", { method: "POST" }).catch((e) => toast(e.message, "err"));
$("hintReset").onclick = async () => {
  await api("/api/hint/reset", { method: "POST" }).catch((e) => toast(e.message, "err"));
  toast("已清除，下一张认出后会重新记住水印位置", "ok");
  refresh();
};
$("updCheck").onclick = async () => {
  $("updCheckText").textContent = "正在检查…";
  try {
    const u = await api("/api/update/check", { method: "POST" });
    $("updCheckText").textContent = u.error ? `检查失败：${u.error}` : u.available ? `发现新版本 ${u.latest}` : `已是最新版本`;
    await refresh();
  } catch (e) { $("updCheckText").textContent = e.message; }
};

function renderUpdate(u) {
  // 设置页里的更新状态：有新版时直接给「立即更新」，下载中显示进度
  show($("updGo2"), u.available && !u.downloading && !u.installing);
  if (u.downloading) $("updCheckText").textContent = `正在下载 ${u.latest}… ${u.size ? Math.round((u.done / u.size) * 100) : 0}%`;
  else if (u.installing) $("updCheckText").textContent = "正在安装，程序会自动重新打开…";
  else if (u.available && !$("updCheckText").textContent) $("updCheckText").textContent = `发现新版本 ${u.latest}`;
  const show_ = (u.available && !u.dismissed) || u.downloading || u.installing;
  show($("updBanner"), show_);
  if (!show_) return;
  show($("updBarWrap"), u.downloading);
  show($("updGo"), !u.downloading && !u.installing);
  show($("updNotes"), !!u.notes && !u.downloading && !u.installing);
  show($("updClose"), !u.downloading && !u.installing);
  if (u.installing) $("updText").textContent = "正在安装新版本，程序会自动关闭并重新打开…";
  else if (u.downloading) {
    const pct = u.size ? Math.round((u.done / u.size) * 100) : 0;
    $("updText").textContent = `正在下载新版本 ${u.latest}… ${pct}%`;
    $("updBar").style.width = pct + "%";
  } else $("updText").textContent = `发现新版本 ${u.latest}（当前 ${u.current}）` + (u.error ? `　${u.error}` : "");
}
$("updGo").onclick = async () => {
  const x = state.export;
  if (x && x.running) return toast("正在导出，完成后再更新", "warn");
  try { await api("/api/update/install", { method: "POST" }); refresh(); }
  catch (e) { toast(e.message, "err"); }
};
$("updGo2").onclick = () => $("updGo").onclick();
$("updClose").onclick = () => api("/api/update/dismiss", { method: "POST" }).then(refresh);
$("updNotes").onclick = () => confirmDlg(`新版本 ${state.update.latest}`, state.update.notes || "", "立即更新", "稍后").then((ok) => ok && $("updGo").onclick());
$("ocrWarn").onclick = () => { $("ocrErr").textContent = state.ocrError || "未知"; $("ocrDlg").showModal(); };
$("ocrFix").onclick = () => api("/api/help/vcredist", { method: "POST" }).catch((e) => toast(e.message, "err"));
$("diagCopy").onclick = async () => {
  try { await navigator.clipboard.writeText($("diagText").textContent); toast("已复制", "ok"); }
  catch { toast("复制失败，请手动选中复制", "warn"); }
};
$("logPath").textContent = "%APPDATA%\\stampsort\\stampsort.log";

// 快捷键
document.addEventListener("keydown", (e) => {
  if (e.ctrlKey && e.key.toLowerCase() === "o") { e.preventDefault(); pick("files"); }
});

connect();
refresh();
