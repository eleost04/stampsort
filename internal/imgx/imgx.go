// Package imgx 是 stampsort 用到的图像基础操作，全部基于 *image.RGBA（Min 恒为 0,0）。
//
// 缩放严格复刻 Pillow 的 ImagingResample（Lanczos、定点 22 位系数、每一趟取整到 8 位），
// 这样移植自 photo6c.py 的版式算法输出与旧脚本几乎逐像素一致。
package imgx

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"runtime"
	"sync"
)

// ToRGBA 把任意解码结果转成 RGBA；YCbCr（JPEG 常见）走并行快速路径。
func ToRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	switch s := src.(type) {
	case *image.RGBA:
		if b.Min == (image.Point{}) {
			return s
		}
	case *image.YCbCr:
		parallelRows(b.Dy(), func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := dst.Pix[y*dst.Stride:]
				for x := 0; x < b.Dx(); x++ {
					yi := s.YOffset(b.Min.X+x, b.Min.Y+y)
					ci := s.COffset(b.Min.X+x, b.Min.Y+y)
					r, g, bb := color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
					row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = r, g, bb, 255
				}
			}
		})
		return dst
	}
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// Crop 复制出 r 区域（会被裁到图像范围内）。
func Crop(src *image.RGBA, r image.Rectangle) *image.RGBA {
	r = r.Intersect(src.Bounds())
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+4*r.Dx()], src.Pix[(r.Min.Y+y)*src.Stride+4*r.Min.X:])
	}
	return dst
}

// Luma 按 Pillow convert("L") 的定点公式转灰度，返回按行排列的 w*h 字节。
func Luma(src *image.RGBA) []uint8 {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	out := make([]uint8, w*h)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			p := src.Pix[y*src.Stride:]
			o := out[y*w:]
			for x := 0; x < w; x++ {
				o[x] = uint8((uint32(p[4*x])*19595 + uint32(p[4*x+1])*38470 + uint32(p[4*x+2])*7471 + 0x8000) >> 16)
			}
		}
	})
	return out
}

// Orient 按 EXIF Orientation(1-8) 把图摆正，等价于 PIL.ImageOps.exif_transpose。
func Orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				var dx, dy int
				switch o {
				case 2: // 水平翻转
					dx, dy = w-1-x, y
				case 3: // 旋转 180
					dx, dy = w-1-x, h-1-y
				case 4: // 垂直翻转
					dx, dy = x, h-1-y
				case 5: // 转置
					dx, dy = y, x
				case 6: // 顺时针 90
					dx, dy = h-1-y, x
				case 7: // 反转置
					dx, dy = h-1-y, w-1-x
				case 8: // 逆时针 90
					dx, dy = y, w-1-x
				}
				copy(dst.Pix[dy*dst.Stride+4*dx:dy*dst.Stride+4*dx+4], src.Pix[y*src.Stride+4*x:])
			}
		}
	})
	return dst
}

// ------------------------------------------------------------------ Pillow Lanczos

const precisionBits = 32 - 8 - 2

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	x *= math.Pi
	return math.Sin(x) / x
}

func lanczos(x float64) float64 {
	if -3 <= x && x < 3 {
		return sinc(x) * sinc(x/3)
	}
	return 0
}

// kernel 是一个输出像素的定点权重：从源坐标 min 开始的 len(k) 个像素。
type kernel struct {
	min int
	k   []int32
}

