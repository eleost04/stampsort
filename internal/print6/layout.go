// Package print6 把照片排成 6 寸（4x6 英寸、3:2）满版打印文件，移植自 photo6c.py。
//
// 方案 C（默认）：顶部裁掉少量画面，底部水印带保持原比例不变形，
// 剩下的高度差由画面主体承担轻微的竖向压缩。
// 纯裁切（crop）：完全不变形，只从顶部裁到 3:2，但不会裁到水印。
package print6

import (
	"image"
	"math"

	"github.com/eleost04/stampsort/internal/imgx"
	"github.com/eleost04/stampsort/internal/stamp"
)

const (
	DPI     = 300
	PaperW  = 6.0 // 英寸，长边
	PaperH  = 4.0 // 英寸，短边
	mmPerIn = 25.4
)

// Mode 是排版方式。
type Mode string

const (
	ModeC    Mode = "c"
	ModeCrop Mode = "crop"
)

// Options 是排版参数，零值字段用默认值。
type Options struct {
	Mode      Mode
	DPI       int
	CropRatio float64 // 多余高度里靠裁顶消化的比例，0=全靠压缩 1=全靠裁切
	CropTop   int     // >=0 时直接指定顶部裁切像素，覆盖 CropRatio；-1 表示不指定
}

// DefaultOptions 与旧脚本默认参数一致。
func DefaultOptions() Options {
	return Options{Mode: ModeC, DPI: DPI, CropRatio: 0.5, CropTop: -1}
}

// Info 是一张图的排版报告。
type Info struct {
	Portrait bool
	WM       *stamp.Box
	Out      image.Point
	CropTop  int
	CutX     int     // 原图比 3:2 还扁时左右裁掉的像素
	Squash   float64 // 主体竖向压缩比例（0.07 = 7%）
}

// WMMarginMM 是水印下沿到成品下边的距离（毫米），没有水印返回 -1。
func (in Info) WMMarginMM(srcW, srcH int, dpi int) float64 {
	if in.WM == nil {
		return -1
	}
	px := float64(srcH-1-in.WM.Bottom) * float64(in.Out.X) / float64(srcW)
	return px / float64(dpi) * mmPerIn
}

// pyRound 等价于 Python 3 的 round()（银行家舍入）。
func pyRound(x float64) int { return int(math.RoundToEven(x)) }

// Make 把一张已摆正的图做成 3:2 成品。gray 可传 nil。
func Make(im *image.RGBA, gray []uint8, o Options) (*image.RGBA, Info) {
	if o.DPI <= 0 {
		o.DPI = DPI
	}
	srcW, srcH := im.Rect.Dx(), im.Rect.Dy()
	portrait := srcH > srcW
	outW, outH := pyRound(PaperW*float64(o.DPI)), pyRound(PaperH*float64(o.DPI))
	if portrait {
		outW, outH = outH, outW
	}
	if gray == nil {
		gray = imgx.Luma(im)
	}
	wm := stamp.FindWatermark(gray, srcW, srcH)
	needH := float64(srcW) * float64(outH) / float64(outW) // 3:2 需要的原图高度
	info := Info{Portrait: portrait, WM: wm, Out: image.Pt(outW, outH)}

	if needH >= float64(srcH) { // 原图偏"扁"：横向要裁
		keepW := pyRound(float64(srcH) * float64(outW) / float64(outH))
		x0 := (srcW - keepW) / 2
		if wm != nil { // 别把左下角水印裁掉
			x0 = min(x0, max(0, wm.Left-8))
			x0 = max(0, min(x0, srcW-keepW))
		}
		img := imgx.Resize(imgx.Crop(im, image.Rect(x0, 0, x0+keepW, srcH)), outW, outH)
		info.CutX = srcW - keepW
		return img, info
	}

	excess := float64(srcH) - needH // 多出来的高度
	var ct int
	switch {
	case o.Mode == ModeCrop:
		ct = pyRound(excess) // 全部靠裁顶
	case o.CropTop >= 0:
		ct = o.CropTop
	default:
		ct = pyRound(excess * o.CropRatio)
	}
	ct = max(0, min(ct, int(excess)))
	if wm != nil { // 顶部裁切不能压到水印
		ct = min(ct, max(0, wm.Top-1))
	}
	cut := imgx.Crop(im, image.Rect(0, ct, srcW, srcH))
	info.CropTop = ct

	if o.Mode == ModeCrop {
		// 水印太靠上导致裁不够时，剩下的再从底部裁（水印在左下角，底边通常有余量）。
		if keep := pyRound(float64(srcW) * float64(outH) / float64(outW)); cut.Rect.Dy() > keep {
			cut = imgx.Crop(cut, image.Rect(0, 0, srcW, keep))
		}
		return imgx.Resize(cut, outW, outH), info
	}

	protect := int(float64(cut.Rect.Dy()) * 0.85)
	if wm != nil {
		protect = wm.Top - 30 - ct
	}
	img, ratio := Squash(cut, outW, outH, protect, 140)
	info.Squash = 1 - ratio
	return img, info
}
