package print6

import (
	"image"
	"math"

	"github.com/eleost04/stampsort/internal/imgx"
)

// AddBleed 生成出血版（photo6c.py add_bleed）：四周镜像向外扩 pct%，扩出部分做高斯模糊，
// 供无边距打印机放大越界时消耗。扩边量按原图宽高比取整，出血后仍是严格 3:2。
func AddBleed(im *image.RGBA, pct float64, radius float64) *image.RGBA {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	g := gcd(w, h)
	p, q := w/g, h/g
	dy := max(q, pyRound(float64(h)*pct/200/float64(q))*q)
	dx := dy * p / q
	cw, ch := w+2*dx, h+2*dy

	// 镜像扩边：坐标折回原图范围。
	mirror := func(v, n int) int {
		if v < 0 {
			return -v - 1
		}
		if v >= n {
			return 2*n - v - 1
		}
		return v
	}
	canvas := image.NewRGBA(image.Rect(0, 0, cw, ch))
	imgx.ParallelRows(ch, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sy := mirror(y-dy, h)
			for x := 0; x < cw; x++ {
				sx := mirror(x-dx, w)
				copy(canvas.Pix[y*canvas.Stride+4*x:y*canvas.Stride+4*x+4], im.Pix[sy*im.Stride+4*sx:])
			}
		}
	})
	blurred := gaussianBlur(canvas, radius)
	// 中间原图区域保持清晰。
	for y := 0; y < h; y++ {
		copy(blurred.Pix[(y+dy)*blurred.Stride+4*dx:(y+dy)*blurred.Stride+4*(dx+w)], im.Pix[y*im.Stride:y*im.Stride+4*w])
	}
	return blurred
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// gaussianBlur 与 Pillow GaussianBlur 一样用 3 趟"扩展盒式模糊"近似高斯。
func gaussianBlur(im *image.RGBA, radius float64) *image.RGBA {
	r := boxRadius(radius, 3)
	out := im
	for range 3 {
		out = boxBlur(out, r, true)
		out = boxBlur(out, r, false)
	}
	return out
}

// boxRadius 复刻 Pillow _gaussian_blur_radius：返回带小数的盒半径。
func boxRadius(radius float64, passes int) float64 {
	sigma2 := radius * radius / float64(passes)
	L := math.Sqrt(12*sigma2 + 1)
	l := math.Floor((L - 1) / 2)
	a := (2*l + 1) * (l*(l+1) - 3*sigma2)
	a /= 6 * (sigma2 - (l+1)*(l+1))
	return l + a
}

// boxBlur 一维盒式模糊，半径可带小数（两端像素按小数部分加权），边缘取最近像素。
func boxBlur(im *image.RGBA, radius float64, horizontal bool) *image.RGBA {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	dst := image.NewRGBA(im.Rect)
	ri := int(radius)
	frac := radius - float64(ri)
	norm := 1 / (2*radius + 1)
	n, lines := w, h
	if !horizontal {
		n, lines = h, w
	}
	imgx.ParallelRows(lines, func(l0, l1 int) {
		line := make([]float64, 3*n)
		for l := l0; l < l1; l++ {
			at := func(i int) int { // 像素 i 在 Pix 中的偏移
				i = max(0, min(i, n-1))
				if horizontal {
					return l*im.Stride + 4*i
				}
				return i*im.Stride + 4*l
			}
			for c := range 3 {
				sum := 0.0
				for k := -ri; k <= ri; k++ {
					sum += float64(im.Pix[at(k)+c])
				}
				for i := 0; i < n; i++ {
					edge := float64(im.Pix[at(i-ri-1)+c]) + float64(im.Pix[at(i+ri+1)+c])
					line[3*i+c] = (sum + frac*edge) * norm
					sum += float64(im.Pix[at(i+ri+1)+c]) - float64(im.Pix[at(i-ri)+c])
				}
			}
			for i := 0; i < n; i++ {
				var o int
				if horizontal {
					o = l*dst.Stride + 4*i
				} else {
					o = i*dst.Stride + 4*l
				}
				dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = to8(line[3*i]), to8(line[3*i+1]), to8(line[3*i+2]), 255
			}
		}
	})
	return dst
}
