// Package desktop 是桌面版：本机 HTTP 服务 + WebView2 窗口（Windows）或浏览器（其它平台）。
//
// 流程：加入照片 → 后台自动识别时间、生成缩略图 → 用户在界面上核对/修改 → 导出到文件夹。
package desktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eleost04/stampsort/internal/job"
	"github.com/eleost04/stampsort/internal/photo"
	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
)

//go:embed ui
var uiFS embed.FS

const cookieName = "stampsort_desktop"

// Options 是桌面版启动参数。
type Options struct {
	Addr     string   // 默认 127.0.0.1:0（随机端口）
	Paths    []string // 启动时自动加入（拖到 exe 图标上的文件）
	Detector *stamp.Detector
	Version  string
	OCRError string // OCR 没加载成功时的原因，界面上显示
	Browser  bool   // 强制用浏览器（不用 WebView2 窗口）
	NoOpen   bool   // 只打印地址，不打开浏览器（测试用）
	DataDir  string
}

// Server 是桌面版的本机服务。
type Server struct {
	opts     Options
	token    string
	host     string // 127.0.0.1:端口，用于校验 Host / Origin
	sess     *Session
	settings *settingsStore
	mux      *http.ServeMux
	logf     func(format string, args ...any)

	subMu sync.Mutex
	subs  map[chan struct{}]bool

	expMu  sync.Mutex
	export *exportState

	diagMu sync.Mutex
	diags  []json.RawMessage

	upd       updater
	instToken string // 单实例转交用的令牌（与浏览器 cookie 令牌分开）
}

type exportState struct {
	Running  bool     `json:"running"`
	Done     int      `json:"done"`
	Total    int      `json:"total"`
	Current  string   `json:"current,omitempty"`
	OutDir   string   `json:"outDir,omitempty"`
	PDFs     []string `json:"pdfs,omitempty"`
	OK       int      `json:"ok"`
	Failed   int      `json:"failed"`
	Unknown  int      `json:"unknown"`
	Skipped  int      `json:"skipped"`
	Elapsed  float64  `json:"elapsed"`
	Error    string   `json:"error,omitempty"`
	Finished bool     `json:"finished"`
	cancel   context.CancelFunc
}

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newServer(o Options, host string) (*Server, error) {
	if o.DataDir == "" {
		o.DataDir = configDir()
	}
	if o.Detector == nil {
		o.Detector = &stamp.Detector{}
	}
	tmp, err := os.MkdirTemp("", "stampsort-gui-")
	if err != nil {
		return nil, err
	}
	logger := newLogger(filepath.Join(o.DataDir, "stampsort.log"))
	s := &Server{opts: o, token: randomToken(), instToken: randomToken(), host: host, mux: http.NewServeMux(),
		settings: loadSettings(filepath.Join(o.DataDir, "settings.json")), subs: map[chan struct{}]bool{}, logf: logger}
	// 上次学到的水印位置直接用；学到新的位置就保存
	for k, h := range s.settings.get().Hints {
		o.Detector.SetHint(k == "portrait", h)
	}
	o.Detector.OnHint = s.settings.setHint
	s.sess = NewSession(o.Detector, tmp, s.broadcast)
	s.sess.SetWorkers(s.settings.get().Workers)
	s.routes()
	return s, nil
}

func newLogger(path string) func(string, ...any) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	var mu sync.Mutex
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 { // 超过 1MB 轮换，只留一份旧的
			os.Rename(path, path+".old")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().Format("2006-01-02 15:04:05")}, args...)...)
		f.Close()
	}
}

