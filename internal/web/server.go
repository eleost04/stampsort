// Package web 是网页版：上传照片/zip → 后台排队处理 → 查看进度 → 下载结果 zip。
package web

import (
	"archive/zip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eleost04/stampsort/internal/input"
	"github.com/eleost04/stampsort/internal/job"
	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
)

//go:embed static
var staticFS embed.FS

// Config 是网页服务的参数。
type Config struct {
	DataDir   string        // 任务文件存放目录
	Password  string        // 为空表示不需要登录
	MaxUpload int64         // 单次上传字节上限
	Keep      time.Duration // 任务保留时长
	Detector  *stamp.Detector
	Version   string
}

// Server 实现 http.Handler。
type Server struct {
	cfg    Config
	key    []byte
	mux    *http.ServeMux
	queue  chan *Job
	mu     sync.Mutex
	jobs   map[string]*Job
	failMu sync.Mutex
}

// Job 是一个网页任务。
type Job struct {
	ID      string
	Created time.Time
	dir     string
	opts    job.Options
	sources []string // 上传的文件（图片或 zip），worker 再展开
	cancel  context.CancelFunc
	ctx     context.Context

	mu       sync.Mutex
	State    string // queued | running | done | error | cancelled
	Done     int
	Total    int
	Results  []resultJSON
	PDFs     []string
	Warnings []string
	Error    string
	Elapsed  float64
}

