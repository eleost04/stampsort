// stampsort：自动识别照片左下角水印时间并按 年/月/日 分类，把 4:3 照片排成 6 寸（4x6in）打印文件。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/eleost04/stampsort/internal/desktop"
	"github.com/eleost04/stampsort/internal/input"
	"github.com/eleost04/stampsort/internal/job"
	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
	"github.com/eleost04/stampsort/internal/web"
	"github.com/eleost04/stampsort/models"
)

var version = "dev" // 构建时用 -ldflags "-X main.version=..." 注入

const usage = `stampsort %s — 水印照片按时间分类 + 6 寸打印排版

用法:
  stampsort all   照片/目录/zip... [-o 输出目录]   分类 + 6 寸（默认）
  stampsort sort  照片/目录/zip... [-o 输出目录]   只按 年/月/日 分类
  stampsort print 照片/目录/zip... [-o 输出目录]   只做 6 寸打印文件
  stampsort web   [--addr 127.0.0.1:8930]          启动网页版（服务器部署用）
  stampsort gui   [照片/目录/zip...]               桌面版界面
  stampsort version

Windows 下双击 stampsort.exe 或把照片拖到它的图标上，直接打开桌面版界面。
在 cmd 里运行命令行时提示符会先返回；写脚本请用 start /wait stampsort all ...
各子命令的参数用 "stampsort <子命令> -h" 查看。

例子:
  stampsort all D:\照片                      -> D:\照片_整理\分类\2026\08\13\2026-08-13_06-20.jpg
                                                D:\照片_整理\6寸\2026\08\13\待打印_横版.pdf
  stampsort sort D:\照片 工地.zip -o D:\已分类 --move
  stampsort print D:\照片 --pdf --bleed 2.5  -> D:\照片_6寸\
  stampsort print a.jpg --mode crop          纯裁切，不变形
`

