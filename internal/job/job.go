// Package job 是命令行和网页共用的处理流程：识别时间 → 分类 → 6 寸排版 → PDF → 报告。
package job

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eleost04/stampsort/internal/imgx"
	"github.com/eleost04/stampsort/internal/input"
	"github.com/eleost04/stampsort/internal/organize"
	"github.com/eleost04/stampsort/internal/photo"
	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
)

// 输出目录名。
const (
	Report = "report.csv"
)

// Options 决定做哪些事。目录由模板决定（见 organize.Render），模板为空时用默认：
//   - 分类 + 6 寸：OUT/分类/年/月/日 + OUT/6寸/年/月/日（每个目录一组 PDF）
//   - 只分类：OUT/年/月/日
//   - 只 6 寸：OUT/<原名>_6寸.jpg（与旧脚本一致）；PrintByDate 时按 6 寸模板分目录
//
// 文件名由 NameFormat 决定。
type Options struct {
	Sort, Print bool
	Move        bool // 分类时移动原图而不是复制
	OutDir      string
	Layout      print6.Options
	PDF         bool
	Bleed       float64 // >0 时额外生成出血版
	Chunk       int     // 每个 PDF 最多几页
	Quality     int
	NoJPG       bool // 只要 PDF
	Workers     int
	// Preset 与 items 按下标对应：有值时直接用这个时间，不再识别（桌面版核对后导出用）。
	Preset []*stamp.Result
	// SortTemplate / PrintTemplate：原图、6 寸放在哪个目录（模板，空 = 默认）。
	SortTemplate, PrintTemplate string
	// PDFTemplate：「待打印」PDF 放在哪个目录。为空时和当天的 6 寸放在一起、文件名不带日期（命令行的老做法）；
	// 设置后按日期分组，文件名为 待打印_2026-10-19_横版.pdf。
	PDFTemplate string
	// NameFormat 见 organize.Name*，空为默认。
	NameFormat string
	// PrintByDate：只做 6 寸时也按日期分目录、按时间命名（需要识别时间）。
	PrintByDate bool
	// ReportName 是报告的相对路径，默认 report.csv；NoReport 时不写报告。
	ReportName string
	NoReport   bool
	// Mark：把时间写进导出的 JPEG（EXIF 拍摄日期 + stampsort 标记），以后再加入时不用重新识别。
	Mark bool
}

// Result 是一张图的处理结果。路径都是相对 OutDir 的斜杠路径。
type Result struct {
	Index    int
	Name     string
	Size     image.Point
	Stamp    stamp.Result
	Sorted   string
	Existed  bool // 输出目录里已有同内容文件，没有重复复制
	Print    string
	Layout   print6.Info
	Warnings []string
	Err      string
	Elapsed  time.Duration
}

// Summary 是整批的结果。
type Summary struct {
	Results  []Result
	PDFs     []string
	Report   string
	Warnings []string
	Elapsed  time.Duration
}

// Progress 每处理完一张调用一次（按完成顺序，不是输入顺序）。
type Progress func(done, total int, r Result)

type runner struct {
	o       Options
	det     *stamp.Detector
	sorted  *organize.Namer
	printed *organize.Namer
	turn    *turnstile
	tmpDir  string

	sortT, printT string // 生效的模板（已代入默认值）

	mu     sync.Mutex
	groups map[pdfGroup]*pdfPages
}

// pdfGroup 是一份（或几份分卷的）「待打印」PDF。date 为空表示按 6 寸目录分组的老做法。
type pdfGroup struct {
	dir  string
	date string
}

type pdfPages struct {
	pages    []print6.PDFPage
	srcDirs  map[string]bool // 这一组的 6 寸照片所在目录（合并以前导出的照片时扫描）
	thisRun  map[string]bool
	printDay bool // 6 寸模板是否每天一个目录（是的话目录里没有标记的照片也算同一天）
}

