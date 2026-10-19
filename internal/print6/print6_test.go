package print6

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleost04/stampsort/internal/imgx"
	"github.com/eleost04/stampsort/internal/photo"
)

// synthetic 生成一张 4:3 测试图：渐变背景 + 左下角两行白色"文字块"（模拟水印）。
func synthetic(w, h int) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := y*im.Stride + 4*x
			im.Pix[o], im.Pix[o+1], im.Pix[o+2], im.Pix[o+3] = uint8(x*200/w), uint8(y*200/h), 90, 255
		}
	}
	block := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if (x/6)%2 == 0 { // 竖条纹，像一串字
					o := y*im.Stride + 4*x
					im.Pix[o], im.Pix[o+1], im.Pix[o+2] = 255, 255, 255
				}
			}
		}
	}
	lh := h / 45
	block(w/40, h*85/100, w*3/10, h*85/100+lh)
	block(w/40, h*89/100, w*4/10, h*89/100+lh)
	return im
}

func TestMakeSizes(t *testing.T) {
	for _, tc := range []struct {
		w, h       int
		mode       Mode
		wantW, wnH int
	}{
		{2400, 1800, ModeC, 1800, 1200},
		{2400, 1800, ModeCrop, 1800, 1200},
		{1800, 2400, ModeC, 1200, 1800},
		{3000, 1500, ModeC, 1800, 1200}, // 比 3:2 还扁：左右裁
	} {
		o := DefaultOptions()
		o.Mode = tc.mode
		src := synthetic(tc.w, tc.h)
		out, info := Make(src, nil, o)
		if out.Rect.Dx() != tc.wantW || out.Rect.Dy() != tc.wnH {
			t.Errorf("%dx%d %s: 得到 %v", tc.w, tc.h, tc.mode, out.Rect.Size())
		}
		if tc.w == 2400 && tc.h == 1800 {
			if info.WM == nil {
				t.Fatalf("%s: 没找到模拟水印", tc.mode)
			}
			if tc.mode == ModeCrop && info.Squash != 0 {
				t.Errorf("crop 模式不应压缩，得到 %v", info.Squash)
			}
			if tc.mode == ModeC && (info.Squash <= 0 || info.Squash > 0.15) {
				t.Errorf("方案 C 压缩比例异常: %v", info.Squash)
			}
		}
	}
}

// 水印带必须横纵同比例：成品中水印块的高度 ≈ 原高度 × 横向比例。
func TestWatermarkNotDistorted(t *testing.T) {
	src := synthetic(2400, 1800)
	out, info := Make(src, nil, DefaultOptions())
	sx := float64(out.Rect.Dx()) / float64(src.Rect.Dx())
	gray := imgx.Luma(out)
	got := 0
	x := int(float64(info.WM.Left+3) * sx) // 白条纹所在列
	for y := 0; y < out.Rect.Dy(); y++ {
		if gray[y*out.Rect.Dx()+x] > 235 {
			got++
		}
	}
	want := float64(2*(1800/45)) * sx
	if math.Abs(float64(got)-want) > 3 {
		t.Errorf("水印高度 %d，期望约 %.1f（变形了）", got, want)
	}
}

func TestBleedAspect(t *testing.T) {
	src := synthetic(1800, 1200)
	b := AddBleed(src, 2.5, 25)
	if b.Rect.Dx()*2 != b.Rect.Dy()*3 {
		t.Errorf("出血后不是 3:2: %v", b.Rect.Size())
	}
	// 中间区域必须与原图一致
	dx, dy := (b.Rect.Dx()-1800)/2, (b.Rect.Dy()-1200)/2
	if !bytes.Equal(b.Pix[(dy+600)*b.Stride+4*dx:(dy+600)*b.Stride+4*(dx+1800)], src.Pix[600*src.Stride:601*src.Stride]) {
		t.Error("出血版中间区域被改动")
	}
}

func TestJPEGAndPDF(t *testing.T) {
	dir := t.TempDir()
	src := synthetic(1800, 1200)
	p := filepath.Join(dir, "a.jpg")
	if err := SaveJPEG(p, src, 95, 300); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	dec, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if ycc, ok := dec.(*image.YCbCr); !ok || ycc.SubsampleRatio != image.YCbCrSubsampleRatio444 {
		t.Errorf("不是 4:4:4: %T", dec)
	}
	if !bytes.Contains(data[:40], []byte("JFIF\x00\x01\x01\x01\x01\x2c\x01\x2c")) {
		t.Error("没有写入 300 DPI")
	}
	if psnr(imgx.ToRGBA(dec), src) < 38 {
		t.Error("JPEG 质量过低")
	}

	pdf := filepath.Join(dir, "a.pdf")
	if err := WritePDF(pdf, []PDFPage{{p, 1800, 1200}, {p, 1800, 1200}}, 300); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(pdf)
	if !bytes.Contains(b, []byte("/MediaBox [0 0 432 288]")) || !bytes.Contains(b, []byte("/Count 2")) {
		t.Errorf("PDF 页面尺寸或页数不对")
	}
}

func psnr(a, b *image.RGBA) float64 {
	if a.Rect.Size() != b.Rect.Size() {
		return 0
	}
	var se float64
	n := 0
	for y := 0; y < a.Rect.Dy(); y++ {
		for x := 0; x < a.Rect.Dx(); x++ {
			for c := 0; c < 3; c++ {
				d := float64(a.Pix[y*a.Stride+4*x+c]) - float64(b.Pix[y*b.Stride+4*x+c])
				se += d * d
				n++
			}
		}
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(se/float64(n)))
}

// 与旧 Python 脚本逐像素对比，由 scripts/compare_py.sh 调用。
func TestCompareWithPython(t *testing.T) {
	sample, ref := os.Getenv("STAMPSORT_SAMPLE"), os.Getenv("STAMPSORT_REF")
	if sample == "" || ref == "" {
		t.Skip("由 scripts/compare_py.sh 运行")
	}
	im, _, err := photo.Load(sample)
	if err != nil {
		t.Fatal(err)
	}
	// 用 Pillow 解码出的同一份像素，排除 JPEG 解码器差异，只比较排版算法本身。
	if f, err := os.Open(filepath.Join(ref, "src.png")); err == nil {
		dec, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		im = imgx.ToRGBA(dec)
	}
	out, info := Make(im, nil, DefaultOptions())
	t.Logf("go: 裁顶=%d 压缩=%.2f%% 水印=%+v", info.CropTop, info.Squash*100, *info.WM)
	for name, got := range map[string]*image.RGBA{"ref_c.png": out, "ref_bleed.png": AddBleed(out, 2.5, 25)} {
		f, err := os.Open(filepath.Join(ref, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		w := imgx.ToRGBA(want)
		diff := 0
		for i := range w.Pix {
			if d := int(w.Pix[i]) - int(got.Pix[i]); d > 1 || d < -1 {
				diff++
			}
		}
		p := psnr(got, w)
		t.Logf("%s: PSNR=%.2fdB，差值>1 的通道数=%d / %d", name, p, diff, len(w.Pix)*3/4)
		if p < 40 {
			t.Errorf("%s 与旧脚本差异过大", name)
		}
	}
}