func main() {
	args := os.Args[1:]
	// Windows：双击，或把文件/文件夹拖到 exe 图标上（参数都是路径）时打开界面。
	if runtime.GOOS == "windows" && (len(args) == 0 || !isSubcommand(args[0])) {
		if err := runGUI(args); err != nil {
			desktop.ShowError("stampsort", err.Error())
			os.Exit(1)
		}
		return
	}
	attachConsole() // Windows 下 exe 是 GUI 程序，命令行用法要挂到父控制台才能输出

	if len(args) == 0 {
		fmt.Printf(usage, version)
		return
	}
	cmd := "all"
	switch args[0] {
	case "all", "sort", "print", "web", "gui":
		cmd, args = args[0], args[1:]
	case "healthcheck": // 给 Docker HEALTHCHECK 用（distroless 里没有 curl）
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case "version", "-v", "--version":
		fmt.Println("stampsort", version)
		return
	case "-h", "--help", "help":
		fmt.Printf(usage, version)
		return
	}
	var err error
	switch cmd {
	case "web":
		err = runWeb(args)
	case "gui":
		err = runGUI(args)
	default:
		err = runBatch(cmd, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func isSubcommand(a string) bool {
	switch a {
	case "all", "sort", "print", "web", "gui", "healthcheck", "version", "-v", "--version", "-h", "--help", "help":
		return true
	}
	return false
}

// runGUI 启动桌面版。参数里的路径会自动加入。
func runGUI(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:0", "本机监听地址（默认随机端口）")
	browser := fs.Bool("browser", false, "用浏览器打开界面（不用独立窗口）")
	noOpen := fs.Bool("no-open", false, "只打印地址，不自动打开浏览器")
	noOCR := fs.Bool("no-ocr", false, "不识别水印，只用 EXIF / 文件名")
	paths, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	// 已经有窗口开着（再次双击、右键"发送到"）：把文件交给它，自己退出
	if !*browser && !desktop.AcquireSingleInstance() && desktop.Forward("", paths) {
		return nil
	}
	det, ocrErr := loadDetector(*noOCR)
	if det.OCR != nil {
		defer det.OCR.Close()
	}
	var ocrMsg string
	if ocrErr != nil {
		ocrMsg = ocrErr.Error()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return desktop.Run(ctx, desktop.Options{Addr: *addr, Paths: paths, Detector: det, OCRError: ocrMsg, Version: version, Browser: *browser, NoOpen: *noOpen})
}

// parseInterleaved 允许参数和路径混写（stampsort print D:\照片 --pdf）。
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newDetector(noOCR bool) *stamp.Detector {
	det, err := loadDetector(noOCR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "注意: OCR 不可用（%v），只能按 EXIF / 文件名识别时间\n", err)
	}
	return det
}

// loadDetector 加载 OCR；失败时返回不带 OCR 的识别器和原因。
func loadDetector(noOCR bool) (*stamp.Detector, error) {
	if noOCR {
		return &stamp.Detector{}, nil
	}
	lib, err := stamp.FindLib()
	if err == nil {
		var ocr *stamp.OCR
		if ocr, err = stamp.NewOCR(lib, models.Rec); err == nil {
			return &stamp.Detector{OCR: ocr}, nil
		}
	}
	return &stamp.Detector{}, err
}

func runBatch(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	out := fs.String("o", "", "输出目录（默认在第一个输入旁边建 <名字>_整理 / _分类 / _6寸）")
	move := fs.Bool("move", false, "分类时移动原图（默认复制，原图不动）")
	noOCR := fs.Bool("no-ocr", false, "不识别水印，只用 EXIF / 文件名")
	mode := fs.String("mode", "c", "6 寸排版：c=方案C（裁一点顶+主体轻压缩，水印不变形）crop=纯裁切")
	dpi := fs.Int("dpi", print6.DPI, "输出分辨率")
	cropRatio := fs.Float64("crop-ratio", 0.5, "方案C：多余高度里靠裁顶消化的比例，0=全靠压缩 1=全靠裁切")
	cropTop := fs.Int("crop-top", -1, "方案C：直接指定顶部裁切像素，覆盖 --crop-ratio")
	pdf := fs.Bool("pdf", cmd == "all", "合成多页 PDF，一次打印一批（all 默认开启）")
	bleed := fs.Float64("bleed", 0, "额外生成出血版，向外扩百分比（无边距打印建议 2.5）")
	chunk := fs.Int("chunk", 30, "每个 PDF 最多几页")
	quality := fs.Int("quality", 95, "JPEG 质量")
	noJPG := fs.Bool("no-jpg", false, "只要 PDF，不留单张 6 寸 JPEG")
	workers := fs.Int("j", 0, "并行数（默认 CPU 核数）")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "用法: stampsort %s 照片/目录/zip... [参数]\n\n", cmd)
		fs.PrintDefaults()
	}
	srcs, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(srcs) == 0 {
		fs.Usage()
		return errors.New("没有指定照片、目录或 zip")
	}
	if *mode != "c" && *mode != "crop" {
		return fmt.Errorf("--mode 只能是 c 或 crop")
	}

	o := job.Options{
		Sort: cmd != "print", Print: cmd != "sort", Move: *move,
		Layout: print6.Options{Mode: print6.Mode(*mode), DPI: *dpi, CropRatio: *cropRatio, CropTop: *cropTop},
		PDF:    *pdf, Bleed: *bleed, Chunk: *chunk, Quality: *quality, NoJPG: *noJPG, Workers: *workers,
	}
	if o.NoJPG {
		o.PDF = true
	}
	o.OutDir = *out
	if o.OutDir == "" {
		suffix := map[string]string{"all": "_整理", "sort": "_分类", "print": "_6寸"}[cmd]
		first := strings.TrimRight(srcs[0], `/\`)
		base := strings.TrimSuffix(filepath.Base(first), filepath.Ext(first))
		if st, err := os.Stat(first); err == nil && st.IsDir() {
			base = filepath.Base(first)
		}
		o.OutDir = filepath.Join(filepath.Dir(first), base+suffix)
	}

	tmp, err := os.MkdirTemp("", "stampsort-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	items, warns, err := input.Collect(srcs, tmp, input.DefaultLimits)
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "注意:", w)
	}
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("没找到图片（支持 jpg/png 和 zip）")
	}

	var det *stamp.Detector
	if o.Sort {
		det = newDetector(*noOCR)
		if det.OCR != nil {
			defer det.OCR.Close()
		}
	}
	abs, _ := filepath.Abs(o.OutDir)
	fmt.Printf("共 %d 张 -> %s\n\n", len(items), abs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sum, err := job.Run(ctx, items, det, o, func(done, total int, r job.Result) {
		printResult(os.Stdout, done, total, r, o)
	})
	if sum != nil {
		printSummary(sum, o, abs)
	}
	return err
}

func printResult(w io.Writer, done, total int, r job.Result, o job.Options) {
	name := []rune(r.Name)
	if len(name) > 36 {
		name = append([]rune("…"), name[len(name)-35:]...)
	}
	line := fmt.Sprintf("[%*d/%d] %-36s", len(fmt.Sprint(total)), done, total, string(name))
	if r.Err != "" {
		fmt.Fprintf(w, "%s  失败: %s\n", line, r.Err)
		return
	}
	if o.Sort {
		t := "未识别"
		if r.Stamp.Source != stamp.None {
			t = r.Stamp.Time.Format("2006-01-02 15:04")
			if !r.Stamp.HasClock {
				t = r.Stamp.Time.Format("2006-01-02") + "      "
			}
		}
		line += fmt.Sprintf("  %s (%s)", t, map[stamp.Source]string{stamp.FromOCR: "水印", stamp.FromEXIF: "EXIF", stamp.FromName: "文件名", stamp.None: "-"}[r.Stamp.Source])
	}
	if o.Print && r.Layout.Out.X > 0 {
		line += fmt.Sprintf("  裁顶%4dpx 压缩%4.1f%%", r.Layout.CropTop, r.Layout.Squash*100)
	}
	fmt.Fprintf(w, "%s  %dms\n", line, r.Elapsed.Milliseconds())
}

func printSummary(sum *job.Summary, o job.Options, abs string) {
	var notes []string
	failed, unknown := 0, 0
	for _, r := range sum.Results {
		if r.Name == "" {
			continue
		}
		if r.Err != "" {
			failed++
		}
		if o.Sort && r.Err == "" && r.Stamp.Source == stamp.None {
			unknown++
		}
		for _, w := range r.Warnings {
			if !strings.Contains(w, "未识别") {
				notes = append(notes, r.Name+": "+w)
			}
		}
	}
	notes = append(notes, sum.Warnings...)
	if len(notes) > 0 {
		fmt.Println("\n注意:")
		for _, n := range notes {
			fmt.Println("  -", n)
		}
	}
	fmt.Printf("\n完成 %d 张，用时 %.1f 秒", len(sum.Results)-failed, sum.Elapsed.Seconds())
	if unknown > 0 {
		fmt.Printf("，%d 张没识别出时间（在「未识别」目录）", unknown)
	}
	if failed > 0 {
		fmt.Printf("，%d 张失败", failed)
	}
	fmt.Printf("\n输出: %s\n报告: %s\n", abs, filepath.Join(abs, sum.Report))
	for _, p := range sum.PDFs {
		fmt.Println("PDF :", filepath.Join(abs, filepath.FromSlash(p)))
	}
	if o.Print {
		fmt.Println("\n打印: 文档尺寸 10x15cm(4x6in) / 横向 / 光泽照片纸 / 质量 高")
		fmt.Println("      要满版就勾'无边距'并把'扩展'调成'最小'；PDF 用'实际大小/100%'打印")
	}
}

func runWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	addr := fs.String("addr", envOr("STAMPSORT_ADDR", "127.0.0.1:8930"), "监听地址")
	data := fs.String("data", envOr("STAMPSORT_DATA", defaultDataDir()), "任务文件目录")
	password := fs.String("password", os.Getenv("STAMPSORT_PASSWORD"), "访问密码（也可用环境变量 STAMPSORT_PASSWORD）")
	maxMB := fs.Int64("max-upload", envInt("STAMPSORT_MAX_UPLOAD_MB", 1024), "单次上传上限（MB），也可用环境变量 STAMPSORT_MAX_UPLOAD_MB")
	keep := fs.Duration("keep", 24*time.Hour, "任务结果保留时长")
	noAuth := fs.Bool("no-auth", false, "允许在非本机地址上无密码运行（不建议）")
	noOCR := fs.Bool("no-ocr", false, "不加载 OCR")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("--addr 格式不对: %w", err)
	}
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if *password == "" && !loopback && !*noAuth {
		return errors.New("监听非本机地址时必须设置密码（--password 或 STAMPSORT_PASSWORD）")
	}

	det := newDetector(*noOCR)
	srv, err := web.New(web.Config{DataDir: *data, Password: *password, MaxUpload: *maxMB << 20, Keep: *keep, Detector: det, Version: version})
	if err != nil {
		return err
	}
	hs := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	ocr := "已加载"
	if det.OCR == nil {
		ocr = "未加载"
	}
	fmt.Printf("stampsort %s 网页版: http://%s  （OCR %s，数据目录 %s）\n", version, *addr, ocr, *data)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int64) int64 {
	if n, err := strconv.ParseInt(os.Getenv(k), 10, 64); err == nil && n > 0 {
		return n
	}
	return def
}

// healthcheck 请求本机网页服务首页。
func healthcheck() error {
	_, port, err := net.SplitHostPort(envOr("STAMPSORT_ADDR", "127.0.0.1:8930"))
	if err != nil {
		return err
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("状态 %d", resp.StatusCode)
	}
	return nil
}

func defaultDataDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "stampsort")
	}
	return filepath.Join(os.TempDir(), "stampsort")
}
