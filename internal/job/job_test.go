package job

import (
	"context"
	"encoding/csv"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleost04/stampsort/internal/input"
	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
)

func writePhoto(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range im.Pix {
		im.Pix[i] = uint8(i * 7)
	}
	p := filepath.Join(dir, name)
	if err := print6.SaveJPEG(p, im, 80, 72); err != nil {
		t.Fatal(err)
	}
	return p
}

// 没有 OCR 时按文件名识别；同一分钟的 3 张按输入顺序编号 _2、_3（并发下也稳定）。
func TestRunSortAndPrint(t *testing.T) {
	src := t.TempDir()
	var items []input.Item
	for _, n := range []string{"IMG_20260813_062001.jpg", "IMG_20260813_062002.jpg", "IMG_20260813_062003.jpg", "IMG_20260814_090000.jpg", "DSC0001.jpg"} {
		items = append(items, input.Item{Path: writePhoto(t, src, n, 400, 300), Name: n})
	}
	items = append(items, input.Item{Path: writePhoto(t, src, "IMG_20260814_100000.jpg", 300, 400), Name: "IMG_20260814_100000.jpg"})
	items = append(items, input.Item{Path: filepath.Join(src, "missing.jpg"), Name: "missing.jpg"})

	out := t.TempDir()
	o := Options{Sort: true, Print: true, OutDir: out, Layout: print6.DefaultOptions(), PDF: true, Workers: 4}
	o.Layout.DPI = 50 // 小图加快测试
	sum, err := Run(context.Background(), items, nil, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"分类/2026/08/13/2026-08-13_06-20.jpg",
		"分类/2026/08/13/2026-08-13_06-20_2.jpg",
		"分类/2026/08/13/2026-08-13_06-20_3.jpg",
		"分类/2026/08/14/2026-08-14_09-00.jpg",
		"分类/未识别/DSC0001.jpg",
		"6寸/2026/08/13/2026-08-13_06-20_2_6寸.jpg",
		"6寸/2026/08/13/待打印_横版.pdf",
		"6寸/2026/08/14/待打印_横版.pdf",
		"6寸/2026/08/14/待打印_竖版.pdf",
		"6寸/未识别/DSC0001_6寸.jpg",
		"report.csv",
	}
	for _, w := range want {
		if _, err := os.Stat(filepath.Join(out, w)); err != nil {
			t.Errorf("缺少 %s", w)
		}
	}
	if sum.Results[6].Err == "" {
		t.Error("不存在的文件应报错")
	}
	if len(sum.PDFs) != 4 {
		t.Errorf("PDF: %v", sum.PDFs)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 3 {
		t.Errorf("临时目录没清理: %v", entries)
	}

	f, _ := os.Open(filepath.Join(out, "report.csv"))
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) != 8 || !strings.HasPrefix(rows[0][0], "\ufeff序号") {
		t.Errorf("报告不对: %v %v", err, rows)
	}

	// 再跑一次：内容相同的不会再复制出 _4
	sum2, err := Run(context.Background(), items[:1], nil, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := sum2.Results[0]; !r.Existed || r.Sorted != "分类/2026/08/13/2026-08-13_06-20.jpg" {
		t.Errorf("重复运行应复用已有文件: %+v", r)
	}
}

func TestRunPrintOnlyNoJPG(t *testing.T) {
	src := t.TempDir()
	items := []input.Item{{Path: writePhoto(t, src, "a.jpg", 400, 300), Name: "a.jpg"}}
	out := t.TempDir()
	o := Options{Print: true, OutDir: out, Layout: print6.DefaultOptions(), PDF: true, NoJPG: true}
	o.Layout.DPI = 50
	if _, err := Run(context.Background(), items, nil, o, nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "report.csv,待打印_横版.pdf" {
		t.Errorf("输出: %v", names)
	}
}

func TestRunCancel(t *testing.T) {
	src := t.TempDir()
	var items []input.Item
	for i := 0; i < 20; i++ {
		n := filepathName(i)
		items = append(items, input.Item{Path: writePhoto(t, src, n, 200, 150), Name: n})
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{Print: true, OutDir: t.TempDir(), Layout: print6.DefaultOptions(), Workers: 2}
	o.Layout.DPI = 20
	_, err := Run(ctx, items, nil, o, func(done, total int, r Result) {
		if done == 3 {
			cancel()
		}
	})
	if err == nil {
		t.Error("取消后应返回错误")
	}
}

func filepathName(i int) string { return "p" + string(rune('a'+i)) + ".jpg" }

func TestRerunOverwritesPrint(t *testing.T) {
	src := t.TempDir()
	items := []input.Item{{Path: writePhoto(t, src, "IMG_20260813_062001.jpg", 400, 300), Name: "IMG_20260813_062001.jpg"}}
	out := t.TempDir()
	o := Options{Sort: true, Print: true, OutDir: out, Layout: print6.DefaultOptions()}
	o.Layout.DPI = 50
	for range 2 {
		if _, err := Run(context.Background(), items, nil, o, nil); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(out, "6寸", "2026", "08", "13"))
	if len(entries) != 1 {
		t.Errorf("重复运行产生了多余的成品: %v", entries)
	}
}

// Preset 有值时直接使用，不再按文件名识别。
func TestRunPreset(t *testing.T) {
	src := t.TempDir()
	items := []input.Item{
		{Path: writePhoto(t, src, "IMG_20260813_062001.jpg", 400, 300), Name: "IMG_20260813_062001.jpg"},
		{Path: writePhoto(t, src, "DSC0001.jpg", 400, 300), Name: "DSC0001.jpg"},
	}
	out := t.TempDir()
	manual := stamp.Result{Time: time.Date(2025, 1, 2, 3, 4, 0, 0, time.Local), HasClock: true, Source: stamp.Manual}
	o := Options{Sort: true, OutDir: out, Preset: []*stamp.Result{nil, &manual}}
	sum, err := Run(context.Background(), items, nil, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Results[0].Sorted != "2026/08/13/2026-08-13_06-20.jpg" || sum.Results[1].Sorted != "2025/01/02/2025-01-02_03-04.jpg" {
		t.Errorf("%s %s", sum.Results[0].Sorted, sum.Results[1].Sorted)
	}
}

// 用户场景：导出到"资料/10/"并选"日"层级 → 资料/10/分类/19/…、资料/10/6寸/19/…；
// 第二次导出到同一个文件夹，当天的 PDF 要包含两次的全部 6 寸。
func TestDayFolderAndPDFMerge(t *testing.T) {
	out := filepath.Join(t.TempDir(), "资料", "10")
	mk := func(names ...string) []input.Item {
		src := t.TempDir()
		var items []input.Item
		for i, n := range names {
			p := writePhoto(t, src, n, 400+i, 300)
			items = append(items, input.Item{Path: p, Name: n})
		}
		return items
	}
	o := Options{Sort: true, Print: true, PDF: true, OutDir: out, Layout: print6.DefaultOptions(), DirFormat: "d", NameFormat: "time"}
	o.Layout.DPI = 50
	if _, err := Run(context.Background(), mk("IMG_20261019_082100.jpg", "IMG_20261020_090000.jpg"), nil, o, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), mk("IMG_20261019_142400.jpg"), nil, o, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"分类/19/08-21.jpg", "分类/19/14-24.jpg", "分类/20/09-00.jpg",
		"6寸/19/08-21_6寸.jpg", "6寸/19/14-24_6寸.jpg", "6寸/19/待打印_横版.pdf", "6寸/20/待打印_横版.pdf",
	} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(want))); err != nil {
			t.Errorf("缺少 %s", want)
		}
	}
	pdf, _ := os.ReadFile(filepath.Join(out, "6寸", "19", "待打印_横版.pdf"))
	if !strings.Contains(string(pdf), "/Count 2") {
		t.Error("第二次导出后，19 日的 PDF 应包含两页")
	}
}

// 只做 6 寸 + PrintByDate：按日期分目录、按时间命名。
func TestPrintByDate(t *testing.T) {
	src := t.TempDir()
	items := []input.Item{{Path: writePhoto(t, src, "IMG_20261019_082100.jpg", 400, 300), Name: "IMG_20261019_082100.jpg"}}
	out := t.TempDir()
	o := Options{Print: true, PrintByDate: true, OutDir: out, Layout: print6.DefaultOptions(), DirFormat: "md"}
	o.Layout.DPI = 50
	sum, err := Run(context.Background(), items, nil, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Results[0].Print != "10/19/2026-10-19_08-21_6寸.jpg" {
		t.Errorf("得到 %s", sum.Results[0].Print)
	}
}
