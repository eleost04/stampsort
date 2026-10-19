"use strict";
const $ = (id) => document.getElementById(id);
const show = (el, on = true) => el.classList.toggle("hidden", !on);
const LAST = "stampsort.lastJob";
const store = {
  get: (k) => { try { return localStorage.getItem(k); } catch { return null; } },
  set: (k, v) => { try { v ? localStorage.setItem(k, v) : localStorage.removeItem(k); } catch {} },
};

let files = [];
let me = null;
let pollTimer = null;
let jobId = null;

async function api(path, opts = {}) {
  const r = await fetch(path, { credentials: "same-origin", ...opts });
  let body = {};
  try { body = await r.json(); } catch {}
  if (r.status === 401 && path !== "/api/login") { showLogin(); throw new Error("需要登录"); }
  if (!r.ok) throw new Error(body.error || r.statusText);
  return body;
}

function showLogin() {
  show($("login")); show($("upload"), false); show($("progress"), false); show($("logout"), false);
  $("pw").focus();
}

async function init() {
  try {
    me = await api("/api/me");
  } catch { return; }
  show($("login"), false);
  show($("logout"), me.auth);
  show($("ocrWarn"), !me.ocr);
  const last = store.get(LAST);
  if (last) {
    jobId = last;
    show($("progress")); show($("upload"), false);
    poll();
  } else {
    show($("upload"));
  }
}

$("loginForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  show($("loginErr"), false);
  try {
    await api("/api/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password: $("pw").value }) });
    $("pw").value = "";
    init();
  } catch (err) {
    $("loginErr").textContent = err.message; show($("loginErr"));
  }
});

$("logout").onclick = async () => { await api("/api/logout", { method: "POST" }).catch(() => {}); showLogin(); };

// ---------------------------------------------------------------- 选文件
function addFiles(list) {
  for (const f of list) {
    if (/\.(jpe?g|png|zip)$/i.test(f.name)) files.push(f);
  }
  const size = files.reduce((s, f) => s + f.size, 0);
  const zips = files.filter((f) => /\.zip$/i.test(f.name)).length;
  $("files").textContent = files.length
    ? `已选 ${files.length - zips} 张照片${zips ? `、${zips} 个压缩包` : ""}，共 ${fmtSize(size)}`
    : "";
  $("start").disabled = !files.length;
  if (me && size > me.maxUpload) {
    $("files").textContent += `（超过上限 ${fmtSize(me.maxUpload)}，请分批）`;
    $("start").disabled = true;
  }
}
const fmtSize = (n) => n > 1 << 30 ? (n / (1 << 30)).toFixed(1) + "GB" : n > 1 << 20 ? (n / (1 << 20)).toFixed(1) + "MB" : Math.ceil(n / 1024) + "KB";

$("pickImg").onchange = (e) => { addFiles(e.target.files); e.target.value = ""; };
$("pickZip").onchange = (e) => { addFiles(e.target.files); e.target.value = ""; };
const drop = $("drop");
["dragenter", "dragover"].forEach((t) => drop.addEventListener(t, (e) => { e.preventDefault(); drop.classList.add("over"); }));
["dragleave", "drop"].forEach((t) => drop.addEventListener(t, (e) => { e.preventDefault(); drop.classList.remove("over"); }));
drop.addEventListener("drop", (e) => addFiles(e.dataTransfer.files));

document.querySelectorAll("input[name=mode]").forEach((r) => r.addEventListener("change", () => {
  const mode = document.querySelector("input[name=mode]:checked").value;
  show($("layoutRow"), mode !== "sort"); show($("extraRow"), mode !== "sort");
}));

// ---------------------------------------------------------------- 上传
$("start").onclick = () => {
  const fd = new FormData();
  fd.append("mode", document.querySelector("input[name=mode]:checked").value);
  fd.append("layout", document.querySelector("input[name=layout]:checked").value);
  fd.append("pdf", $("pdf").checked ? "1" : "0");
  fd.append("bleed", $("bleed").checked ? "2.5" : "0");
  for (const f of files) fd.append("files", f, f.name);

  show($("upload"), false); show($("progress"));
  resetProgress();
  $("stage").textContent = "上传中…";
  const xhr = new XMLHttpRequest();
  xhr.open("POST", "/api/jobs");
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) {
      setBar(e.loaded / e.total);
      $("count").textContent = `${fmtSize(e.loaded)} / ${fmtSize(e.total)}`;
    }
  };
  xhr.onload = () => {
    let body = {};
    try { body = JSON.parse(xhr.responseText); } catch {}
    if (xhr.status === 401) return showLogin();
    if (xhr.status !== 200) return fail(body.error || `上传失败（${xhr.status}）`);
    jobId = body.id; store.set(LAST, jobId);
    files = []; addFiles([]);
    poll();
  };
  xhr.onerror = () => fail("网络错误，上传失败");
  $("cancel").onclick = () => { xhr.abort(); back(); };
  xhr.send(fd);
};