// Run 处理 items。det 可为 nil（只排版不识别时）。ctx 取消后尽快停止，已完成的结果仍会写报告。
func Run(ctx context.Context, items []input.Item, det *stamp.Detector, o Options, progress Progress) (*Summary, error) {
	start := time.Now()
	if o.Workers <= 0 {
		o.Workers = runtime.NumCPU()
	}
	if o.Chunk <= 0 {
		o.Chunk = 30
	}
	if o.Quality <= 0 {
		o.Quality = 95
	}
	if o.Layout.DPI <= 0 {
		o.Layout.DPI = print6.DPI
	}
	if det == nil {
		det = &stamp.Detector{}
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return nil, err
	}
	r := &runner{o: o, det: det, turn: newTurnstile(), groups: map[pdfGroup]*pdfPages{}}
	r.sortT, r.printT = o.templates()
	r.sorted = organize.NewNamer(o.OutDir)
	r.printed = organize.NewNamer(o.OutDir)
	if o.Print && (o.NoJPG || o.PDF) {
		tmp, err := os.MkdirTemp(o.OutDir, ".tmp-")
		if err != nil {
			return nil, err
		}
		r.tmpDir = tmp
		defer os.RemoveAll(tmp)
	}

	results := make([]Result, len(items))
	idx := make(chan int)
	var wg sync.WaitGroup
	var doneMu sync.Mutex
	done := 0
	for range min(o.Workers, max(1, len(items))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				res := r.one(ctx, i, items[i])
				results[i] = res
				doneMu.Lock()
				done++
				if progress != nil {
					progress(done, len(items), res)
				}
				doneMu.Unlock()
			}
		}()
	}
feed:
	for i := range items {
		select {
		case idx <- i:
		case <-ctx.Done():
			r.turn.cancel()
			break feed
		}
	}
	close(idx)
	wg.Wait()

	sum := &Summary{Results: results}
	if ctx.Err() != nil {
		sum.Warnings = append(sum.Warnings, "已取消，只处理了部分照片")
	}
	if o.Print && o.PDF {
		pdfs, err := r.writePDFs()
		sum.PDFs = pdfs
		if err != nil {
			return sum, err
		}
	}
	if !o.NoReport {
		sum.Report = Report
		if o.ReportName != "" {
			sum.Report = o.ReportName
		}
		if err := writeReport(filepath.Join(o.OutDir, filepath.FromSlash(sum.Report)), results); err != nil {
			return sum, err
		}
	}
	sum.Elapsed = time.Since(start)
	return sum, ctx.Err()
}

// templates 返回生效的原图、6 寸目录模板（空的用默认值）。
func (o Options) templates() (sortT, printT string) {
	sortT, printT = o.SortTemplate, o.PrintTemplate
	both := o.Sort && o.Print
	if sortT == "" {
		sortT = "{年}/{月}/{日}"
		if both {
			sortT = organize.DefaultSortTemplate
		}
	}
	if printT == "" {
		printT = "{年}/{月}/{日}"
		if both {
			printT = organize.DefaultPrintTemplate
		}
	}
	return sortT, printT
}