func (s *Server) routes() {
	ui, _ := fs.Sub(uiFS, "ui")
	files := http.FileServerFS(ui)
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.Handle("GET /assets/", s.auth(http.StripPrefix("/assets", files).ServeHTTP))
	s.mux.HandleFunc("GET /api/events", s.auth(s.events))
	s.mux.HandleFunc("GET /api/state", s.auth(s.state))
	s.mux.HandleFunc("PUT /api/settings", s.auth(s.putSettings))
	s.mux.HandleFunc("POST /api/add/paths", s.auth(s.addPaths))
	s.mux.HandleFunc("POST /api/add/upload", s.auth(s.addUpload))
	s.mux.HandleFunc("POST /api/add/urls", s.auth(s.addURLs))
	s.mux.HandleFunc("POST /api/paste-native", s.auth(s.pasteNative))
	s.mux.HandleFunc("POST /api/pick", s.auth(s.pick))
	s.mux.HandleFunc("GET /api/thumb/{id}", s.auth(s.thumb))
	s.mux.HandleFunc("GET /api/preview/{id}", s.auth(s.preview))
	s.mux.HandleFunc("PATCH /api/items/{id}", s.auth(s.patchItem))
	s.mux.HandleFunc("POST /api/items/{id}/redetect", s.auth(s.redetect))
	s.mux.HandleFunc("POST /api/remove", s.auth(s.remove))
	s.mux.HandleFunc("POST /api/export", s.auth(s.startExport))
	s.mux.HandleFunc("POST /api/export/cancel", s.auth(s.cancelExport))
	s.mux.HandleFunc("POST /api/open", s.auth(s.open))
	s.mux.HandleFunc("POST /api/diag", s.auth(s.postDiag))
	s.mux.HandleFunc("POST /api/help/vcredist", s.auth(s.openVCRedist))
	s.mux.HandleFunc("POST /api/update/check", s.auth(s.apiCheckUpdate))
	s.mux.HandleFunc("POST /api/update/install", s.auth(s.apiInstallUpdate))
	s.mux.HandleFunc("POST /api/update/dismiss", s.auth(s.apiDismissUpdate))
	s.mux.HandleFunc("POST /api/hint/reset", s.auth(s.apiResetHint))
	s.mux.HandleFunc("POST /api/open-logs", s.auth(s.apiOpenLogs))
	s.mux.HandleFunc("POST /api/remote/open", s.apiRemoteOpen) // 自带实例令牌认证
}

// ServeHTTP 先校验 Host（防 DNS rebinding），再分发。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.host && r.Host != strings.Replace(s.host, "127.0.0.1", "localhost", 1) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// index：第一次用带令牌的地址打开时种下 cookie 并跳转到干净地址；之后凭 cookie 访问。
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("t"); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
			http.Error(w, "令牌不对", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !s.authed(r) {
		http.Error(w, "请从 stampsort 程序打开", http.StatusForbidden)
		return
	}
	data, _ := uiFS.ReadFile("ui/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
}

// auth 要求 cookie；修改类请求若带 Origin，必须是本服务（SameSite=Strict 之外再加一层防 CSRF）。
func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "未授权"})
			return
		}
		if o := r.Header.Get("Origin"); o != "" && r.Method != http.MethodGet {
			if u, err := url.Parse(o); err != nil || (u.Host != s.host && u.Host != strings.Replace(s.host, "127.0.0.1", "localhost", 1)) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "来源不对"})
				return
			}
		}
		h(w, r)
	}
}

// ------------------------------------------------------------------ 状态推送

func (s *Server) broadcast() {
	s.subMu.Lock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.subMu.Unlock()
}

