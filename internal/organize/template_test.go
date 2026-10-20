package organize

import (
	"testing"
	"time"

	"github.com/eleost04/stampsort/internal/stamp"
)

func TestRenderTemplate(t *testing.T) {
	r := stamp.Result{Time: time.Date(2026, 10, 9, 8, 21, 0, 0, time.Local), HasClock: true, Source: stamp.FromOCR}
	for tpl, want := range map[string]string{
		DefaultSortTemplate:   "分类/2026/10/09",
		`{年}\{月}月\原图\{日}`:     "2026/10月/原图/09",
		" /{年}/{月}月/打印/{日}/ ": "2026/10月/打印/09",
		"{日期}":                "2026-10-09",
		"待打印":                 "待打印",
		"":                    "",
	} {
		if got := Render(tpl, r); got != want {
			t.Errorf("Render(%q) = %q，期望 %q", tpl, got, want)
		}
	}
	none := stamp.Result{Source: stamp.None}
	for tpl, want := range map[string]string{DefaultSortTemplate: "分类/未识别", `{年}\{月}月\原图\{日}`: "原图/未识别", "照片/{日期}": "照片/未识别", "{年}/{月}": "未识别"} {
		if got := Render(tpl, none); got != want {
			t.Errorf("未识别 Render(%q) = %q，期望 %q", tpl, got, want)
		}
	}
}

func TestValidTemplate(t *testing.T) {
	for _, ok := range []string{DefaultSortTemplate, `{年}\{月}月\原图\{日}`, "", "照片 2026"} {
		if err := ValidTemplate(ok); err != nil {
			t.Errorf("%q 应合法: %v", ok, err)
		}
	}
	for _, bad := range []string{"../{年}", `C:\照片\{年}`, "{年}/{周}", "a/./b", "a?b", "a./b"} {
		if ValidTemplate(bad) == nil {
			t.Errorf("%q 应不合法", bad)
		}
	}
}

func TestMonthNoPad(t *testing.T) {
	r := stamp.Result{Time: time.Date(2026, 8, 3, 8, 21, 0, 0, time.Local), HasClock: true, Source: stamp.FromOCR}
	if got := Render(`{年}\{月份}月\原图\{日}`, r); got != "2026/8月/原图/03" {
		t.Errorf("{月份} 不补零: %s", got)
	}
	if got := Render("{年}/{月}", r); got != "2026/08" {
		t.Errorf("{月} 补零: %s", got)
	}
	if _, m := TemplateSplits("{年}/{月份}月"); !m {
		t.Error("{月份} 也算分月")
	}
	if ValidTemplate("{年}/{月份}月/{日}") != nil {
		t.Error("{月份} 应合法")
	}
}

func TestTemplateSplits(t *testing.T) {
	if y, m := TemplateSplits("原图/{日}"); y || m {
		t.Error("只有日：不分年月")
	}
	if y, m := TemplateSplits("{月}月/{日}"); y || !m {
		t.Error("月/日：分月不分年")
	}
	if y, m := TemplateSplits("{日期}"); !y || !m {
		t.Error("日期：都分")
	}
	if s, p, ok := LegacyTemplates("d"); !ok || s != "分类/{日}" || p != "6寸/{日}" {
		t.Errorf("旧设置转换: %s %s", s, p)
	}
}