// one 处理一张图。命名在 turnstile 里按输入顺序进行，保证 _2、_3 的编号稳定。
func (r *runner) one(ctx context.Context, i int, it input.Item) (res Result) {
	start := time.Now()
	res = Result{Index: i, Name: it.Name}
	named := false
	defer func() {
		if !named {
			r.turn.skip(i)
		}
		res.Elapsed = time.Since(start)
	}()
	if ctx.Err() != nil {
		res.Err = "已取消"
		return
	}

	var img *image.RGBA
	var gray []uint8
	var meta photo.Meta
	preset := r.preset(i)
	needPixels := r.o.Print || ((r.o.Sort || r.o.PrintByDate) && preset == nil && r.det.OCR != nil)
	if needPixels {
		var err error
		img, meta, err = photo.Load(it.Path)
		if err != nil {
			res.Err = "打不开: " + err.Error()
			return
		}
		res.Size = img.Rect.Size()
		gray = imgx.Luma(img)
	} else if data, err := os.ReadFile(it.Path); preset == nil && err == nil {
		meta = photo.ParseExif(data)
	}

	if r.o.Sort || r.o.PrintByDate {
		if preset != nil {
			res.Stamp = *preset
		} else {
			res.Stamp = r.det.Detect(img, gray, meta, it.Name)
		}
	}

	// 要写入时间标记时，分类后的文件内容 = 原图 + 标记；去重也要按写入后的内容比较。
	// 读文件、写标记放在下面的按序命名之前做，不占用串行区。
	var marked []byte
	if r.o.Sort && r.o.Mark && res.Stamp.Source != stamp.None {
		if src, err := os.ReadFile(it.Path); err == nil {
			marked, _ = photo.WithMark(src, markOf(res.Stamp)) // 不是 JPEG 时为 nil，按原样复制
		}
	}

	// 按输入顺序分配名字
	if !r.turn.wait(i) {
		res.Err = "已取消"
		return
	}
	var sortedRel, printDir, printStem string
	ext := strings.ToLower(filepath.Ext(it.Path))
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	if r.o.Sort {
		dir, stem := organize.Render(r.sortT, res.Stamp), organize.Stem(res.Stamp, it.Name, r.o.NameFormat)
		same := func(p string) bool { return organize.SameFile(it.Path, p) }
		if marked != nil {
			same = func(p string) bool { return organize.SameContent(marked, p) || isSamePath(it.Path, p) }
		}
		sortedRel, res.Existed = r.sorted.Reserve(dir, stem, ext, same)
		printDir = organize.Render(r.printT, res.Stamp)
		printStem = strings.TrimSuffix(path.Base(sortedRel), path.Ext(sortedRel))
	} else if r.o.PrintByDate {
		printDir = organize.Render(r.printT, res.Stamp)
		printStem = organize.Stem(res.Stamp, it.Name, r.o.NameFormat)
	} else {
		base := path.Base(filepath.ToSlash(it.Name))
		printStem = strings.TrimSuffix(base, path.Ext(base))
	}
	var printRel string
	if r.o.Print && !r.o.NoJPG {
		// 成品是重新生成的：上次运行留下的同名文件直接覆盖，本次运行内的重名仍会加序号
		printRel, _ = r.printed.Reserve(printDir, printStem+input.SkipSuffix, ".jpg", func(string) bool { return true })
	}
	r.turn.done(i)
	named = true

	if r.o.Sort {
		res.Sorted = sortedRel
		if !res.Existed {
			dst := filepath.Join(r.o.OutDir, filepath.FromSlash(res.Sorted))
			var err error
			if marked != nil {
				err = organize.PlaceData(it.Path, dst, marked, r.o.Move)
			} else {
				err = organize.Place(it.Path, dst, r.o.Move)
			}
			if err != nil {
				res.Err = "分类失败: " + err.Error()
				return
			}
		} else if r.o.Move && !isSamePath(it.Path, filepath.Join(r.o.OutDir, filepath.FromSlash(res.Sorted))) {
			// 目标位置已有同内容文件，移动 = 删掉源文件。但源文件本身就在目标位置时
			// （对已整理好的目录再整理一次）绝不能删，否则唯一的一份就没了。
			os.Remove(it.Path)
		}
		if res.Stamp.Source == stamp.None {
			res.Warnings = append(res.Warnings, "没识别出时间，放进「未识别」")
		}
	}

	if r.o.Print {
		if err := r.print(img, gray, &res, printDir, printRel); err != nil {
			res.Err = "排版失败: " + err.Error()
		}
	}
	return
}

