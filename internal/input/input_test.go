package input

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func writeZip(t *testing.T, path string, files map[string]string, gbk bool) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Store}
		if gbk {
			h.Name, _ = simplifiedchinese.GBK.NewEncoder().String(name)
			h.NonUTF8 = true
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
}

func TestZipGBKAndSlip(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "照片.zip")
	writeZip(t, zp, map[string]string{
		"工地/现场照片1.jpg":       "a",
		"../../evil.jpg":     "x",
		"/abs.jpg":           "x",
		"__MACOSX/._a.jpg":   "x",
		"说明.txt":             "x",
		"工地/现场照片1_6寸.jpg":    "x",
		`C:\windows\bad.jpg`: "x",
	}, true)
	tmp := t.TempDir()
	items, warns, err := Collect([]string{zp}, tmp, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "照片.zip/工地/现场照片1.jpg" {
		t.Fatalf("items = %+v", items)
	}
	if !strings.HasPrefix(items[0].Path, tmp) {
		t.Errorf("解压到了临时目录之外: %s", items[0].Path)
	}
	if len(warns) != 3 {
		t.Errorf("应拦下 3 个不安全路径, warns=%v", warns)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "evil.jpg")); err == nil {
		t.Error("zip-slip 写出了临时目录")
	}
}

func TestZipLimits(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "big.zip")
	writeZip(t, zp, map[string]string{"a.jpg": strings.Repeat("x", 1000), "b.jpg": "x"}, false)
	if _, _, err := Collect([]string{zp}, t.TempDir(), Limits{MaxFiles: 10, MaxBytes: 500}); err == nil {
		t.Error("超过大小限制应报错")
	}
	if _, _, err := Collect([]string{zp}, t.TempDir(), Limits{MaxFiles: 1, MaxBytes: 1 << 20}); err == nil {
		t.Error("超过文件数限制应报错")
	}
}

func TestSortNatural(t *testing.T) {
	items := []Item{{Name: "IMG_10.jpg"}, {Name: "IMG_2.jpg"}, {Name: "img_1.jpg"}}
	SortNatural(items)
	if items[0].Name != "img_1.jpg" || items[1].Name != "IMG_2.jpg" {
		t.Errorf("%v", items)
	}
}