function resetProgress() {
  setBar(0); $("count").textContent = ""; $("eta").textContent = "";
  show($("jobMsg"), false); show($("download"), false); show($("again"), false); show($("cancel"));
  $("list").innerHTML = "";
}
const setBar = (p) => { $("barFill").style.width = Math.round(p * 100) + "%"; };

function fail(msg) {
  $("stage").textContent = "出错了";
  $("jobMsg").textContent = msg; $("jobMsg").className = "msg err"; show($("jobMsg"));
  show($("cancel"), false); show($("again"));
}

function back() {
  clearTimeout(pollTimer); jobId = null; store.set(LAST, null);
  show($("progress"), false); show($("upload"));
}
$("again").onclick = back;

// ---------------------------------------------------------------- 进度
const STAGE = { queued: "排队中…", running: "处理中…", done: "处理完成", error: "出错了", cancelled: "已取消" };
const SRC = { ocr: "水印", exif: "EXIF", filename: "文件名", none: "未识别" };

async function poll() {
  clearTimeout(pollTimer);
  let j;
  try {
    j = await api(`/api/jobs/${jobId}`);
  } catch (err) {
    if (err.message === "需要登录") return;
    store.set(LAST, null);
    return fail(err.message);
  }
  $("stage").textContent = STAGE[j.state] || j.state;
  setBar(j.total ? j.done / j.total : 0);
  $("count").textContent = j.total ? `${j.done} / ${j.total} 张` : "";
  $("eta").textContent = j.elapsed ? `用时 ${j.elapsed.toFixed(1)} 秒` : "";
  renderList(j.results || []);

  const finished = ["done", "error", "cancelled"].includes(j.state);
  show($("cancel"), !finished);
  show($("again"), finished);
  const notes = [...(j.warnings || [])];
  if (j.error) notes.unshift(j.error);
  const bad = (j.results || []).filter((r) => r.err || r.source === "none").length;
  if (finished && bad) notes.push(`${bad} 张没识别出时间或处理失败（标红），已放进「未识别」`);
  if (finished && j.pdfs && j.pdfs.length) notes.push(`生成了 ${j.pdfs.length} 个 PDF`);
  $("jobMsg").textContent = notes.join("；");
  $("jobMsg").className = j.state === "error" ? "msg err" : (bad || (j.warnings || []).length) ? "msg" : "msg ok";
  show($("jobMsg"), notes.length > 0);
  if (j.state === "done" || j.state === "cancelled") {
    $("download").href = `/api/jobs/${jobId}/download`;
    show($("download"));
  }
  $("cancel").onclick = async () => {
    await api(`/api/jobs/${jobId}`, { method: "DELETE" }).catch(() => {});
    back();
  };
  if (!finished) pollTimer = setTimeout(poll, 1000);
}

function renderList(results) {
  const frag = document.createDocumentFragment();
  for (const r of results) {
    const div = document.createElement("div");
    const pending = !r.source && !r.err && !r.print;
    div.className = "item" + ((r.err || r.source === "none") ? " bad" : "");
    const name = document.createElement("div");
    name.className = "name"; name.textContent = r.name;
    const time = document.createElement("div");
    time.className = "time";
    if (pending) time.textContent = "…";
    else if (r.source) {
      time.textContent = r.time || "未识别";
      const tag = document.createElement("span");
      tag.className = "tag " + r.source; tag.textContent = SRC[r.source] || r.source;
      time.append(tag);
    } else time.textContent = r.err ? "失败" : "完成";
    div.append(name, time);
    const note = [r.err, ...(r.warnings || [])].filter(Boolean).join("；");
    if (note) {
      const n = document.createElement("div");
      n.className = "note"; n.textContent = note;
      div.append(n);
    }
    frag.append(div);
  }
  $("list").replaceChildren(frag);
}

if ("serviceWorker" in navigator) navigator.serviceWorker.register("sw.js").catch(() => {});
init();