type resultJSON struct {
	Name     string   `json:"name"`
	Time     string   `json:"time,omitempty"`
	Source   string   `json:"source,omitempty"`
	Raw      string   `json:"raw,omitempty"`
	Sorted   string   `json:"sorted,omitempty"`
	Print    string   `json:"print,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Err      string   `json:"err,omitempty"`
}

// New 创建服务并启动后台处理和过期清理。
func New(cfg Config) (*Server, error) {
	if cfg.MaxUpload <= 0 {
		cfg.MaxUpload = 1 << 30
	}
	if cfg.Keep <= 0 {
		cfg.Keep = 24 * time.Hour
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "jobs"), 0o755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte("stampsort-session\x00" + cfg.Password))
	s := &Server{cfg: cfg, key: sum[:], mux: http.NewServeMux(), queue: make(chan *Job, 64), jobs: map[string]*Job{}}

	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServerFS(static)
	s.mux.Handle("GET /", noCache(files))
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/logout", s.logout)
	s.mux.HandleFunc("GET /api/me", s.auth(s.me))
	s.mux.HandleFunc("POST /api/jobs", s.auth(s.createJob))
	s.mux.HandleFunc("GET /api/jobs/{id}", s.auth(s.getJob))
	s.mux.HandleFunc("DELETE /api/jobs/{id}", s.auth(s.deleteJob))
	s.mux.HandleFunc("GET /api/jobs/{id}/download", s.auth(s.download))

	go s.worker()
	go s.janitor()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	s.mux.ServeHTTP(w, r)
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------ 登录

const cookieName = "stampsort"
const sessionTTL = 30 * 24 * time.Hour

func (s *Server) sign(exp int64) string {
	m := hmac.New(sha256.New, s.key)
	fmt.Fprint(m, exp)
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(m.Sum(nil))
}

func (s *Server) authed(r *http.Request) bool {
	if s.cfg.Password == "" {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	expStr, _, ok := strings.Cut(c.Value, ".")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if !ok || err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(s.sign(exp)))
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "需要登录"})
			return
		}
		h(w, r)
	}
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ Password string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	if s.cfg.Password != "" && subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.Password)) != 1 {
		s.failMu.Lock() // 串行 + 延时，拖慢暴力猜密码
		time.Sleep(time.Second)
		s.failMu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密码不对"})
		return
	}
	exp := time.Now().Add(sessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.sign(exp), Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ocr": s.cfg.Detector != nil && s.cfg.Detector.OCR != nil,
		"maxUpload": s.cfg.MaxUpload, "version": s.cfg.Version, "auth": s.cfg.Password != ""})
}

// ------------------------------------------------------------------ 任务

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// createJob 流式接收 multipart 上传：字段 mode/layout/pdf/bleed，文件字段 files（可多个，可以是 zip）。
func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要 multipart 上传"})
		return
	}
	id := newID()
	dir := filepath.Join(s.cfg.DataDir, "jobs", id)
	inDir := filepath.Join(dir, "in")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fail := func(code int, msg string) {
		os.RemoveAll(dir)
		writeJSON(w, code, map[string]string{"error": msg})
	}

	o := job.Options{Sort: true, Print: true, PDF: true, Layout: print6.DefaultOptions(), OutDir: filepath.Join(dir, "out")}
	var srcs []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				fail(http.StatusRequestEntityTooLarge, fmt.Sprintf("上传超过上限 %dMB", s.cfg.MaxUpload>>20))
				return
			}
			fail(http.StatusBadRequest, "上传中断: "+err.Error())
			return
		}
		if part.FileName() == "" { // 普通字段
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			setOption(&o, part.FormName(), strings.TrimSpace(string(v)))
			continue
		}
		name := safeName(part.FileName())
		if !strings.EqualFold(filepath.Ext(name), ".zip") && !isImageName(name) {
			part.Close()
			continue
		}
		// 每个文件一个子目录，保留原文件名（文件名里的时间也能参与识别）
		sub := filepath.Join(inDir, strconv.Itoa(len(srcs)))
		os.MkdirAll(sub, 0o755)
		dst := filepath.Join(sub, name)
		f, err := os.Create(dst)
		if err != nil {
			fail(http.StatusInternalServerError, err.Error())
			return
		}
		_, err = io.Copy(f, part)
		f.Close()
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				fail(http.StatusRequestEntityTooLarge, fmt.Sprintf("上传超过上限 %dMB", s.cfg.MaxUpload>>20))
				return
			}
			fail(http.StatusBadRequest, "上传中断: "+err.Error())
			return
		}
		srcs = append(srcs, dst)
	}
	if len(srcs) == 0 {
		fail(http.StatusBadRequest, "没有收到照片或 zip")
		return
	}
	if !o.Sort && !o.Print {
		fail(http.StatusBadRequest, "至少选择分类或 6 寸中的一项")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{ID: id, Created: time.Now(), dir: dir, opts: o, sources: srcs, ctx: ctx, cancel: cancel, State: "queued"}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	select {
	case s.queue <- j:
	default:
		s.mu.Lock()
		delete(s.jobs, id)
		s.mu.Unlock()
		cancel()
		fail(http.StatusServiceUnavailable, "排队任务太多，请稍后再试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func setOption(o *job.Options, k, v string) {
	on := v == "1" || v == "true" || v == "on"
	switch k {
	case "mode":
		o.Sort = v == "all" || v == "sort"
		o.Print = v == "all" || v == "print"
	case "layout":
		if v == string(print6.ModeCrop) {
			o.Layout.Mode = print6.ModeCrop
		} else {
			o.Layout.Mode = print6.ModeC
		}
	case "pdf":
		o.PDF = on
	case "bleed":
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 10 {
			o.Bleed = f
		}
	}
}

// safeName 只保留文件名部分，去掉路径和控制字符。
func safeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	if name == "." || name == ".." || name == "/" || name == "" {
		name = "upload"
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		name = name[:200-len(ext)] + ext
	}
	return name
}

func isImageName(n string) bool {
	switch strings.ToLower(filepath.Ext(n)) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

func (s *Server) worker() {
	for j := range s.queue {
		s.run(j)
	}
}

func (s *Server) run(j *Job) {
	if j.ctx.Err() != nil {
		return
	}
	j.mu.Lock()
	j.State = "running"
	srcs := j.sources
	j.mu.Unlock()

	tmp := filepath.Join(j.dir, "tmp")
	os.MkdirAll(tmp, 0o755)
	defer os.RemoveAll(tmp)
	defer os.RemoveAll(filepath.Join(j.dir, "in"))

	items, warns, err := input.Collect(srcs, tmp, input.DefaultLimits)
	j.mu.Lock()
	j.Warnings = warns
	j.Total = len(items)
	j.Results = make([]resultJSON, len(items))
	for i, it := range items {
		j.Results[i] = resultJSON{Name: it.Name}
	}
	j.mu.Unlock()
	if err == nil && len(items) == 0 {
		err = errors.New("没有找到可处理的照片（支持 jpg/png 和 zip）")
	}
	if err != nil {
		j.finish("error", err.Error())
		return
	}

	sum, err := job.Run(j.ctx, items, s.cfg.Detector, j.opts, func(done, total int, r job.Result) {
		j.mu.Lock()
		j.Done = done
		j.Results[r.Index] = toJSON(r)
		j.mu.Unlock()
	})
	j.mu.Lock()
	if sum != nil {
		j.PDFs = sum.PDFs
		j.Elapsed = sum.Elapsed.Seconds()
	}
	j.mu.Unlock()
	switch {
	case errors.Is(err, context.Canceled):
		j.finish("cancelled", "已取消")
	case err != nil:
		j.finish("error", err.Error())
	default:
		j.finish("done", "")
	}
}

func (j *Job) finish(state, msg string) {
	j.mu.Lock()
	j.State, j.Error = state, msg
	j.mu.Unlock()
}

func toJSON(r job.Result) resultJSON {
	out := resultJSON{Name: r.Name, Source: string(r.Stamp.Source), Raw: r.Stamp.Raw, Sorted: r.Sorted, Print: r.Print, Warnings: r.Warnings, Err: r.Err}
	if r.Stamp.Source != stamp.None && r.Stamp.Source != "" {
		out.Time = r.Stamp.Time.Format("2006-01-02 15:04")
		if !r.Stamp.HasClock {
			out.Time = r.Stamp.Time.Format("2006-01-02")
		}
	}
	return out
}

func (s *Server) lookup(r *http.Request) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[r.PathValue("id")]
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j := s.lookup(r)
	if j == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在或已过期"})
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"id": j.ID, "state": j.State, "done": j.Done, "total": j.Total, "results": j.Results,
		"pdfs": j.PDFs, "warnings": j.Warnings, "error": j.Error, "elapsed": j.Elapsed,
		"expires": j.Created.Add(s.cfg.Keep).Format(time.RFC3339),
	})
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	j := s.lookup(r)
	if j == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
		return
	}
	s.remove(j)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) remove(j *Job) {
	j.cancel()
	s.mu.Lock()
	delete(s.jobs, j.ID)
	s.mu.Unlock()
	go func() {
		// 等 worker 停下来再删目录
		for range 60 {
			j.mu.Lock()
			st := j.State
			j.mu.Unlock()
			if st != "running" {
				break
			}
			time.Sleep(time.Second)
		}
		os.RemoveAll(j.dir)
	}()
}

// download 把输出目录流式打成 zip。JPEG/PDF 本身已压缩，用 Store 不再压，速度快。
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	j := s.lookup(r)
	if j == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在或已过期"})
		return
	}
	j.mu.Lock()
	state := j.State
	j.mu.Unlock()
	if state != "done" && state != "cancelled" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "任务还没完成"})
		return
	}
	out := filepath.Join(j.dir, "out")
	name := "stampsort-" + j.Created.Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	zw := zip.NewWriter(w)
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, _ := zip.FileInfoHeader(info)
		h.Name = filepath.ToSlash(rel)
		h.Method = zip.Store
		if strings.HasSuffix(h.Name, ".csv") {
			h.Method = zip.Deflate
		}
		h.Flags |= 0x800 // UTF-8 文件名，Windows 解压不乱码
		zf, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(zf, f)
		return err
	})
	if err != nil {
		log.Printf("下载 %s 出错: %v", j.ID, err)
	}
	zw.Close()
}

// janitor 每小时删除过期任务；启动时清掉上次运行残留的目录（任务只在内存里，重启后无法下载）。
// 之后只删超过保留时长的未知目录，避免误删正在上传、还没登记的任务。
func (s *Server) janitor() {
	root := filepath.Join(s.cfg.DataDir, "jobs")
	for first := true; ; first = false {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			s.mu.Lock()
			j := s.jobs[e.Name()]
			s.mu.Unlock()
			if j != nil {
				if time.Since(j.Created) > s.cfg.Keep {
					s.remove(j)
				}
				continue
			}
			if info, err := e.Info(); err == nil && (first || time.Since(info.ModTime()) > s.cfg.Keep) {
				os.RemoveAll(filepath.Join(root, e.Name()))
			}
		}
		time.Sleep(time.Hour)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