// events 是 SSE：状态变化时发一个 "changed"（最多每 200ms 一次），前端再拉 /api/state。
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan struct{}, 1)
	s.subMu.Lock()
	s.subs[ch] = true
	s.subMu.Unlock()
	defer func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}()
	fmt.Fprint(w, "event: changed\ndata: {}\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprint(w, "event: changed\ndata: {}\n\n")
			fl.Flush()
			time.Sleep(200 * time.Millisecond)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	items, pending, rate := s.sess.Snapshot()
	s.expMu.Lock()
	var exp *exportState
	if s.export != nil {
		cp := *s.export
		exp = &cp
	}
	s.expMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "pending": pending, "settings": s.settings.get(), "export": exp,
		"native": nativeSupported(), "ocr": s.opts.Detector.OCR != nil, "ocrError": s.opts.OCRError, "version": s.opts.Version,
		"platform": runtime.GOOS, "rate": rate, "update": s.updateSnapshot(), "cpu": runtime.NumCPU(),
		"workers": Workers(s.settings.get().Workers), "hinted": len(s.settings.get().Hints) > 0, "dataDir": s.opts.DataDir,
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	before := s.settings.get()
	cur, err := s.settings.update(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if before.Layout != cur.Layout || before.DPI != cur.DPI {
		s.sess.DropPreviews()
	}
	s.sess.SetWorkers(cur.Workers)
	s.broadcast()
	writeJSON(w, http.StatusOK, cur)
}

// ------------------------------------------------------------------ 加入照片

func (s *Server) addPaths(w http.ResponseWriter, r *http.Request) {
	var body struct{ Paths []string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	s.respondAdd(w, body.Paths)
}

func (s *Server) respondAdd(w http.ResponseWriter, paths []string) {
	if len(paths) == 0 {
		writeJSON(w, http.StatusOK, AddResult{})
		return
	}
	res, err := s.sess.AddPaths(paths)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "warnings": res.Warnings})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// addUpload 接收网页里拖入/粘贴的文件（流式写盘）。字段 files 可以有多个，文件名里允许带相对路径（文件夹拖入）。
func (s *Server) addUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要 multipart 上传"})
		return
	}
	dir, err := s.sess.TempDir("up-")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var paths []string
	for n := 0; ; n++ {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传中断: " + err.Error()})
			return
		}
		if part.FileName() == "" {
			continue
		}
		name := cleanUploadName(part.FileName())
		if !photo.IsImage(name) && !strings.EqualFold(path.Ext(name), ".zip") {
			part.Close()
			continue
		}
		// 每个文件单独一个子目录，保留原文件名（文件名里的时间也能参与识别）
		sub := filepath.Join(dir, strconv.Itoa(n))
		os.MkdirAll(sub, 0o755)
		dst := filepath.Join(sub, path.Base(name))
		f, err := os.Create(dst)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		_, err = io.Copy(f, part)
		f.Close()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传中断: " + err.Error()})
			return
		}
		paths = append(paths, dst)
	}
	s.respondAdd(w, paths)
}

// cleanUploadName 只保留安全的文件名部分。
func cleanUploadName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	base := path.Base(name)
	base = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, base)
	if base == "." || base == ".." || base == "/" || base == "" {
		base = "image.png"
	}
	return base
}

// addURLs 处理只带链接的拖放：file:// 当本地路径，http(s) 下载到临时目录。
func (s *Server) addURLs(w http.ResponseWriter, r *http.Request) {
	var body struct{ URLs []string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	var paths []string
	var warns []string
	for _, raw := range body.URLs {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		switch u.Scheme {
		case "file":
			paths = append(paths, fileURLPath(u))
		case "http", "https":
			p, err := s.download(r.Context(), u)
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s: %v", raw, err))
				continue
			}
			paths = append(paths, p)
		}
	}
	res, err := s.sess.AddPaths(paths)
	res.Warnings = append(warns, res.Warnings...)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "warnings": res.Warnings})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func fileURLPath(u *url.URL) string {
	p := u.Path
	if runtime.GOOS == "windows" {
		if u.Host != "" && u.Host != "localhost" { // \\server\share\...
			return `\\` + u.Host + filepath.FromSlash(p)
		}
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p)
}

func (s *Server) download(ctx context.Context, u *url.URL) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: %s", resp.Status)
	}
	name := path.Base(u.Path)
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = params["filename"]
	}
	if !photo.IsImage(name) && !strings.EqualFold(path.Ext(name), ".zip") {
		switch strings.Split(resp.Header.Get("Content-Type"), ";")[0] {
		case "image/jpeg":
			name = "image.jpg"
		case "image/png":
			name = "image.png"
		default:
			return "", errors.New("不是图片")
		}
	}
	dir, err := s.sess.TempDir("url-")
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, cleanUploadName(name))
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if n, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20+1)); err != nil || n > 200<<20 {
		return "", errors.New("文件太大或下载中断")
	}
	return dst, nil
}

// pasteNative 读剪贴板里的文件路径（资源管理器/微信"复制"的文件），拿到的是原文件。
func (s *Server) pasteNative(w http.ResponseWriter, r *http.Request) {
	paths, err := clipboardFiles()
	if err != nil || len(paths) == 0 {
		writeJSON(w, http.StatusOK, AddResult{})
		return
	}
	s.respondAdd(w, paths)
}

type fileFilter struct{ Name, Spec string }

