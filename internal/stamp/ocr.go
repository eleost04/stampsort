package stamp

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/eleost04/stampsort/internal/imgx"
)

// OCR 是 PP-OCRv4 识别模型的封装：输入一行文字的裁图，输出文字。并发安全。
type OCR struct {
	sess    *ort.DynamicAdvancedSession
	in, out string
	chars   []string // 0=blank，末尾是空格，与 RapidOCR CTCLabelDecode 一致
}

const recH = 48

// LibName 是当前平台 onnxruntime 动态库的文件名。
func LibName() string {
	switch runtime.GOOS {
	case "windows":
		return "onnxruntime.dll"
	case "darwin":
		return "libonnxruntime.dylib"
	}
	return "libonnxruntime.so"
}

// FindLib 依次在 $STAMPSORT_ORT_LIB、程序所在目录、程序目录/lib、当前目录/lib 中找动态库。
func FindLib() (string, error) {
	if p := os.Getenv("STAMPSORT_ORT_LIB"); p != "" {
		return p, nil
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		dirs = append(dirs, filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "lib"))
	}
	dirs = append(dirs, "lib", ".")
	for _, d := range dirs {
		p := filepath.Join(d, LibName())
		if _, err := os.Stat(p); err == nil {
			return filepath.Abs(p)
		}
	}
	return "", fmt.Errorf("找不到 %s（放在程序同目录，或设置 STAMPSORT_ORT_LIB）", LibName())
}

// NewOCR 加载动态库并用内嵌模型创建会话。每次推理只用 1 个线程，并发靠外层多个 goroutine。
func NewOCR(libPath string, model []byte) (*OCR, error) {
	if len(model) == 0 {
		return nil, errors.New("程序里没有内嵌 OCR 模型（构建前先运行 make model）")
	}
	if !ort.IsInitialized() {
		ort.SetSharedLibraryPath(libPath)
		if err := ort.InitializeEnvironment(ort.WithLogLevelError()); err != nil {
			return nil, fmt.Errorf("加载 onnxruntime 失败: %w", err)
		}
	}
	meta, err := ort.GetModelMetadataWithONNXData(model)
	if err != nil {
		return nil, err
	}
	dict, ok, err := meta.LookupCustomMetadataMap("character")
	meta.Destroy()
	if err != nil || !ok {
		return nil, fmt.Errorf("模型里没有字典: %v", err)
	}
	ins, outs, err := ort.GetInputOutputInfoWithONNXData(model)
	if err != nil {
		return nil, err
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	_ = opts.SetIntraOpNumThreads(1)
	_ = opts.SetInterOpNumThreads(1)
	sess, err := ort.NewDynamicAdvancedSessionWithONNXData(model, []string{ins[0].Name}, []string{outs[0].Name}, opts)
	if err != nil {
		return nil, err
	}
	chars := append([]string{""}, strings.Split(strings.ReplaceAll(dict, "\r\n", "\n"), "\n")...)
	chars = append(chars, " ")
	return &OCR{sess: sess, in: ins[0].Name, out: outs[0].Name, chars: chars}, nil
}

// Close 释放会话。
func (o *OCR) Close() {
	if o != nil && o.sess != nil {
		o.sess.Destroy()
	}
}

// Recognize 识别一行文字。预处理与 RapidOCR 相同：BGR、高 48、(x/255-0.5)/0.5、右侧补 0 到至少 320 宽。
func (o *OCR) Recognize(line *image.RGBA) (string, error) {
	w, h := line.Rect.Dx(), line.Rect.Dy()
	if w == 0 || h == 0 {
		return "", nil
	}
	rw := int(math.Ceil(float64(recH) * float64(w) / float64(h)))
	rw = max(1, min(rw, 2000))
	img := imgx.Resize(line, rw, recH)
	tw := max(rw, 320)
	data := make([]float32, 3*recH*tw)
	plane := recH * tw
	for y := 0; y < recH; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < rw; x++ {
			i := y*tw + x
			data[i] = float32(row[4*x+2])/127.5 - 1 // B
			data[plane+i] = float32(row[4*x+1])/127.5 - 1
			data[2*plane+i] = float32(row[4*x])/127.5 - 1 // R
		}
	}
	in, err := ort.NewTensor(ort.NewShape(1, 3, recH, int64(tw)), data)
	if err != nil {
		return "", err
	}
	defer in.Destroy()
	outs := []ort.Value{nil}
	if err := o.sess.Run([]ort.Value{in}, outs); err != nil {
		return "", err
	}
	defer outs[0].Destroy()
	t, ok := outs[0].(*ort.Tensor[float32])
	if !ok {
		return "", errors.New("OCR 输出类型不对")
	}
	shape := t.GetShape() // [1, T, C]
	return o.decode(t.GetData(), int(shape[1]), int(shape[2])), nil
}

// decode 是 CTC 贪心解码：逐帧取最大类别，去掉连续重复和 blank。
func (o *OCR) decode(p []float32, steps, classes int) string {
	var sb strings.Builder
	prev := -1
	for t := 0; t < steps; t++ {
		row := p[t*classes : (t+1)*classes]
		best := 0
		for c := 1; c < classes; c++ {
			if row[c] > row[best] {
				best = c
			}
		}
		if best != prev && best != 0 && best < len(o.chars) {
			sb.WriteString(o.chars[best])
		}
		prev = best
	}
	return sb.String()
}
