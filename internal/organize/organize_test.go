package organize

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eleost04/stampsort/internal/stamp"
)

func TestStem(t *testing.T) {
	tm := time.Date(2026, 8, 13, 6, 20, 13, 0, time.Local)
	r := stamp.Result{Time: tm, HasClock: true, Source: stamp.FromOCR}
	for f, want := range map[string]string{"": "2026-08-13_06-20", NameDateTime: "2026-08-13_06-20", NameTime: "06-20", NameOrig: "IMG_1"} {
		if got := Stem(r, "a.zip/IMG_1.JPG", f); got != want {
			t.Errorf("Stem(%q) = %s，期望 %s", f, got, want)
		}
	}
	r.HasClock = false
	if Stem(r, "x.jpg", NameTime) != "2026-08-13" {
		t.Error("只有日期时不能用时间命名")
	}
	if Stem(stamp.Result{Source: stamp.None}, "a.zip/工地/IMG_1.JPG", NameTime) != "IMG_1" {
		t.Error("未识别应保留原名")
	}
}

func TestNamerCollisions(t *testing.T) {
	root := t.TempDir()
	n := NewNamer(root)
	a, _ := n.Reserve("2026/08/13", "2026-08-13_06-20", ".jpg", nil)
	b, _ := n.Reserve("2026/08/13", "2026-08-13_06-20", ".jpg", nil)
	if a != "2026/08/13/2026-08-13_06-20.jpg" || b != "2026/08/13/2026-08-13_06-20_2.jpg" {
		t.Errorf("%s %s", a, b)
	}

	// 磁盘上已有同名同内容文件：复用；不同内容：加序号。
	src := filepath.Join(t.TempDir(), "src.jpg")
	os.WriteFile(src, []byte("photo"), 0o644)
	os.MkdirAll(filepath.Join(root, "d"), 0o755)
	os.WriteFile(filepath.Join(root, "d", "s.jpg"), []byte("photo"), 0o644)
	os.WriteFile(filepath.Join(root, "d", "t.jpg"), []byte("other"), 0o644)
	n2 := NewNamer(root)
	same := func(p string) bool { return SameFile(src, p) }
	if rel, ex := n2.Reserve("d", "s", ".jpg", same); rel != "d/s.jpg" || !ex {
		t.Errorf("应复用已有文件: %s %v", rel, ex)
	}
	if rel, ex := n2.Reserve("d", "t", ".jpg", same); rel != "d/t_2.jpg" || ex {
		t.Errorf("内容不同应加序号: %s %v", rel, ex)
	}
}

func TestPlaceMove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jpg")
	os.WriteFile(src, []byte("x"), 0o644)
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(src, old, old)
	dst := filepath.Join(dir, "out", "b.jpg")
	if err := Place(src, dst, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dst); !st.ModTime().Equal(old) {
		t.Error("没有保留修改时间")
	}
	if err := Place(src, filepath.Join(dir, "out", "c.jpg"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("移动后原文件还在")
	}
}