// pick 弹出原生对话框。kind: files | folder | zip | outdir。
func (s *Server) pick(w http.ResponseWriter, r *http.Request) {
	var body struct{ Kind string }
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	var paths []string
	var err error
	switch body.Kind {
	case "files":
		paths, err = pickFiles("添加照片", []fileFilter{{"图片 (*.jpg;*.jpeg;*.png)", "*.jpg;*.jpeg;*.png"}}, true)
	case "zip":
		paths, err = pickFiles("添加压缩包", []fileFilter{{"zip 压缩包 (*.zip)", "*.zip"}}, true)
	case "folder", "outdir":
		var p string
		title := "添加文件夹"
		if body.Kind == "outdir" {
			title = "选择输出位置"
		}
		p, err = pickFolder(title)
		if p != "" {
			paths = []string{p}
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未知类型"})
		return
	}
	if errors.Is(err, errUnsupported) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if body.Kind == "outdir" {
		if len(paths) == 1 {
			patch, _ := json.Marshal(map[string]string{"outDir": paths[0]})
			s.settings.update(patch)
			s.broadcast()
		}
		writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
		return
	}
	s.respondAdd(w, paths)
}

// ------------------------------------------------------------------ 单张

func itemID(r *http.Request) int {
	id, _ := strconv.Atoi(r.PathValue("id"))
	return id
}

func (s *Server) thumb(w http.ResponseWriter, r *http.Request) {
	data := s.sess.Thumb(itemID(r))
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(data)
}

// preview: kind=orig 返回原图文件；kind=print 返回 6 寸效果图。
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	id := itemID(r)
	it, ok := s.sess.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("kind") == "print" {
		st := s.settings.get()
		lo := print6.DefaultOptions()
		lo.Mode = print6.Mode(st.Layout)
		lo.DPI = 150 // 预览用一半分辨率，够看
		data, err := s.sess.Preview(id, lo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, it.path)
}

func (s *Server) patchItem(w http.ResponseWriter, r *http.Request) {
	var body struct{ Time string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	if err := s.sess.SetTime(itemID(r), strings.TrimSpace(body.Time)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	it, _ := s.sess.Get(itemID(r))
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) redetect(w http.ResponseWriter, r *http.Request) {
	if err := s.sess.Redetect(itemID(r)); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "这张正在识别或已被移除"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int
		All bool
	}
	json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	if len(body.IDs) == 0 && !body.All {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有指定照片"})
		return
	}
	s.sess.Remove(body.IDs)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ------------------------------------------------------------------ 导出

func (s *Server) startExport(w http.ResponseWriter, r *http.Request) {
	s.expMu.Lock()
	if s.export != nil && s.export.Running {
		s.expMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "正在导出"})
		return
	}
	items, presets, skipped := s.sess.ExportList()
	if len(items) == 0 {
		s.expMu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有可导出的照片（还在识别中或都打不开）"})
		return
	}
	st := s.settings.get()
	now := time.Now()
	out := st.OutDir // 默认直接导出到所选文件夹（比如"资料/10/"），内容相同的照片不会重复复制
	report := path.Join("导出记录", now.Format("2006-01-02 150405")+".csv")
	if st.NewFolder {
		out = filepath.Join(st.OutDir, now.Format("2006-01-02 150405")+" 导出")
		report = job.Report
	}
	lo := print6.DefaultOptions()
	lo.Mode = print6.Mode(st.Layout)
	lo.DPI = st.DPI
	o := job.Options{Sort: st.Mode != "print", Print: st.Mode != "sort", OutDir: out, Layout: lo,
		PDF: st.PDF, Bleed: st.Bleed, Quality: st.Quality, Preset: presets, Workers: Workers(st.Workers),
		DirFormat: st.DirFormat, NameFormat: st.NameFormat, PrintByDate: !st.PrintFlat, ReportName: report}
	ctx, cancel := context.WithCancel(context.Background())
	exp := &exportState{Running: true, Total: len(items), OutDir: out, Skipped: skipped, cancel: cancel}
	s.export = exp
	s.expMu.Unlock()
	s.broadcast()

	go func() {
		sum, err := job.Run(ctx, items, s.opts.Detector, o, func(done, total int, res job.Result) {
			s.expMu.Lock()
			exp.Done, exp.Current = done, res.Name
			if res.Err != "" {
				exp.Failed++
			} else {
				exp.OK++
			}
			if o.Sort && res.Err == "" && res.Stamp.Source == stamp.None {
				exp.Unknown++
			}
			s.expMu.Unlock()
			s.broadcast()
		})
		s.expMu.Lock()
		exp.Running, exp.Finished, exp.Current = false, true, ""
		if sum != nil {
			exp.Elapsed = sum.Elapsed.Seconds()
			for _, p := range sum.PDFs {
				exp.PDFs = append(exp.PDFs, filepath.Join(out, filepath.FromSlash(p)))
			}
		}
		if err != nil {
			exp.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				exp.Error = "已取消"
			}
			s.logf("导出出错: %v", err)
		}
		s.expMu.Unlock()
		s.broadcast()
	}()
	writeJSON(w, http.StatusOK, map[string]any{"outDir": out, "total": len(items)})
}