// isSamePath 判断两个路径是否指向同一个文件（考虑大小写、相对路径、链接）。
func isSamePath(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// markOf 把识别结果转成要写进照片的标记。时间本来就来自标记时，沿用标记里记录的最初来源。
func markOf(r stamp.Result) photo.Mark {
	src := string(r.Source)
	if r.Source == stamp.FromMark && r.Orig != "" {
		src = r.Orig
	}
	return photo.Mark{Time: r.Time, HasClock: r.HasClock, Source: src}
}

func (r *runner) preset(i int) *stamp.Result {
	if i < len(r.o.Preset) {
		return r.o.Preset[i]
	}
	return nil
}

func (r *runner) print(img *image.RGBA, gray []uint8, res *Result, dir, rel string) error {
	out, info := print6.Make(img, gray, r.o.Layout)
	res.Layout = info
	if info.WM == nil {
		res.Warnings = append(res.Warnings, "没探测到水印，按普通照片处理")
	}
	if info.Portrait {
		res.Warnings = append(res.Warnings, "竖图，已按竖版 4x6 输出，需单独打印")
	}
	if info.CutX > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("原图比 3:2 还扁，左右裁掉 %dpx", info.CutX))
	}
	dpi, q := r.o.Layout.DPI, r.o.Quality
	var mark *photo.Mark
	if r.o.Mark && res.Stamp.Source != stamp.None && res.Stamp.Source != "" {
		m := markOf(res.Stamp)
		mark = &m
	}
	var jpgPath string
	if rel != "" {
		res.Print = rel
		jpgPath = filepath.Join(r.o.OutDir, filepath.FromSlash(res.Print))
		if err := os.MkdirAll(filepath.Dir(jpgPath), 0o755); err != nil {
			return err
		}
		if err := print6.SaveJPEGMark(jpgPath, out, q, dpi, mark); err != nil {
			return err
		}
	}
	if r.o.Bleed > 0 && rel != "" {
		bp := strings.TrimSuffix(jpgPath, ".jpg") + fmt.Sprintf("_出血%g%%.jpg", r.o.Bleed)
		if err := print6.SaveJPEGMark(bp, print6.AddBleed(out, r.o.Bleed, 25), q, dpi, mark); err != nil {
			return err
		}
	}
	if r.o.PDF {
		if jpgPath == "" { // --no-jpg：先写到临时目录给 PDF 用
			jpgPath = filepath.Join(r.tmpDir, fmt.Sprintf("%06d.jpg", res.Index))
			if err := print6.SaveJPEG(jpgPath, out, q, dpi); err != nil {
				return err
			}
		}
		g := pdfGroup{dir: dir}
		if r.o.PDFTemplate != "" {
			pdfDir := organize.Render(r.o.PDFTemplate, res.Stamp)
			if res.Stamp.Source == stamp.None { // 文件名里已经有"未识别"，不用再套一层未识别文件夹
				pdfDir = organize.FixedPart(r.o.PDFTemplate)
			}
			g = pdfGroup{dir: pdfDir, date: dateLabel(res.Stamp)}
		}
		r.mu.Lock()
		pg := r.groups[g]
		if pg == nil {
			pg = &pdfPages{srcDirs: map[string]bool{}, thisRun: map[string]bool{}, printDay: organize.TemplateHasDay(r.printT)}
			r.groups[g] = pg
		}
		pg.pages = append(pg.pages, print6.PDFPage{JPEG: jpgPath, W: out.Rect.Dx(), H: out.Rect.Dy()})
		pg.srcDirs[dir] = true
		pg.thisRun[jpgPath] = true
		r.mu.Unlock()
	}
	return nil
}

// dateLabel 是 PDF 文件名里的日期。
func dateLabel(r stamp.Result) string {
	if r.Source == stamp.None || r.Source == "" {
		return organize.Unknown
	}
	return r.Time.Format("2006-01-02")
}

// writePDFs 按组写「待打印」PDF：横版、竖版分开，按文件名排序，每 Chunk 页一个文件。
//
// 单张 6 寸 JPEG 存在时，PDF 收录这一组对应目录下的全部 6 寸照片（包括以前导出到同一目录的），
// 这样多次导出到同一个文件夹，每天的 PDF 始终是完整的。
func (r *runner) writePDFs() ([]string, error) {
	groups := make([]pdfGroup, 0, len(r.groups))
	for g := range r.groups {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].dir != groups[j].dir {
			return groups[i].dir < groups[j].dir
		}
		return groups[i].date < groups[j].date
	})
	var out []string
	for _, g := range groups {
		pg := r.groups[g]
		pages := pg.pages
		if !r.o.NoJPG {
			pages = r.dirPages(g, pg)
		}
		abs := filepath.Join(r.o.OutDir, filepath.FromSlash(g.dir))
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return out, err
		}
		removeOldPDFs(abs, g.date)
		for _, portrait := range []bool{false, true} {
			var part []print6.PDFPage
			for _, p := range pages {
				if (p.H > p.W) == portrait {
					part = append(part, p)
				}
			}
			sort.Slice(part, func(i, j int) bool { return input.NaturalLess(part[i].JPEG, part[j].JPEG) })
			tag := "横版"
			if portrait {
				tag = "竖版"
			}
			base := "待打印_" + tag
			if g.date != "" {
				base = "待打印_" + g.date + "_" + tag
			}
			for i := 0; i < len(part); i += r.o.Chunk {
				chunk := part[i:min(i+r.o.Chunk, len(part))]
				name := base
				if len(part) > r.o.Chunk {
					name += fmt.Sprintf("_%d", i/r.o.Chunk+1)
				}
				rel := path.Join(g.dir, name+".pdf")
				if err := print6.WritePDF(filepath.Join(r.o.OutDir, filepath.FromSlash(rel)), chunk, r.o.Layout.DPI); err != nil {
					return out, err
				}
				out = append(out, rel)
			}
		}
	}
	return out, nil
}

