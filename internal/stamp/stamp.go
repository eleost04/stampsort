// Package stamp 识别照片的拍摄时间：先 OCR 左下角水印，再读 EXIF，最后看文件名。
package stamp

import (
	"image"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eleost04/stampsort/internal/imgx"
	"github.com/eleost04/stampsort/internal/photo"
)

// Source 表示时间来自哪里。
type Source string

const (
	FromOCR  Source = "ocr"
	FromEXIF Source = "exif"
	FromName Source = "filename"
	Manual   Source = "manual" // 用户在桌面版里手动填写
	FromMark Source = "mark"   // stampsort 以前写进照片的时间（导出时写入）
	None     Source = "none"
)

// Result 是识别结果。HasClock 为 false 时只有日期（时分秒为 0）。
type Result struct {
	Time     time.Time
	HasClock bool
	Source   Source
	Raw      string // OCR 原文，便于排查
	Orig     string // Source 为 mark 时，标记里记录的最初来源（ocr/manual/...）
}

// Hint 是水印"时间"那一行在画面里的相对位置（0~1）。同一个水印相机拍的照片，
// 水印位置基本固定，只是背景在变：认出一张后记下位置，后面的直接去这个位置读。
type Hint struct {
	X0, Y0, X1, Y1 float64
}

func (h Hint) valid() bool { return h.X1 > h.X0 && h.Y1 > h.Y0 && h.Y1 <= 1.0001 && h.X1 <= 1.0001 }

func (h Hint) rect(w, hh int) image.Rectangle {
	return image.Rect(int(h.X0*float64(w)), int(h.Y0*float64(hh)), int(math.Ceil(h.X1*float64(w))), int(math.Ceil(h.Y1*float64(hh))))
}

func hintOf(r image.Rectangle, w, h int) Hint {
	return Hint{float64(r.Min.X) / float64(w), float64(r.Min.Y) / float64(h), float64(r.Max.X) / float64(w), float64(r.Max.Y) / float64(h)}
}

// Detector 串起三种来源。OCR 为 nil 时跳过水印识别（没有 onnxruntime 也能用）。可并发使用。
type Detector struct {
	OCR *OCR
	// OnHint 在学到新的水印位置时调用（桌面版用来保存，下次启动直接用）。
	OnHint func(portrait bool, h Hint)

	mu    sync.Mutex
	hints [2]*Hint // [横图, 竖图]
}

// SetHint 设置已知的水印位置（例如上次保存的）。
func (d *Detector) SetHint(portrait bool, h Hint) {
	if !h.valid() {
		return
	}
	d.mu.Lock()
	d.hints[b2i(portrait)] = &h
	d.mu.Unlock()
}

// ResetHints 忘掉学到的水印位置（学错了或者换了水印 App 时用）。
func (d *Detector) ResetHints() {
	d.mu.Lock()
	d.hints = [2]*Hint{}
	d.mu.Unlock()
}

// HintFor 返回当前记住的水印位置。
func (d *Detector) HintFor(portrait bool) (Hint, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h := d.hints[b2i(portrait)]; h != nil {
		return *h, true
	}
	return Hint{}, false
}

