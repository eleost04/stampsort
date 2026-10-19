package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
)

func jpegBytes(t *testing.T) []byte {
	im := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for i := range im.Pix {
		im.Pix[i] = uint8(i)
	}
	var b bytes.Buffer
	if err := print6.EncodeJPEG(&b, im, 80, 72); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestWebFlow(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir(), Password: "secret", Detector: &stamp.Detector{}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	// 首页不需要登录；API 需要
	if r, _ := c.Get(ts.URL + "/"); r.StatusCode != 200 {
		t.Fatalf("首页 %d", r.StatusCode)
	}
	if r, _ := c.Get(ts.URL + "/api/me"); r.StatusCode != 401 {
		t.Fatalf("未登录应 401，得到 %d", r.StatusCode)
	}
	if r, _ := c.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"wrong"}`)); r.StatusCode != 401 {
		t.Fatalf("错误密码应 401")
	}
	if r, _ := c.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"secret"}`)); r.StatusCode != 200 {
		t.Fatalf("登录失败 %d", r.StatusCode)
	}

	// 上传：一张散图 + 一个 zip
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("现场/IMG_20260814_090000.jpg")
	w.Write(jpegBytes(t))
	zw.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("mode", "all")
	mw.WriteField("pdf", "1")
	fw, _ := mw.CreateFormFile("files", "IMG_20260813_062013.jpg")
	fw.Write(jpegBytes(t))
	fw, _ = mw.CreateFormFile("files", "照片.zip")
	fw.Write(zbuf.Bytes())
	fw, _ = mw.CreateFormFile("files", "../../evil.sh")
	fw.Write([]byte("x"))
	mw.Close()
	r, err := c.Post(ts.URL+"/api/jobs", mw.FormDataContentType(), &body)
	if err != nil || r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("上传失败 %v %s", err, b)
	}
	var created struct{ ID string }
	json.NewDecoder(r.Body).Decode(&created)

	var st struct {
		State   string
		Done    int
		Total   int
		Results []resultJSON
		PDFs    []string
	}
	for i := 0; i < 100; i++ {
		r, _ := c.Get(ts.URL + "/api/jobs/" + created.ID)
		json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st.State == "done" || st.State == "error" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.State != "done" || st.Total != 2 || st.Done != 2 {
		t.Fatalf("任务状态 %+v", st)
	}
	if st.Results[0].Time != "2026-08-13 06:20" || st.Results[1].Name != "照片.zip/现场/IMG_20260814_090000.jpg" {
		t.Errorf("结果 %+v", st.Results)
	}

	r, _ = c.Get(ts.URL + "/api/jobs/" + created.ID + "/download")
	data, _ := io.ReadAll(r.Body)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"report.csv", "分类/2026/08/13/2026-08-13_06-20.jpg", "6寸/2026/08/14/2026-08-14_09-00_6寸.jpg", "6寸/2026/08/14/待打印_横版.pdf"} {
		if !names[want] {
			t.Errorf("下载的 zip 缺少 %s，实际 %v", want, names)
		}
	}

	// 删除后再查应 404
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/jobs/"+created.ID, nil)
	c.Do(req)
	if r, _ := c.Get(ts.URL + "/api/jobs/" + created.ID); r.StatusCode != 404 {
		t.Errorf("删除后应 404，得到 %d", r.StatusCode)
	}
}

func TestUploadLimit(t *testing.T) {
	srv, _ := New(Config{DataDir: t.TempDir(), MaxUpload: 1000, Detector: &stamp.Detector{}})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("files", "a.jpg")
	fw.Write(bytes.Repeat([]byte("x"), 5000))
	mw.Close()
	r, _ := http.Post(ts.URL+"/api/jobs", mw.FormDataContentType(), &body)
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("超限应 413，得到 %d", r.StatusCode)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{`..\..\a.jpg`: "a.jpg", "../": "upload", "a:b?.jpg": "a_b_.jpg", "照片.zip": "照片.zip"} {
		if got := safeName(in); got != want {
			t.Errorf("%q -> %q，期望 %q", in, got, want)
		}
	}
}
