package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/eleost04/stampsort/internal/organize"
	"github.com/eleost04/stampsort/internal/stamp"
)

// Settings 是界面上的选项，保存在用户配置目录里，下次打开沿用。
//
// 新增字段一律让零值等于默认值（比如用 NoAutoUpdate 而不是 AutoUpdate），
// 这样旧版本留下的 settings.json 不用迁移就能直接读。
type Settings struct {
	Mode   string  `json:"mode"`   // all | sort | print
	Layout string  `json:"layout"` // c | crop
	PDF    bool    `json:"pdf"`
	Bleed  float64 `json:"bleed"` // 0 表示不生成出血版
	OutDir string  `json:"outDir"`

	// 导出
	NewFolder bool `json:"newFolder"` // true：每次新建「日期 时间 导出」文件夹；false：直接导出到 OutDir
	// 目录模板（见 organize.Render）：原图、6 寸、「待打印」PDF 各放哪里。空 = 默认。
	SortTemplate  string `json:"sortTemplate"`
	PrintTemplate string `json:"printTemplate"`
	PDFTemplate   string `json:"pdfTemplate"`
	DirFormat     string `json:"dirFormat,omitempty"` // 旧版的"按什么分文件夹"，读取时转换成模板
	NameFormat    string `json:"nameFormat"`          // 文件名，见 organize.Name*
	PrintFlat     bool   `json:"printFlat"`           // 只做 6 寸时不按日期分目录
	NoMark        bool   `json:"noMark"`              // 不把拍摄时间写进导出的照片
	Report        bool   `json:"report"`              // 保存导出记录（CSV），默认不保存

	// 6 寸
	DPI     int `json:"dpi"`     // 0 = 300
	Quality int `json:"quality"` // 0 = 95

	// 识别
	Workers int `json:"workers"` // 0 = CPU 核数

	// 更新
	NoAutoUpdate bool      `json:"noAutoUpdate"`
	UpdateSource string    `json:"updateSource"` // auto | openlist | github
	LastCheck    time.Time `json:"lastCheck"`

	// Hints 是学到的水印位置（landscape / portrait），下次启动直接用。
	Hints map[string]stamp.Hint `json:"hints,omitempty"`
}

func defaultSettings() Settings {
	return Settings{Mode: "all", Layout: "c", PDF: true, OutDir: filepath.Join(picturesDir(), "stampsort"),
		SortTemplate: organize.DefaultSortTemplate, PrintTemplate: organize.DefaultPrintTemplate, PDFTemplate: organize.DefaultPDFTemplate,
		NameFormat: organize.NameDateTime, DPI: 300, Quality: 95, UpdateSource: "auto"}
}

type settingsStore struct {
	path string
	mu   sync.Mutex
	cur  Settings
}

func configDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "stampsort")
	}
	return filepath.Join(os.TempDir(), "stampsort")
}

func loadSettings(path string) *settingsStore {
	st := &settingsStore{path: path, cur: defaultSettings()}
	if data, err := os.ReadFile(path); err == nil {
		var s Settings
		if json.Unmarshal(data, &s) == nil {
			st.cur = s.sanitize()
		}
	}
	return st
}

func (s Settings) sanitize() Settings {
	d := defaultSettings()
	if s.Mode != "all" && s.Mode != "sort" && s.Mode != "print" {
		s.Mode = d.Mode
	}
	if s.Layout != "c" && s.Layout != "crop" {
		s.Layout = d.Layout
	}
	if s.Bleed < 0 || s.Bleed > 10 {
		s.Bleed = 0
	}
	if s.OutDir == "" {
		s.OutDir = d.OutDir
	}
	if s.SortTemplate == "" && s.PrintTemplate == "" { // 旧设置：按原来选的层级换成模板
		if st, pt, ok := organize.LegacyTemplates(s.DirFormat); ok {
			s.SortTemplate, s.PrintTemplate = st, pt
		}
	}
	s.DirFormat = ""
	fix := func(t *string, def string) {
		*t = organize.CleanTemplate(*t)
		if *t == "" || organize.ValidTemplate(*t) != nil {
			*t = def
		}
	}
	fix(&s.SortTemplate, d.SortTemplate)
	fix(&s.PrintTemplate, d.PrintTemplate)
	fix(&s.PDFTemplate, d.PDFTemplate)
	if !organize.ValidNameFormat(s.NameFormat) {
		s.NameFormat = d.NameFormat
	}
	if s.DPI < 150 || s.DPI > 600 {
		s.DPI = d.DPI
	}
	if s.Quality < 70 || s.Quality > 100 {
		s.Quality = d.Quality
	}
	if s.Workers < 0 || s.Workers > 32 {
		s.Workers = 0
	}
	if s.UpdateSource != "openlist" && s.UpdateSource != "github" {
		s.UpdateSource = "auto"
	}
	return s
}

func (st *settingsStore) get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur
}

// update 合并部分字段（JSON 里出现的才改）并保存。界面不能改 hints 和 lastCheck。
func (st *settingsStore) update(patch []byte) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.cur
	if err := json.Unmarshal(patch, &next); err != nil {
		return st.cur, err
	}
	next.Hints, next.LastCheck = st.cur.Hints, st.cur.LastCheck
	for name, t := range map[string]string{"原图": next.SortTemplate, "6 寸": next.PrintTemplate, "待打印 PDF": next.PDFTemplate} {
		if err := organize.ValidTemplate(t); err != nil { // 模板写错了直接告诉用户，不要悄悄改回默认
			return st.cur, fmt.Errorf("%s的文件夹模板：%v", name, err)
		}
	}
	st.cur = next.sanitize()
	st.saveLocked()
	return st.cur, nil
}

// setHint 记录学到的水印位置并保存。
func (st *settingsStore) setHint(portrait bool, h stamp.Hint) {
	st.mu.Lock()
	defer st.mu.Unlock()
	hints := map[string]stamp.Hint{}
	for k, v := range st.cur.Hints {
		hints[k] = v
	}
	hints[hintKey(portrait)] = h
	st.cur.Hints = hints
	st.saveLocked()
}

func (st *settingsStore) clearHints() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.cur.Hints = nil
	st.saveLocked()
}

func (st *settingsStore) setLastCheck(t time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.cur.LastCheck = t
	st.saveLocked()
}

func hintKey(portrait bool) string {
	if portrait {
		return "portrait"
	}
	return "landscape"
}

func (st *settingsStore) saveLocked() {
	if st.path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(st.path), 0o755)
	data, _ := json.MarshalIndent(st.cur, "", "  ")
	tmp := st.path + ".tmp" // 先写临时文件再改名，断电也不会写坏
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, st.path)
	}
}
