package organize

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/eleost04/stampsort/internal/stamp"
)

// 目录模板：导出时每张照片放在哪个文件夹，由模板按照片的时间生成。
//
//	{年} 2026   {月} 08   {月份} 8（不补零）   {日} 09   {日期} 2026-08-09
//
// 其它文字原样保留，"/" 和 "\" 都可以当分隔符。例如 `{年}\{月份}月\原图\{日}`
// → `2026/8月/原图/09`。模板为空表示直接放在保存位置下。
var placeholders = []string{"{年}", "{月份}", "{月}", "{日}", "{日期}"}

// 默认模板（程序里只放通用的，具体的文件夹名由用户在设置里自己填）。
const (
	DefaultSortTemplate  = "分类/{年}/{月}/{日}"
	DefaultPrintTemplate = "6寸/{年}/{月}/{日}"
	DefaultPDFTemplate   = "待打印"
)

// CleanTemplate 统一分隔符、去掉首尾的斜杠和空白。
func CleanTemplate(t string) string {
	t = strings.ReplaceAll(strings.TrimSpace(t), `\`, "/")
	parts := strings.Split(t, "/")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// ValidTemplate 检查模板能否安全地生成相对路径。
func ValidTemplate(t string) error {
	t = CleanTemplate(t)
	if len([]rune(t)) > 200 {
		return errors.New("模板太长")
	}
	if strings.ContainsAny(t, `:*?"<>|`) {
		return errors.New(`模板里不能有 : * ? " < > | 这些字符`)
	}
	for _, seg := range strings.Split(t, "/") {
		if seg == "." || seg == ".." {
			return errors.New("模板里不能有 . 或 .. 这样的目录")
		}
		if strings.HasSuffix(seg, ".") {
			return fmt.Errorf("文件夹名不能以 . 结尾：%s", seg)
		}
	}
	// 去掉已知占位符后不能再剩下花括号（多半是写错了）
	rest := t
	for _, p := range placeholders {
		rest = strings.ReplaceAll(rest, p, "")
	}
	if strings.ContainsAny(rest, "{}") {
		return errors.New("不认识的占位符，只能用 {年} {月} {月份} {日} {日期}")
	}
	return nil
}

// Render 按照片时间生成相对目录（斜杠分隔）。识别失败的照片放到模板里固定的文件夹名下的「未识别」：
// `{年}/{月}月/原图/{日}` → `原图/未识别`，`分类/{年}/{月}/{日}` → `分类/未识别`，这样原图和 6 寸不会混在一起。
func Render(t string, r stamp.Result) string {
	t = CleanTemplate(t)
	if r.Source == stamp.None || r.Source == "" {
		return path.Join(FixedPart(t), Unknown)
	}
	tm := r.Time
	return strings.NewReplacer(
		"{日期}", tm.Format("2006-01-02"),
		"{年}", tm.Format("2006"),
		"{月份}", tm.Format("1"),
		"{月}", tm.Format("01"),
		"{日}", tm.Format("02"),
	).Replace(t)
}

// FixedPart 是模板里不含占位符的那些文件夹，例如 "{年}/{月}月/原图/{日}" → "原图"。
func FixedPart(t string) string {
	var out []string
	for _, seg := range strings.Split(CleanTemplate(t), "/") {
		if seg != "" && !strings.Contains(seg, "{") {
			out = append(out, seg)
		}
	}
	return strings.Join(out, "/")
}

// TemplateSplits 报告模板是否按年、按月区分（不区分时，跨年/跨月的照片会混进同一个文件夹）。
func TemplateSplits(t string) (year, month bool) {
	has := func(p string) bool { return strings.Contains(t, p) }
	return has("{年}") || has("{日期}"), has("{月}") || has("{月份}") || has("{日期}")
}

// TemplateHasDay 报告模板是否每天一个文件夹。
func TemplateHasDay(t string) bool {
	return strings.Contains(t, "{日}") || strings.Contains(t, "{日期}")
}

// LegacyTemplates 把旧版"按什么分文件夹"的选项换成模板（读旧设置时用）。
func LegacyTemplates(dirFormat string) (sortT, printT string, ok bool) {
	tail, ok := map[string]string{
		"ymd": "{年}/{月}/{日}", "ym": "{年}/{月}", "md": "{月}/{日}", "d": "{日}", "flat": "{日期}",
	}[dirFormat]
	if !ok {
		return "", "", false
	}
	return "分类/" + tail, "6寸/" + tail, true
}
