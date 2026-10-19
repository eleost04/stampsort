package stamp

import (
	"image"
	"image/color"
	"image/draw"
	"os"
	"testing"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/eleost04/stampsort/internal/imgx"
	"github.com/eleost04/stampsort/internal/photo"
	"github.com/eleost04/stampsort/models"
)

// 集成测试：需要真实水印照片（STAMPSORT_SAMPLE）和 onnxruntime 动态库，缺一个就跳过。
// 真实照片不进仓库。
func TestOCRSample(t *testing.T) {
	path := os.Getenv("STAMPSORT_SAMPLE")
	if path == "" {
		t.Skip("未设置 STAMPSORT_SAMPLE")
	}
	lib, err := FindLib()
	if err != nil {
		t.Skip(err)
	}
	ocr, err := NewOCR(lib, models.Rec)
	if err != nil {
		t.Fatal(err)
	}
	defer ocr.Close()

	img, meta, err := photo.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	gray := imgx.Luma(img)
	for _, r := range TextLines(gray, img.Rect.Dx(), img.Rect.Dy(), 235) {
		txt, _ := ocr.Recognize(imgx.Crop(img, r))
		t.Logf("行 %v: %q", r, txt)
	}
	d := Detector{OCR: ocr}
	start := time.Now()
	res := d.Detect(img, gray, meta, path)
	t.Logf("结果 %s %v 来源=%s 原文=%q 耗时=%v", res.Time.Format("2006-01-02 15:04:05"), res.HasClock, res.Source, res.Raw, time.Since(start))
	if want := os.Getenv("STAMPSORT_SAMPLE_WANT"); want != "" {
		if got := res.Time.Format("2006-01-02 15:04"); got != want || res.Source != FromOCR {
			t.Fatalf("得到 %s (%s)，期望 %s (ocr)", got, res.Source, want)
		}
	}
}

// 合成水印：用点阵字体在左下角画白字，放大后送 OCR。CI 上没有真实照片也能验证整条识别链路。
func TestOCRSynthetic(t *testing.T) {
	lib, err := FindLib()
	if err != nil {
		t.Skip(err)
	}
	ocr, err := NewOCR(lib, models.Rec)
	if err != nil {
		t.Fatal(err)
	}
	defer ocr.Close()

	const w, h = 2400, 1800
	small := image.NewRGBA(image.Rect(0, 0, w/4, h/4))
	draw.Draw(small, small.Bounds(), image.NewUniform(color.RGBA{90, 110, 95, 255}), image.Point{}, draw.Src)
	d := font.Drawer{Dst: small, Src: image.White, Face: basicfont.Face7x13}
	d.Dot = fixed.P(10, h/4-40)
	d.DrawString("2026.08.13 06:20")
	d.Dot = fixed.P(10, h/4-20)
	d.DrawString("Site A-3")
	img := imgx.Resize(small, w, h)

	res := (&Detector{OCR: ocr}).Detect(img, nil, photo.Meta{}, "x.jpg")
	t.Logf("原文 %q", res.Raw)
	if res.Source != FromOCR || res.Time.Format("2006-01-02 15:04") != "2026-08-13 06:20" {
		t.Fatalf("得到 %v (%s)", res.Time, res.Source)
	}
}

// 记住水印位置：先识别一张干净背景的，学到位置后，浅色杂乱背景的同款水印直接按位置读出来。
func TestOCRHintLearning(t *testing.T) {
	lib, err := FindLib()
	if err != nil {
		t.Skip(err)
	}
	ocr, err := NewOCR(lib, models.Rec)
	if err != nil {
		t.Fatal(err)
	}
	defer ocr.Close()

	const w, h = 2400, 1800
	mk := func(noisy bool, text string) *image.RGBA {
		small := image.NewRGBA(image.Rect(0, 0, w/4, h/4))
		for i := 0; i < len(small.Pix); i += 4 {
			v := uint8(60)
			if noisy { // 浅色碎石路面：亮度 150~250 的随机斑点
				v = uint8(150 + (i*7919)%100)
			}
			small.Pix[i], small.Pix[i+1], small.Pix[i+2], small.Pix[i+3] = v, v, v-10, 255
		}
		// 水印 App 一般给白字加深色描边，这里画一圈深色再画白字
		for _, off := range []image.Point{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			d := font.Drawer{Dst: small, Src: image.NewUniform(color.RGBA{30, 30, 30, 255}), Face: basicfont.Face7x13}
			d.Dot = fixed.P(10+off.X, h/4-40+off.Y)
			d.DrawString(text)
		}
		d := font.Drawer{Dst: small, Src: image.White, Face: basicfont.Face7x13}
		d.Dot = fixed.P(10, h/4-40)
		d.DrawString(text)
		return imgx.Resize(small, w, h)
	}

	var learned []Hint
	det := &Detector{OCR: ocr, OnHint: func(_ bool, h Hint) { learned = append(learned, h) }}
	if r := det.Detect(mk(false, "2026.08.13 06:20"), nil, photo.Meta{}, "a.jpg"); r.Source != FromOCR {
		t.Fatalf("干净背景没认出: %q", r.Raw)
	}
	if len(learned) != 1 {
		t.Fatalf("应学到水印位置")
	}
	start := time.Now()
	r := det.Detect(mk(true, "2026.10.19 08:21"), nil, photo.Meta{}, "b.jpg")
	t.Logf("杂乱背景: %q %s，耗时 %v", r.Raw, r.Source, time.Since(start))
	if r.Source != FromOCR || r.Time.Format("2006-01-02 15:04") != "2026-10-19 08:21" {
		t.Fatalf("杂乱背景按位置没读出来: %+v", r)
	}
}