// coeffs 复刻 Pillow precompute_coeffs + normalize_coeffs_8bpc。
func coeffs(inSize, outSize int) []kernel {
	scale := float64(inSize) / float64(outSize)
	filterscale := math.Max(scale, 1)
	support := 3 * filterscale
	ss := 1 / filterscale
	ks := make([]kernel, outSize)
	buf := make([]float64, int(math.Ceil(support))*2+1)
	for xx := range ks {
		center := (float64(xx) + 0.5) * scale
		xmin := int(center - support + 0.5) // C 的 (int) 向零截断
		if xmin < 0 {
			xmin = 0
		}
		xmax := int(center + support + 0.5)
		if xmax > inSize {
			xmax = inSize
		}
		xmax -= xmin
		ww := 0.0
		for x := 0; x < xmax; x++ {
			w := lanczos((float64(x+xmin) - center + 0.5) * ss)
			buf[x] = w
			ww += w
		}
		k := make([]int32, xmax)
		for x := 0; x < xmax; x++ {
			v := buf[x]
			if ww != 0 {
				v /= ww
			}
			if v < 0 {
				k[x] = int32(math.Trunc(-0.5 + v*(1<<precisionBits)))
			} else {
				k[x] = int32(math.Trunc(0.5 + v*(1<<precisionBits)))
			}
		}
		ks[xx] = kernel{xmin, k}
	}
	return ks
}

func clip8(v int64) uint8 {
	v >>= precisionBits
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// ResizeH 只做横向 Lanczos 缩放到 outW（Pillow 只改宽度时就是这一趟）。
func ResizeH(src *image.RGBA, outW int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if outW == w {
		return src
	}
	ks := coeffs(w, outW)
	dst := image.NewRGBA(image.Rect(0, 0, outW, h))
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sp := src.Pix[y*src.Stride:]
			dp := dst.Pix[y*dst.Stride:]
			for xx, kk := range ks {
				var r, g, b int64 = 1 << (precisionBits - 1), 1 << (precisionBits - 1), 1 << (precisionBits - 1)
				for i, c := range kk.k {
					p := sp[4*(kk.min+i):]
					r += int64(p[0]) * int64(c)
					g += int64(p[1]) * int64(c)
					b += int64(p[2]) * int64(c)
				}
				dp[4*xx], dp[4*xx+1], dp[4*xx+2], dp[4*xx+3] = clip8(r), clip8(g), clip8(b), 255
			}
		}
	})
	return dst
}

// ResizeV 只做竖向 Lanczos 缩放到 outH。
func ResizeV(src *image.RGBA, outH int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if outH == h {
		return src
	}
	ks := coeffs(h, outH)
	dst := image.NewRGBA(image.Rect(0, 0, w, outH))
	parallelRows(outH, func(y0, y1 int) {
		for yy := y0; yy < y1; yy++ {
			kk := ks[yy]
			dp := dst.Pix[yy*dst.Stride:]
			for x := 0; x < w; x++ {
				var r, g, b int64 = 1 << (precisionBits - 1), 1 << (precisionBits - 1), 1 << (precisionBits - 1)
				for i, c := range kk.k {
					p := src.Pix[(kk.min+i)*src.Stride+4*x:]
					r += int64(p[0]) * int64(c)
					g += int64(p[1]) * int64(c)
					b += int64(p[2]) * int64(c)
				}
				dp[4*x], dp[4*x+1], dp[4*x+2], dp[4*x+3] = clip8(r), clip8(g), clip8(b), 255
			}
		}
	})
	return dst
}

// Resize 等价于 Pillow im.resize((w, h), Image.LANCZOS)：先横向再竖向。
//
// Pillow 在两趟都需要时，横向那趟只算竖向核实际会用到的行；全图都会用到时结果相同。
func Resize(src *image.RGBA, w, h int) *image.RGBA {
	return ResizeV(ResizeH(src, w), h)
}

// ------------------------------------------------------------------ 并行

// parallelRows 把 [0,n) 按 CPU 数分块并行执行。
func parallelRows(n int, fn func(y0, y1 int)) {
	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}
	if workers <= 1 || n < 64 {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	step := (n + workers - 1) / workers
	for y0 := 0; y0 < n; y0 += step {
		y1 := min(y0+step, n)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(y0, y1)
		}()
	}
	wg.Wait()
}

// ParallelRows 供其它包复用的分块并行。
func ParallelRows(n int, fn func(y0, y1 int)) { parallelRows(n, fn) }
