package desktop

import (
	"bytes"
	"encoding/json"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
)

func jpegFile(t *testing.T, dir, name string, seed byte) string {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for i := range im.Pix {
		im.Pix[i] = uint8(i) + seed
	}
	p := filepath.Join(dir, name)
	if err := print6.SaveJPEG(p, im, 80, 72); err != nil {
		t.Fatal(err)
	}
	return p
}

type testEnv struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
	c   *http.Client
}

func newEnv(t *testing.T) *testEnv {
	ts := httptest.NewUnstartedServer(nil)
	host := ts.Listener.Addr().String()
	s, err := newServer(Options{Detector: &stamp.Detector{}, DataDir: t.TempDir()}, host)
	if err != nil {
		t.Fatal(err)
	}
	s.settings.update([]byte(`{"outDir":` + jsonStr(t.TempDir()) + `}`))
	ts.Config.Handler = s
	ts.Start()
	t.Cleanup(func() { ts.Close(); s.sess.Close() })
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := c.Get(ts.URL + "/?t=" + s.token)
	if err != nil || r.StatusCode != http.StatusFound {
		t.Fatalf("登录跳转失败: %v %v", err, r.StatusCode)
	}
	u, _ := url.Parse(ts.URL)
	jar.SetCookies(u, r.Cookies())
	return &testEnv{t, s, ts, c}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func (e *testEnv) post(path string, body any) map[string]any {
	e.t.Helper()
	data, _ := json.Marshal(body)
	r, err := e.c.Post(e.ts.URL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	var out map[string]any
	json.NewDecoder(r.Body).Decode(&out)
	if r.StatusCode != 200 {
		e.t.Fatalf("%s: %d %v", path, r.StatusCode, out)
	}
	return out
}

type stateResp struct {
	Items   []Item
	Pending int
	Export  *exportState
}

func (e *testEnv) waitIdle() stateResp {
	e.t.Helper()
	for range 200 {
		r, _ := e.c.Get(e.ts.URL + "/api/state")
		var st stateResp
		json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st.Pending == 0 && (st.Export == nil || !st.Export.Running) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("等待超时")
	return stateResp{}
}

func TestAuthAndHost(t *testing.T) {
	e := newEnv(t)
	// 没有 cookie
	r, _ := http.Get(e.ts.URL + "/api/state")
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("无 cookie 应 403，得到 %d", r.StatusCode)
	}
	// 错误令牌
	r, _ = http.Get(e.ts.URL + "/?t=wrong")
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("错误令牌应 403，得到 %d", r.StatusCode)
	}
	// 伪造 Host（DNS rebinding）
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/state", nil)
	req.Host = "evil.example:80"
	r, _ = e.c.Do(req)
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("错误 Host 应 403，得到 %d", r.StatusCode)
	}
	// 跨站 Origin 的修改请求
	req, _ = http.NewRequest("POST", e.ts.URL+"/api/remove", strings.NewReader(`{"all":true}`))
	req.Header.Set("Origin", "http://evil.example")
	r, _ = e.c.Do(req)
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("跨站 Origin 应 403，得到 %d", r.StatusCode)
	}
	// 带 cookie 正常访问
	r, _ = e.c.Get(e.ts.URL + "/")
	if r.StatusCode != 200 {
		t.Errorf("首页 %d", r.StatusCode)
	}
}

