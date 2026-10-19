package stamp

import (
	"image"
	"math"
)

// Box 是原图上的像素包围盒（含 Bottom/Right 这一行列）。
type Box struct{ Top, Bottom, Left, Right int }

// segments 扫描 [y0,h) 中亮像素（>thresh）数 >= minPx 的连续行段，丢掉高度 < 3 的噪点段。
func segments(gray []uint8, w, h, y0 int, thresh uint8, minPx int) [][2]int {
	var segs [][2]int
	cur := -1
	flush := func(end int) {
		if cur >= 0 && end-cur >= 3 {
			segs = append(segs, [2]int{cur, end})
		}
		cur = -1
	}
	for y := y0; y < h; y++ {
		n := 0
		for _, v := range gray[y*w : (y+1)*w] {
			if v > thresh {
				n++
			}
		}
		if n >= minPx {
			if cur < 0 {
				cur = y
			}
		} else if cur >= 0 {
			flush(y - 1)
		}
	}
	flush(h - 1)
	return segs
}

// FindWatermark 移植自 photo6c.py detect_watermark：在画面下方 35% 找亮色文字块，
// 把间隔 <= gap 的行段合并，取最靠下的一块。参数与旧脚本一致，保证版式结果相同。
func FindWatermark(gray []uint8, w, h int) *Box {
	const thresh, minPx, gap = 235, 4, 90
	y0 := int(float64(h) * 0.65)
	segs := segments(gray, w, h, y0, thresh, minPx)
	if len(segs) == 0 {
		return nil
	}
	merged := [][2]int{segs[0]}
	for _, s := range segs[1:] {
		if s[0]-merged[len(merged)-1][1] <= gap {
			merged[len(merged)-1][1] = s[1]
		} else {
			merged = append(merged, s)
		}
	}
	top, bottom := merged[len(merged)-1][0], merged[len(merged)-1][1]
	left, right := -1, w-1
	for x := 0; x < w; x++ {
		n := 0
		for y := top; y <= bottom; y++ {
			if gray[y*w+x] > thresh {
				n++
			}
		}
		if n > 2 {
			if left < 0 {
				left = x
			}
			right = x
		}
	}
	if left < 0 {
		left, right = 0, w-1
	}
	return &Box{top, bottom, left, right}
}

// TextLines 找出画面左下区域的亮色文字行，从上到下返回（已加边距，可直接裁图送 OCR）。
//
// 与 FindWatermark 不同，这里按行高自适应合并：行间空隙大于行高的 30% 就算两行，
// 这样"时间"行和"备注"行会被拆开，每行单独识别。
func TextLines(gray []uint8, w, h int, thresh uint8) []image.Rectangle {
	y0 := int(float64(h) * 0.55)
	xMax := int(float64(w) * 0.75) // 水印在左下角，右侧的亮物体不参与
	segs := segments(gray, w, h, y0, thresh, 4)
	var lines [][2]int
	for _, s := range segs {
		if n := len(lines); n > 0 {
			last := lines[n-1]
			lh := max(last[1]-last[0], s[1]-s[0]) + 1
			if float64(s[0]-last[1]) <= 0.3*float64(lh) {
				lines[n-1][1] = s[1]
				continue
			}
		}
		lines = append(lines, s)
	}

	minH := max(8, h/200)
	maxH := max(minH+1, min(w, h)*9/100) // 水印一行字约占短边的 3%~6%，太高的是成片的亮背景
	var out []image.Rectangle
	for _, l := range lines {
		lh := l[1] - l[0] + 1
		if lh < minH || lh > maxH {
			continue
		}
		// 列投影聚类：空隙 <= 1.5 倍行高的列归为一簇，取最宽的一簇当作文字行。
		bestL, bestR, curL, lastX := -1, -1, -1, -1
		gapMax := int(1.5 * float64(lh))
		for x := 0; x < xMax; x++ {
			bright := false
			for y := l[0]; y <= l[1]; y++ {
				if gray[y*w+x] > thresh {
					bright = true
					break
				}
			}
			if !bright {
				continue
			}
			if curL < 0 || x-lastX > gapMax {
				curL = x
			}
			lastX = x
			if lastX-curL > bestR-bestL {
				bestL, bestR = curL, lastX
			}
		}
		if bestL < 0 || bestR-bestL < 2*lh {
			continue
		}
		pad := int(math.Ceil(0.25 * float64(lh)))
		out = append(out, image.Rect(bestL-pad, l[0]-pad, bestR+pad+1, l[1]+pad+1).Intersect(image.Rect(0, 0, w, h)))
		if len(out) == 8 {
			break
		}
	}
	return out
}
