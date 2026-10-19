// Package organize 负责按识别出的时间给照片起名、分目录，并复制/移动过去。
package organize

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/eleost04/stampsort/internal/stamp"
)

// Unknown 是识别不出时间的照片所在目录。
const Unknown = "未识别"

// 分类目录的层级格式。
const (
	DirYMD  = "ymd"  // 2026/10/19（默认）
	DirYM   = "ym"   // 2026/10
	DirMD   = "md"   // 10/19
	DirD    = "d"    // 19：适合导出到"资料/10/"这种已经按月建好的文件夹
	DirFlat = "flat" // 2026-10-19 一层
)

// 文件名格式。
const (
	NameDateTime = "datetime" // 2026-10-19_08-21（默认）
	NameTime     = "time"     // 08-21：目录里已经有日期时用
	NameOrig     = "orig"     // 保留原文件名
)

var dirLayouts = map[string]string{DirYMD: "2006/01/02", DirYM: "2006/01", DirMD: "01/02", DirD: "02", DirFlat: "2006-01-02"}

// ValidDirFormat / ValidNameFormat 校验设置值。
func ValidDirFormat(f string) bool  { _, ok := dirLayouts[f]; return ok }
func ValidNameFormat(f string) bool { return f == NameDateTime || f == NameTime || f == NameOrig }

// DirHasMonth 表示该层级是否区分月份（不区分时跨月的照片会混在一起）。
func DirHasMonth(f string) bool { return f != DirD }

// DirHasYear 表示该层级是否区分年份。
func DirHasYear(f string) bool { return f != DirD && f != DirMD }

// DateDir 返回分类目录（如 "2026/10/19"），识别失败返回 "未识别"。format 为空时用年/月/日。
func DateDir(r stamp.Result, format string) string {
	if r.Source == stamp.None || r.Source == "" {
		return Unknown
	}
	layout, ok := dirLayouts[format]
	if !ok {
		layout = dirLayouts[DirYMD]
	}
	return r.Time.Format(layout)
}

// Stem 返回不带扩展名的新文件名。识别失败时总是保留原名。
func Stem(r stamp.Result, origName, format string) string {
	base := path.Base(filepath.ToSlash(origName))
	orig := strings.TrimSuffix(base, path.Ext(base))
	if r.Source == stamp.None || r.Source == "" || format == NameOrig {
		return orig
	}
	if !r.HasClock {
		return r.Time.Format("2006-01-02")
	}
	if format == NameTime {
		return r.Time.Format("15-04")
	}
	return r.Time.Format("2006-01-02_15-04")
}

// Namer 在一个输出目录里分配不冲突的相对路径（同名依次加 _2、_3），并发安全。
type Namer struct {
	root  string
	mu    sync.Mutex
	taken map[string]bool
}

func NewNamer(root string) *Namer { return &Namer{root: root, taken: map[string]bool{}} }

// Reserve 为 dir/stem+ext 分配名字。same 不为 nil 时，若磁盘上已有内容完全相同的文件就直接复用
// （重复运行不会产生 _2 副本），返回值 exists 为 true。
func (n *Namer) Reserve(dir, stem, ext string, same func(existing string) bool) (rel string, exists bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := 1; ; i++ {
		name := stem + ext
		if i > 1 {
			name = fmt.Sprintf("%s_%d%s", stem, i, ext)
		}
		rel = path.Join(dir, name)
		if n.taken[rel] {
			continue
		}
		abs := filepath.Join(n.root, filepath.FromSlash(rel))
		if _, err := os.Stat(abs); err == nil {
			if same != nil && same(abs) {
				n.taken[rel] = true
				return rel, true
			}
			continue
		}
		n.taken[rel] = true
		return rel, false
	}
}

// SameFile 判断两个文件内容是否完全相同。
func SameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil || sa.Size() != sb.Size() {
		return false
	}
	da, err1 := os.ReadFile(a)
	db, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(da, db)
}

// SameContent 判断磁盘上的文件内容是否等于 data。
func SameContent(data []byte, p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.Size() != int64(len(data)) {
		return false
	}
	b, err := os.ReadFile(p)
	return err == nil && bytes.Equal(b, data)
}

// PlaceData 把 data（原图加上时间标记后的内容）写到 dst，保留 src 的修改时间；move 时删除 src。
func PlaceData(src, dst string, data []byte, move bool) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chtimes(dst, st.ModTime(), st.ModTime())
	if move {
		return os.Remove(src)
	}
	return nil
}

// Place 把 src 复制（move=true 时移动）到 dst，保留修改时间。
func Place(src, dst string, move bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if move {
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
		// 跨磁盘时 Rename 会失败，退回复制+删除
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if move {
		return os.Remove(src)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}
