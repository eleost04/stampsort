// Package input 把命令行/网页传入的文件、目录、zip 展开成待处理的图片列表。
package input

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/eleost04/stampsort/internal/photo"
)

// Item 是一张待处理的图片。
type Item struct {
	Path string // 磁盘路径（zip 里的已解压到临时目录）
	Name string // 显示/报告用的名字，zip 内为 "包名.zip/内部路径"
}

// Limits 限制 zip 解压，防压缩炸弹。
type Limits struct {
	MaxFiles int
	MaxBytes int64
}

// DefaultLimits：最多 2 万张、解压后共 20GB。
var DefaultLimits = Limits{MaxFiles: 20000, MaxBytes: 20 << 30}

// SkipSuffix 是我们自己输出文件的后缀，重复运行时跳过。
const SkipSuffix = "_6寸"

// Collect 展开 srcs。zip 解压到 tmpDir（调用方负责删除）。不是图片的文件记入 warns。
func Collect(srcs []string, tmpDir string, lim Limits) (items []Item, warns []string, err error) {
	for _, s := range srcs {
		st, err := os.Stat(s)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v", s, err))
			continue
		}
		switch {
		case st.IsDir():
			err := filepath.WalkDir(s, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					if p == s {
						return err
					}
					// 子目录没权限（$RECYCLE.BIN、System Volume Information 等）：跳过，别让整个文件夹加不进来
					warns = append(warns, fmt.Sprintf("跳过 %s: %v", p, err))
					if d != nil && d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if d.IsDir() {
					if p != s && strings.HasPrefix(d.Name(), ".") {
						return filepath.SkipDir
					}
					return nil
				}
				if strings.EqualFold(filepath.Ext(p), ".zip") {
					zi, zw, err := extractZip(p, tmpDir, lim)
					items, warns = append(items, zi...), append(warns, zw...)
					return err
				}
				if wanted(d.Name()) {
					rel, _ := filepath.Rel(s, p)
					items = append(items, Item{Path: p, Name: filepath.ToSlash(rel)})
				}
				return nil
			})
			if err != nil {
				return nil, warns, err
			}
		case strings.EqualFold(filepath.Ext(s), ".zip"):
			zi, zw, err := extractZip(s, tmpDir, lim)
			items, warns = append(items, zi...), append(warns, zw...)
			if err != nil {
				return nil, warns, err
			}
		case photo.IsImage(s):
			items = append(items, Item{Path: s, Name: filepath.Base(s)})
		default:
			warns = append(warns, fmt.Sprintf("跳过（不是图片）: %s", s))
		}
	}
	SortNatural(items)
	return items, warns, nil
}

func wanted(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "._") {
		return false
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	return photo.IsImage(name) && !strings.Contains(stem, SkipSuffix)
}

var errTooBig = errors.New("压缩包太大或文件太多")

// zipName 解码 zip 里的文件名：没有 UTF-8 标志位且不是合法 UTF-8 时按 GBK 解码（Windows 打包的中文名）。
func zipName(f *zip.File) string {
	if f.NonUTF8 || !utf8.ValidString(f.Name) {
		if s, err := simplifiedchinese.GBK.NewDecoder().String(f.Name); err == nil {
			return s
		}
	}
	return f.Name
}

// safeRel 校验 zip 内路径，拒绝绝对路径和 ..（zip-slip）。
func safeRel(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
		return "", false
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func extractZip(zipPath, tmpDir string, lim Limits) ([]Item, []string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: 打不开压缩包（%v）", filepath.Base(zipPath), err)}, nil
	}
	defer zr.Close()
	dest, err := os.MkdirTemp(tmpDir, "zip-")
	if err != nil {
		return nil, nil, err
	}
	var items []Item
	var warns []string
	var total int64
	base := filepath.Base(zipPath)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := zipName(f)
		rel, ok := safeRel(name)
		if !ok {
			warns = append(warns, fmt.Sprintf("%s: 跳过不安全路径 %q", base, name))
			continue
		}
		if strings.HasPrefix(rel, "__MACOSX/") || !wanted(path.Base(rel)) {
			continue
		}
		if len(items) >= lim.MaxFiles {
			return nil, warns, errTooBig
		}
		dst := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, warns, err
		}
		n, err := copyLimited(f, dst, lim.MaxBytes-total)
		total += n
		if errors.Is(err, errTooBig) {
			return nil, warns, err
		}
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s/%s: %v", base, rel, err))
			continue
		}
		items = append(items, Item{Path: dst, Name: base + "/" + rel})
	}
	return items, warns, nil
}

func copyLimited(f *zip.File, dst string, remain int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, remain+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if n > remain {
		return n, errTooBig
	}
	return n, err
}

var digits = regexp.MustCompile(`\d+|\D+`)

// SortNatural 按人类习惯的数字顺序排序（IMG_2 在 IMG_10 前面）。
func SortNatural(items []Item) {
	sort.SliceStable(items, func(i, j int) bool { return NaturalLess(items[i].Name, items[j].Name) })
}

// NaturalLess 按数字大小而不是字符比较数字段。
func NaturalLess(x, y string) bool {
	a, b := digits.FindAllString(strings.ToLower(x), -1), digits.FindAllString(strings.ToLower(y), -1)
	for k := 0; k < len(a) && k < len(b); k++ {
		if a[k] == b[k] {
			continue
		}
		na, ea := strconv.ParseUint(a[k], 10, 64)
		nb, eb := strconv.ParseUint(b[k], 10, 64)
		if ea == nil && eb == nil && na != nb {
			return na < nb
		}
		return a[k] < b[k]
	}
	return len(a) < len(b)
}
