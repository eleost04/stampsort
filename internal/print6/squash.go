package print6

import (
	"image"
	"math"

	"github.com/eleost04/stampsort/internal/imgx"
)

// Squash 竖向非均匀压缩（photo6c.py content_aware_squash）：protectTop 以下（水印带）
// 与横向同比例、不变形，需要压掉的高度全部由上方画面主体承担。
// 返回 (成品, 主体相对横向的比例)。
func Squash(im *image.RGBA, outW, outH, protectTop, ramp int) (*image.RGBA, float64) {
	srcW, srcH := im.Rect.Dx(), im.Rect.Dy()
	sx := float64(outW) / float64(srcW) // 横向比例，即"不变形"的基准
	protectTop = max(0, min(protectTop, srcH-1))

	wBody := make([]float64, srcH) // smoothstep: 0=保护区 1=主体
	for r := range wBody {
		t := math.Min(1, math.Max(0, float64(protectTop-r)/float64(max(ramp, 1))))
		wBody[r] = t * t * (3 - 2*t)
	}
	total := func(sBody float64) float64 {
		s := 0.0
		for _, w := range wBody {
			s += sx*(1-w) + sBody*w
		}
		return s
	}

	lo, hi := 1e-4, sx
	if total(hi) < float64(outH) { // 保护区自身已超高，退回整体缩放
		return imgx.Resize(im, outW, outH), float64(outH) / (float64(srcH) * sx)
	}
	for range 80 { // 二分求主体比例
		mid := (lo + hi) / 2
		if total(mid) > float64(outH) {
			hi = mid
		} else {
			lo = mid
		}
	}
	sBody := (lo + hi) / 2

	pos := make([]float64, srcH+1) // 每源行累积到的输出位置
	for r, w := range wBody {
		pos[r+1] = pos[r] + sx*(1-w) + sBody*w
	}
	k := float64(outH) / pos[srcH] // 消掉二分残差
	for i := range pos {
		pos[i] *= k
	}
	edges := make([]float64, outH+1) // np.interp(arange(outH+1), pos, arange(srcH+1))
	j := 0
	for i := range edges {
		x := float64(i)
		for j < srcH-1 && pos[j+1] < x {
			j++
		}
		switch {
		case x <= pos[0]:
			edges[i] = 0
		case x >= pos[srcH]:
			edges[i] = float64(srcH)
		default:
			edges[i] = float64(j) + (x-pos[j])/(pos[j+1]-pos[j])
		}
	}

	wide := imgx.ResizeH(im, outW) // 横向先用 Lanczos
	return resampleRows(wide, edges), sBody / sx
}

// resampleRows 按行边界做精确面积平均（photo6c.py _resample_rows），
// 每个输出行直接对覆盖到的源行加权，不做全图累加，内存只占一张输出图。
func resampleRows(src *image.RGBA, edges []float64) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	outH := len(edges) - 1
	dst := image.NewRGBA(image.Rect(0, 0, w, outH))
	clampY := func(y float64) float64 { return math.Min(math.Max(y, 0), float64(h)) }
	imgx.ParallelRows(outH, func(y0, y1 int) {
		acc := make([]float64, 3*w)
		for yy := y0; yy < y1; yy++ {
			a, b := clampY(edges[yy]), clampY(edges[yy+1])
			clear(acc)
			for r := min(int(a), h-1); r < h && float64(r) < b; r++ {
				ov := math.Min(b, float64(r+1)) - math.Max(a, float64(r))
				if ov <= 0 {
					continue
				}
				p := src.Pix[r*src.Stride:]
				for x := 0; x < w; x++ {
					acc[3*x] += ov * float64(p[4*x])
					acc[3*x+1] += ov * float64(p[4*x+1])
					acc[3*x+2] += ov * float64(p[4*x+2])
				}
			}
			span := math.Max(b-a, 1e-9)
			d := dst.Pix[yy*dst.Stride:]
			for x := 0; x < w; x++ {
				d[4*x] = to8(acc[3*x] / span)
				d[4*x+1] = to8(acc[3*x+1] / span)
				d[4*x+2] = to8(acc[3*x+2] / span)
				d[4*x+3] = 255
			}
		}
	})
	return dst
}

// to8 等价于 np.clip(v + 0.5, 0, 255).astype(uint8)。
func to8(v float64) uint8 {
	v += 0.5
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}