func TestAddReviewExport(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	a := jpegFile(t, src, "IMG_20260813_062001.jpg", 0)
	jpegFile(t, src, "微信图片_20260814090000.jpg", 1)
	b := jpegFile(t, src, "DSC0001.jpg", 2)

	// 1. 按路径加入（目录），再加一次同一张 → 去重
	res := e.post("/api/add/paths", map[string]any{"paths": []string{src}})
	if res["added"].(float64) != 3 {
		t.Fatalf("加入 %v", res)
	}
	res = e.post("/api/add/paths", map[string]any{"paths": []string{a}})
	if res["dup"].(float64) != 1 {
		t.Errorf("重复加入应跳过: %v", res)
	}

	// 2. 上传（模拟网页拖入/粘贴），内容与已加入的不同
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("files", "image.png")
	im := image.NewRGBA(image.Rect(0, 0, 80, 60))
	var pngBuf bytes.Buffer
	print6.EncodeJPEG(&pngBuf, im, 80, 72)
	fw.Write(pngBuf.Bytes())
	fw, _ = mw.CreateFormFile("files", "../../evil.exe")
	fw.Write([]byte("x"))
	mw.Close()
	r, err := e.c.Post(e.ts.URL+"/api/add/upload", mw.FormDataContentType(), &body)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("上传失败 %v %v", err, r.StatusCode)
	}

	// 3. file:// 链接
	bb := jpegFile(t, t.TempDir(), "IMG_20260815_100000.jpg", 3)
	res = e.post("/api/add/urls", map[string]any{"urls": []string{"file://" + bb}})
	if res["added"].(float64) != 1 {
		t.Errorf("file 链接: %v", res)
	}

	st := e.waitIdle()
	if len(st.Items) != 5 {
		t.Fatalf("应有 5 张，得到 %d", len(st.Items))
	}
	var unknown []Item
	for _, it := range st.Items {
		if it.Source == "none" {
			unknown = append(unknown, it)
		}
	}
	if len(unknown) != 2 { // DSC0001 和 image.png
		t.Fatalf("未识别应 2 张: %+v", st.Items)
	}
	// 缩略图
	if r, _ := e.c.Get(e.ts.URL + "/api/thumb/" + itoa(st.Items[0].ID)); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("缩略图 %d", r.StatusCode)
	}
	// 6 寸预览
	if r, _ := e.c.Get(e.ts.URL + "/api/preview/" + itoa(st.Items[0].ID) + "?kind=print"); r.StatusCode != 200 {
		t.Errorf("6 寸预览 %d", r.StatusCode)
	}

	// 4. 手动填写时间；删掉另一张
	var dscID, pngID int
	for _, it := range unknown {
		if it.Name == "DSC0001.jpg" {
			dscID = it.ID
		} else {
			pngID = it.ID
		}
	}
	req, _ := http.NewRequest("PATCH", e.ts.URL+"/api/items/"+itoa(dscID), strings.NewReader(`{"time":"2025-01-02 03:04"}`))
	if r, _ := e.c.Do(req); r.StatusCode != 200 {
		t.Fatalf("改时间 %d", r.StatusCode)
	}
	req, _ = http.NewRequest("PATCH", e.ts.URL+"/api/items/"+itoa(dscID), strings.NewReader(`{"time":"不是时间"}`))
	if r, _ := e.c.Do(req); r.StatusCode != 400 {
		t.Errorf("错误时间应 400")
	}
	e.post("/api/remove", map[string]any{"ids": []int{pngID}})

	// 5. 导出
	e.post("/api/export", map[string]any{})
	st = e.waitIdle()
	x := st.Export
	if x == nil || x.Error != "" || x.OK != 4 || x.Unknown != 0 {
		t.Fatalf("导出结果 %+v", x)
	}
	for _, want := range []string{
		"分类/2026/08/13/2026-08-13_06-20.jpg",
		"分类/2026/08/14/2026-08-14_09-00.jpg",
		"分类/2025/01/02/2025-01-02_03-04.jpg",
		"6寸/2026/08/15/待打印_横版.pdf",
	} {
		if _, err := os.Stat(filepath.Join(x.OutDir, filepath.FromSlash(want))); err != nil {
			t.Errorf("缺少 %s", want)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(x.OutDir, "导出记录", "*.csv")); len(m) != 0 {
		t.Errorf("默认不保存导出记录: %v", m)
	}
	// 打开"保存导出记录"后再导出一次：有一份记录，且照片不会重复复制
	req, _ = http.NewRequest("PUT", e.ts.URL+"/api/settings", strings.NewReader(`{"report":true}`))
	e.c.Do(req)
	time.Sleep(1100 * time.Millisecond) // 记录文件名精确到秒
	e.post("/api/export", map[string]any{})
	st = e.waitIdle()
	if m, _ := filepath.Glob(filepath.Join(x.OutDir, "导出记录", "*.csv")); len(m) != 1 {
		t.Errorf("导出记录应有 1 份: %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(x.OutDir, "分类", "2026", "08", "13", "*")); len(m) != 1 {
		t.Errorf("重复导出不应产生副本: %v", m)
	}
	// 原图不能被移动
	if _, err := os.Stat(b); err != nil {
		t.Error("原图被移动了")
	}

	// 6. 打开：只允许导出目录里的
	data, _ := json.Marshal(map[string]string{"path": "/etc/passwd"})
	if r, _ := e.c.Post(e.ts.URL+"/api/open", "application/json", bytes.NewReader(data)); r.StatusCode != http.StatusForbidden {
		t.Errorf("打开导出目录外的文件应 403，得到 %d", r.StatusCode)
	}
}

func TestSettingsPersist(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	st := loadSettings(p)
	if _, err := st.update([]byte(`{"mode":"print","layout":"crop","bleed":99}`)); err != nil {
		t.Fatal(err)
	}
	got := loadSettings(p).get()
	if got.Mode != "print" || got.Layout != "crop" || got.Bleed != 0 || got.OutDir == "" {
		t.Errorf("%+v", got)
	}
}

func TestEvents(t *testing.T) {
	e := newEnv(t)
	r, err := e.c.Get(e.ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	buf := make([]byte, 64)
	n, _ := io.ReadAtLeast(r.Body, buf, 10)
	if !strings.Contains(string(buf[:n]), "event: changed") {
		t.Errorf("SSE 首条消息 %q", buf[:n])
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// 旧版本的 settings.json（没有新字段）读进来要得到合理的默认值。
func TestSettingsMigration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"mode":"all","layout":"c","pdf":true,"bleed":0,"outDir":"D:\\照片"}`), 0o644)
	got := loadSettings(p).get()
	if got.DirFormat != "ymd" || got.NameFormat != "datetime" || got.DPI != 300 || got.Quality != 95 ||
		got.NoAutoUpdate || got.NewFolder || got.PrintFlat || got.UpdateSource != "auto" || got.OutDir != `D:\照片` {
		t.Errorf("%+v", got)
	}
	// 界面不能改 hints / lastCheck
	st := loadSettings(p)
	st.setHint(false, stamp.Hint{X0: 0, Y0: 0.8, X1: 0.5, Y1: 0.85})
	st.update([]byte(`{"hints":null,"dirFormat":"d","workers":99}`))
	if g := st.get(); len(g.Hints) != 1 || g.DirFormat != "d" || g.Workers != 0 {
		t.Errorf("%+v", g)
	}
}

// 用户场景：保存位置是"资料/10"，按"日"分、只要时间做文件名。
func TestExportByDay(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "资料", "10")
	data, _ := json.Marshal(map[string]any{"outDir": root, "dirFormat": "d", "nameFormat": "time"})
	req, _ := http.NewRequest("PUT", e.ts.URL+"/api/settings", bytes.NewReader(data))
	if r, _ := e.c.Do(req); r.StatusCode != 200 {
		t.Fatal("保存设置失败")
	}
	src := t.TempDir()
	jpegFile(t, src, "IMG_20261019_082100.jpg", 1)
	jpegFile(t, src, "IMG_20261020_142400.jpg", 2)
	e.post("/api/add/paths", map[string]any{"paths": []string{src}})
	e.waitIdle()
	e.post("/api/export", map[string]any{})
	st := e.waitIdle()
	if st.Export.OutDir != root {
		t.Errorf("应直接导出到保存位置，得到 %s", st.Export.OutDir)
	}
	for _, want := range []string{"分类/19/08-21.jpg", "分类/20/14-24.jpg", "6寸/19/08-21_6寸.jpg", "6寸/19/待打印_横版.pdf"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
			t.Errorf("缺少 %s", want)
		}
	}
}

// 第二个实例把文件转交给已运行的实例；令牌不对时拒绝。
func TestRemoteOpenAndHintReset(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	p := jpegFile(t, src, "IMG_20261019_082100.jpg", 7)
	body := strings.NewReader(`{"paths":[` + jsonStr(p) + `]}`)
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/remote/open", body)
	req.Header.Set("X-Instance-Token", "wrong")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusForbidden {
		t.Errorf("错误令牌应 403，得到 %d", r.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.ts.URL+"/api/remote/open", strings.NewReader(`{"paths":[`+jsonStr(p)+`]}`))
	req.Header.Set("X-Instance-Token", e.srv.instToken)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatalf("转交失败 %d", r.StatusCode)
	}
	if st := e.waitIdle(); len(st.Items) != 1 {
		t.Errorf("转交的照片没有加入")
	}

	e.srv.opts.Detector.SetHint(false, stamp.Hint{X0: 0, Y0: 0.8, X1: 0.5, Y1: 0.85})
	e.srv.settings.setHint(false, stamp.Hint{X0: 0, Y0: 0.8, X1: 0.5, Y1: 0.85})
	e.post("/api/hint/reset", map[string]any{})
	if _, ok := e.srv.opts.Detector.HintFor(false); ok || len(e.srv.settings.get().Hints) != 0 {
		t.Error("重置后不应还有水印位置")
	}
}