func (d *Detector) learn(portrait bool, h Hint) {
	d.mu.Lock()
	old := d.hints[b2i(portrait)]
	same := old != nil && math.Abs(old.Y0-h.Y0) < 0.005 && math.Abs(old.X1-h.X1) < 0.01
	d.hints[b2i(portrait)] = &h
	cb := d.OnHint
	d.mu.Unlock()
	if !same && cb != nil {
		cb(portrait, h)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Detect 识别一张已摆正的图。gray 是 imgx.Luma(img) 的结果，可传 nil。
func (d *Detector) Detect(img *image.RGBA, gray []uint8, meta photo.Meta, name string) Result {
	// stampsort 导出时写进去的时间（可能是核对、手动改过的）最可信，直接用，不再识别
	if m := meta.Mark; m != nil {
		return Result{Time: m.Time, HasClock: m.HasClock, Source: FromMark, Orig: m.Source, Raw: "照片里已标注的时间"}
	}
	var raw []string
	if d.OCR != nil {
		if gray == nil {
			gray = imgx.Luma(img)
		}
		r, texts := d.fromWatermark(img, gray)
		if r.Source == FromOCR {
			return r
		}
		raw = texts
	}
	if !meta.Taken.IsZero() {
		return Result{Time: meta.Taken, HasClock: true, Source: FromEXIF, Raw: strings.Join(raw, " | ")}
	}
	if t, clock, ok := FromFilename(name); ok {
		return Result{Time: t, HasClock: clock, Source: FromName, Raw: strings.Join(raw, " | ")}
	}
	return Result{Source: None, Raw: strings.Join(raw, " | ")}
}

// watermarkSearch 记录一张图的识别过程：读到的文字，以及分别找到的日期、时间。
type watermarkSearch struct {
	d        *Detector
	img      *image.RGBA
	tried    []image.Rectangle
	texts    []string
	date     *Parsed
	clock    *Parsed
	dateRect image.Rectangle
	budget   int // 最多 OCR 次数，防止没有水印的照片耗时太久
}

// try 识别一个区域；同时读到日期和时间时返回 true。
func (s *watermarkSearch) try(r image.Rectangle) bool {
	r = r.Intersect(s.img.Rect)
	if r.Dx() < 16 || r.Dy() < 8 || s.budget <= 0 {
		return false
	}
	for _, t := range s.tried { // 和已经试过的区域几乎一样就跳过
		if overlap(t, r) > 0.85 {
			return false
		}
	}
	s.tried = append(s.tried, r)
	s.budget--
	txt, err := s.d.OCR.Recognize(imgx.Crop(s.img, r))
	if err != nil || strings.TrimSpace(txt) == "" {
		return false
	}
	s.texts = append(s.texts, txt)
	p := ParseText(txt)
	if p.HasDate && (s.date == nil || (p.HasClock && !s.date.HasClock)) {
		s.date, s.dateRect = &p, r
	}
	if p.HasClock && !p.HasDate && s.clock == nil {
		s.clock = &p
	}
	return p.HasDate && p.HasClock
}

// overlap 是两个矩形的交并比（IoU）。注意不能用"交集占小矩形的比例"：
// 一个误判的大块区域会把里面所有扫描窗口都当成试过。
func overlap(a, b image.Rectangle) float64 {
	in := a.Intersect(b)
	if in.Empty() {
		return 0
	}
	ia := in.Dx() * in.Dy()
	return float64(ia) / float64(a.Dx()*a.Dy()+b.Dx()*b.Dy()-ia)
}

// fromWatermark 依次尝试：
//  1. 记住的水印位置（最快，背景再乱也不影响）；
//  2. 按亮度找出的白色文字行；
//  3. 在左下角按几种字号逐条扫描（兜底：白字压在浅色背景上、彩色字）。
//
// 日期和时间分在两行时也能拼起来。成功后记下日期行的位置，供后面的照片使用。
func (d *Detector) fromWatermark(img *image.RGBA, gray []uint8) (Result, []string) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	portrait := h > w
	s := &watermarkSearch{d: d, img: img, budget: 40}
	done := false

	if hint, ok := d.HintFor(portrait); ok {
		r := hint.rect(w, h)
		lh := r.Dy()
		// 原位置，再上下各挪 1/3 行、放大一点，容忍不同机型的细微差别
		for _, c := range []image.Rectangle{r, r.Add(image.Pt(0, -lh/3)), r.Add(image.Pt(0, lh/3)), r.Inset(-lh / 4)} {
			if done = s.try(c); done {
				break
			}
		}
	}
	if !done {
	lines:
		for _, thresh := range []uint8{235, 200} {
			for _, r := range TextLines(gray, w, h, thresh) {
				if done = s.try(r); done {
					break lines
				}
			}
		}
	}
	if !done && s.date == nil {
		for _, r := range scanWindows(gray, w, h) {
			if done = s.try(r); done || s.budget <= 0 {
				break
			}
		}
	}

	if s.date == nil {
		return Result{Source: None}, s.texts
	}
	if !s.date.HasClock && s.clock != nil {
		s.date.H, s.date.Mi, s.date.S, s.date.HasClock = s.clock.H, s.clock.Mi, s.clock.S, true
	}
	if s.date.HasClock {
		d.learn(portrait, hintOf(s.dateRect, w, h))
	}
	raw := s.texts[len(s.texts)-1]
	if !done {
		raw = strings.Join(s.texts, " | ")
	}
	return Result{Time: s.date.time(), HasClock: s.date.HasClock, Source: FromOCR, Raw: raw}, s.texts
}

// scanWindows 生成左下角的扫描窗口：几种行高、半行步长，按"像文字"的程度排序，
// 越可能是文字行的越先识别。
func scanWindows(gray []uint8, w, h int) []image.Rectangle {
	ref := min(w, h) // 横竖图都按短边估计字号
	x1 := int(float64(w) * 0.7)
	y0 := int(float64(h) * 0.55)
	// 每行的"文字感"：左侧区域里亮像素（>190）的个数
	rowScore := make([]int, h)
	for y := y0; y < h; y++ {
		n := 0
		for _, v := range gray[y*w : y*w+x1] {
			if v > 190 {
				n++
			}
		}
		rowScore[y] = n
	}
	type win struct {
		r     image.Rectangle
		score float64
	}
	var wins []win
	for _, frac := range []float64{0.042, 0.06, 0.085} {
		wh := max(16, int(frac*float64(ref)))
		step := max(4, wh/2)
		for y := h - wh; y >= y0; y -= step {
			sum := 0
			for yy := y; yy < y+wh; yy++ {
				sum += min(rowScore[yy], x1/4) // 封顶：整片亮背景不该得满分
			}
			wins = append(wins, win{image.Rect(0, y, x1, y+wh), float64(sum) / float64(wh)})
		}
	}
	sort.SliceStable(wins, func(i, j int) bool { return wins[i].score > wins[j].score })
	out := make([]image.Rectangle, len(wins))
	for i, w := range wins {
		out[i] = w.r
	}
	return out
}

func (p Parsed) time() time.Time {
	return time.Date(p.Y, time.Month(p.Mo), p.D, p.H, p.Mi, p.S, 0, time.Local)
}