func (s *Server) cancelExport(w http.ResponseWriter, r *http.Request) {
	s.expMu.Lock()
	if s.export != nil && s.export.Running {
		s.export.cancel()
	}
	s.expMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// open 用系统默认程序打开文件或文件夹，只允许本次导出目录里的路径（或输出目录本身）。
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var body struct{ Path string }
	json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body)
	allowed := []string{s.settings.get().OutDir}
	s.expMu.Lock()
	if s.export != nil {
		allowed = append(allowed, s.export.OutDir)
	}
	s.expMu.Unlock()
	target := filepath.Clean(body.Path)
	ok := false
	for _, root := range allowed {
		if rel, err := filepath.Rel(root, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			ok = true
		}
	}
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只能打开导出目录里的文件"})
		return
	}
	if _, err := os.Stat(target); err != nil {
		if target == filepath.Clean(allowed[0]) { // 输出目录还不存在时先建好
			os.MkdirAll(target, 0o755)
		} else {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "文件不存在"})
			return
		}
	}
	if err := openPath(target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// vcRedistURL 是微软官方 VC++ 运行库下载地址（onnxruntime.dll 依赖它）。
const vcRedistURL = "https://aka.ms/vs/17/release/vc_redist.x64.exe"

func (s *Server) openVCRedist(w http.ResponseWriter, r *http.Request) {
	if err := openPath(vcRedistURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// postDiag 记录前端的拖放诊断（数据类型、文件名、大小），写进日志，方便排查微信等来源。
func (s *Server) postDiag(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if !json.Valid(data) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不是 JSON"})
		return
	}
	s.diagMu.Lock()
	s.diags = append(s.diags, data)
	if len(s.diags) > 20 {
		s.diags = s.diags[1:]
	}
	s.diagMu.Unlock()
	s.logf("拖放诊断 %s", data)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ------------------------------------------------------------------ 启动

// Run 启动桌面版，窗口关闭（或 Ctrl+C）后返回。
func Run(ctx context.Context, o Options) error {
	if o.Addr == "" {
		o.Addr = "127.0.0.1:0"
	}
	if o.DataDir == "" {
		o.DataDir = configDir()
	}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return err
	}
	host := ln.Addr().String()
	s, err := newServer(o, host)
	if err != nil {
		ln.Close()
		return err
	}
	defer s.sess.Close()
	hs := &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go hs.Serve(ln)
	defer hs.Close()
	s.writeInstance()
	defer s.removeInstance()
	go cleanStaleTemp()
	go s.autoCheckUpdate()

	if len(o.Paths) > 0 {
		if res, err := s.sess.AddPaths(o.Paths); err != nil {
			s.logf("加入启动参数失败: %v", err)
		} else if len(res.Warnings) > 0 {
			s.logf("加入启动参数: %v", res.Warnings)
		}
	}
	link := "http://" + host + "/?t=" + s.token
	s.logf("启动 %s，地址 %s", o.Version, "http://"+host)

	cache, err := os.UserCacheDir() // Windows: %LOCALAPPDATA%，浏览器缓存不该放进漫游目录
	if err != nil {
		cache = o.DataDir
	}
	if !o.Browser && runWindow(link, "stampsort 水印照片整理", filepath.Join(cache, "stampsort", "webview2")) {
		return nil // 窗口已关闭
	}
	// 浏览器模式：没有 WebView2，或其它平台
	fmt.Printf("stampsort 桌面版已启动: %s\n（关闭本程序即退出）\n", link)
	if !o.NoOpen {
		if err := openPath(link); err != nil {
			log.Printf("打开浏览器失败: %v", err)
		}
	}
	return waitBrowserMode(ctx)
}

// ShowError 在 GUI 模式下（没有控制台）用提示框报告致命错误；其它平台什么也不做。
func ShowError(title, msg string) { showError(title, msg) }
