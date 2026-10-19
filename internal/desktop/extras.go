package desktop

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eleost04/stampsort/internal/update"
)

// ------------------------------------------------------------------ 自动更新

type updateState struct {
	Current     string    `json:"current"`
	Checking    bool      `json:"checking"`
	Available   bool      `json:"available"`
	Latest      string    `json:"latest,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	Size        int64     `json:"size,omitempty"`
	Source      string    `json:"source,omitempty"`
	Error       string    `json:"error,omitempty"`
	Checked     time.Time `json:"checked,omitempty"`
	Downloading bool      `json:"downloading"`
	Done        int64     `json:"done"`
	Installing  bool      `json:"installing"`
	Dismissed   bool      `json:"dismissed"`
	found       *update.Found
}

type updater struct {
	mu sync.Mutex
	st updateState
}

func (s *Server) updateSnapshot() updateState {
	s.upd.mu.Lock()
	defer s.upd.mu.Unlock()
	st := s.upd.st
	st.Current = s.opts.Version
	return st
}

// checkUpdate 检查新版本。auto 为 true 时是启动后的自动检查（每天最多一次，出错不提示）。
func (s *Server) checkUpdate(ctx context.Context) updateState {
	s.upd.mu.Lock()
	if s.upd.st.Checking || s.upd.st.Downloading || s.upd.st.Installing {
		st := s.upd.st
		s.upd.mu.Unlock()
		return st
	}
	s.upd.st.Checking, s.upd.st.Error = true, ""
	s.upd.mu.Unlock()
	s.broadcast()

	found, err := update.Check(ctx, update.Sources(s.settings.get().UpdateSource), update.PublicKey())
	s.settings.setLastCheck(time.Now())

	s.upd.mu.Lock()
	st := &s.upd.st
	st.Checking, st.Checked = false, time.Now()
	if err != nil {
		st.Error = err.Error()
		s.logf("检查更新失败: %v", err)
	} else {
		st.found = found
		st.Latest, st.Notes, st.Size, st.Source = found.Manifest.Version, found.Manifest.Notes, found.Manifest.Installer.Size, found.Source.Name()
		st.Available = update.Newer(found.Manifest.Version, s.opts.Version)
	}
	out := *st
	s.upd.mu.Unlock()
	s.broadcast()
	return out
}

func (s *Server) autoCheckUpdate() {
	st := s.settings.get()
	if st.NoAutoUpdate || time.Since(st.LastCheck) < 20*time.Hour || s.opts.Version == "dev" {
		return
	}
	time.Sleep(3 * time.Second) // 别和启动抢资源
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.checkUpdate(ctx)
}

func (s *Server) apiCheckUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	writeJSON(w, http.StatusOK, s.checkUpdate(ctx))
}

func (s *Server) apiDismissUpdate(w http.ResponseWriter, r *http.Request) {
	s.upd.mu.Lock()
	s.upd.st.Dismissed = true
	s.upd.mu.Unlock()
	s.broadcast()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// apiInstallUpdate 下载新版安装包（校验签名和 sha256），静默安装并退出程序；安装完会自动重新打开。
// 程序自己下载的文件不带"来自网络"标记，不会触发 SmartScreen。
func (s *Server) apiInstallUpdate(w http.ResponseWriter, r *http.Request) {
	if !installerSupported() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "只有 Windows 版支持自动安装，请手动下载新版"})
		return
	}
	s.expMu.Lock()
	exporting := s.export != nil && s.export.Running
	s.expMu.Unlock()
	if exporting {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "正在导出，完成后再更新"})
		return
	}
	s.upd.mu.Lock()
	st := &s.upd.st
	if !st.Available || st.found == nil || st.Downloading || st.Installing {
		s.upd.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "没有可安装的新版本"})
		return
	}
	found := st.found
	st.Downloading, st.Done, st.Error = true, 0, ""
	s.upd.mu.Unlock()
	s.broadcast()

	go func() {
		dir, err := os.MkdirTemp("", "stampsort-update-")
		var p string
		if err == nil {
			last := time.Now()
			p, err = update.Download(context.Background(), found, update.Sources(s.settings.get().UpdateSource), dir, func(done, total int64) {
				s.upd.mu.Lock()
				s.upd.st.Done = done
				s.upd.mu.Unlock()
				if time.Since(last) > 300*time.Millisecond {
					last = time.Now()
					s.broadcast()
				}
			})
		}
		s.upd.mu.Lock()
		s.upd.st.Downloading = false
		if err != nil {
			s.upd.st.Error = "下载失败：" + err.Error()
			s.upd.mu.Unlock()
			s.logf("下载更新失败: %v", err)
			s.broadcast()
			return
		}
		s.upd.st.Installing = true
		s.upd.mu.Unlock()
		s.broadcast()
		s.logf("开始安装 %s", found.Manifest.Version)
		// 安装程序等本程序退出后覆盖文件，装完自动重新打开（/relaunch）
		if err := exec.Command(p, "/S", "/relaunch").Start(); err != nil {
			s.upd.mu.Lock()
			s.upd.st.Installing, s.upd.st.Error = false, "启动安装程序失败："+err.Error()
			s.upd.mu.Unlock()
			s.broadcast()
			return
		}
		time.Sleep(500 * time.Millisecond)
		quitApp()
	}()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ------------------------------------------------------------------ 单实例

// instanceInfo 写在配置目录里，第二个实例（再次双击、"发送到"）读它把文件转交给已打开的窗口。
type instanceInfo struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func instancePath(dataDir string) string { return filepath.Join(dataDir, "instance.json") }

func (s *Server) writeInstance() {
	data, _ := json.Marshal(instanceInfo{Addr: s.host, Token: s.instToken, PID: os.Getpid()})
	os.WriteFile(instancePath(s.opts.DataDir), data, 0o600)
}

func (s *Server) removeInstance() {
	p := instancePath(s.opts.DataDir)
	var cur instanceInfo
	if data, err := os.ReadFile(p); err == nil && json.Unmarshal(data, &cur) == nil && cur.Token == s.instToken {
		os.Remove(p)
	}
}

// apiRemoteOpen 接收第二个实例转交的文件。用单独的实例令牌（请求头）认证，不走 cookie。
func (s *Server) apiRemoteOpen(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Instance-Token")), []byte(s.instToken)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "未授权"})
		return
	}
	var body struct{ Paths []string }
	json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body)
	bringToFront()
	if len(body.Paths) > 0 {
		if _, err := s.sess.AddPaths(body.Paths); err != nil {
			s.logf("转交的文件加入失败: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Forward 把文件转交给已经在运行的实例。成功返回 true，调用方直接退出。
func Forward(dataDir string, paths []string) bool {
	if dataDir == "" {
		dataDir = configDir()
	}
	data, err := os.ReadFile(instancePath(dataDir))
	if err != nil {
		return false
	}
	var inf instanceInfo
	if json.Unmarshal(data, &inf) != nil || inf.Addr == "" || !strings.HasPrefix(inf.Addr, "127.0.0.1:") {
		return false
	}
	allowForeground(inf.PID)
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		if a, err := filepath.Abs(p); err == nil {
			abs = append(abs, a)
		}
	}
	body, _ := json.Marshal(map[string]any{"paths": abs})
	req, _ := http.NewRequest(http.MethodPost, "http://"+inf.Addr+"/api/remote/open", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Instance-Token", inf.Token)
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ------------------------------------------------------------------ 其它

func (s *Server) apiResetHint(w http.ResponseWriter, r *http.Request) {
	s.opts.Detector.ResetHints()
	s.settings.clearHints()
	s.broadcast()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiOpenLogs(w http.ResponseWriter, r *http.Request) {
	os.MkdirAll(s.opts.DataDir, 0o755)
	if err := openPath(s.opts.DataDir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// cleanStaleTemp 删除上次异常退出留下的临时目录（超过一天的）。
func cleanStaleTemp() {
	for _, pat := range []string{"stampsort-gui-*", "stampsort-update-*"} {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), pat))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && time.Since(st.ModTime()) > 24*time.Hour {
				os.RemoveAll(m)
			}
		}
	}
}