var rePrintPDF = regexp.MustCompile(`^待打印_(?:(\d{4}-\d{2}-\d{2}|未识别)_)?(横|竖)版(_\d+)?\.pdf$`)

// removeOldPDFs 删掉本程序以前为这一组生成的 PDF（分卷数可能变了）。只删名字完全符合的。
func removeOldPDFs(dir, date string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if m := rePrintPDF.FindStringSubmatch(e.Name()); m != nil && m[1] == date {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// dirPages 收集这一组对应 6 寸目录里的全部成品。按日期分组时只收同一天的：
// 照片里有时间标记就按标记判断；没有标记时，6 寸目录本身按天分的才算。
func (r *runner) dirPages(g pdfGroup, pg *pdfPages) []print6.PDFPage {
	var pages []print6.PDFPage
	seen := map[string]bool{}
	for dir := range pg.srcDirs {
		abs := filepath.Join(r.o.OutDir, filepath.FromSlash(dir))
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, input.SkipSuffix+".jpg") { // 跳过出血版和其它文件
				continue
			}
			p := filepath.Join(abs, name)
			if seen[p] {
				continue
			}
			seen[p] = true
			head := readHead(p, 128<<10)
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(head))
			if err != nil {
				continue
			}
			if g.date != "" && !pg.thisRun[p] {
				if m := photo.ParseExif(head).Mark; m != nil {
					if m.Time.Format("2006-01-02") != g.date {
						continue
					}
				} else if !pg.printDay {
					continue
				}
			}
			pages = append(pages, print6.PDFPage{JPEG: p, W: cfg.Width, H: cfg.Height})
		}
	}
	return pages
}

func readHead(p string, n int) []byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	k, _ := io.ReadFull(f, buf)
	return buf[:k]
}

// writeReport 写 UTF-8 BOM 的 CSV，Excel 直接打开不乱码。
func writeReport(p string, results []Result) error {
	os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	f.WriteString("\ufeff")
	w := csv.NewWriter(f)
	w.Write([]string{"序号", "原文件", "时间", "来源", "识别原文", "分类后", "6寸文件", "裁顶px", "主体压缩%", "备注"})
	for _, r := range results {
		if r.Name == "" {
			continue
		}
		tm := ""
		if r.Stamp.Source != stamp.None && r.Stamp.Source != "" {
			tm = r.Stamp.Time.Format("2006-01-02 15:04:05")
			if !r.Stamp.HasClock {
				tm = r.Stamp.Time.Format("2006-01-02")
			}
		}
		note := strings.Join(r.Warnings, "；")
		if r.Err != "" {
			note = strings.TrimPrefix(r.Err+"；"+note, "；")
		}
		crop, squash := "", ""
		if r.Print != "" || r.Layout.Out.X > 0 {
			crop = fmt.Sprint(r.Layout.CropTop)
			squash = fmt.Sprintf("%.1f", r.Layout.Squash*100)
		}
		w.Write([]string{fmt.Sprint(r.Index + 1), r.Name, tm, string(r.Stamp.Source), r.Stamp.Raw, r.Sorted, r.Print, crop, squash, note})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ------------------------------------------------------------------ turnstile

// turnstile 让并发的 worker 按下标顺序依次通过一个临界区。
type turnstile struct {
	mu        sync.Mutex
	cond      *sync.Cond
	next      int
	finished  map[int]bool
	cancelled bool
}

func newTurnstile() *turnstile {
	t := &turnstile{finished: map[int]bool{}}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// wait 阻塞到轮到 i；被取消时返回 false。
func (t *turnstile) wait(i int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for t.next != i && !t.cancelled {
		t.cond.Wait()
	}
	return !t.cancelled
}

func (t *turnstile) done(i int) { t.skip(i) }

// skip 标记 i 已通过（出错的图也要调用，否则后面的会一直等）。
func (t *turnstile) skip(i int) {
	t.mu.Lock()
	t.finished[i] = true
	for t.finished[t.next] {
		delete(t.finished, t.next)
		t.next++
	}
	t.mu.Unlock()
	t.cond.Broadcast()
}

func (t *turnstile) cancel() {
	t.mu.Lock()
	t.cancelled = true
	t.mu.Unlock()
	t.cond.Broadcast()
}
