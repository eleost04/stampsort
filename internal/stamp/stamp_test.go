package stamp

import (
	"testing"
	"time"
)

func TestParseText(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  string // "2006-01-02 15:04:05"，只有日期时时间为 00:00:00
		date  bool
		clock bool
	}{
		{"时间2026.08.1306:20", "2026-08-13 06:20:00", true, true},
		{"时间 2026.08.13 06:20", "2026-08-13 06:20:00", true, true},
		{"2026-08-13 06:20:13", "2026-08-13 06:20:13", true, true},
		{"2026/8/3 9:05", "2026-08-03 09:05:00", true, true},
		{"2026年8月13日 06：20", "2026-08-13 06:20:00", true, true},
		{"拍摄于２０２６年０８月１３日", "2026-08-13 00:00:00", true, false},
		{"2O26.O8.13 O6:2O", "2026-08-13 06:20:00", true, true}, // O 误识别成字母
		{"时间 2026.l2.3l 23:59", "2026-12-31 23:59:00", true, true},
		{"2026.02.30 10:00", "0001-01-01 10:00:00", false, true},  // 无效日期
		{"时间2026.10:1914:24", "2026-10-19 14:24:00", true, true},  // 用户样本：. 认成了 :
		{"时间2026,10.19 08.21", "2026-10-19 08:21:00", true, true}, // , 和 . 混用
		{"时间 202610191424", "2026-10-19 14:24:00", true, true},    // 分隔符全丢
		{"时间2026 10 19 14 24", "2026-10-19 14:24:00", true, true}, // 分隔符变空格
		{"备注 某某建设工程有限公司", "", false, false},
		{"电话 13800138000", "", false, false},
		{"06:20", "0001-01-01 06:20:00", false, true},
	} {
		p := ParseText(tc.in)
		if p.HasDate != tc.date || p.HasClock != tc.clock {
			t.Errorf("%q: date=%v clock=%v，期望 %v %v", tc.in, p.HasDate, p.HasClock, tc.date, tc.clock)
			continue
		}
		if tc.date {
			if got := p.time().Format("2006-01-02 15:04:05"); got != tc.want {
				t.Errorf("%q: 得到 %s，期望 %s", tc.in, got, tc.want)
			}
		}
	}
}

func TestFromFilename(t *testing.T) {
	for _, tc := range []struct {
		name  string
		want  string
		clock bool
		ok    bool
	}{
		{"2026_08_13_06_20_13.jpg", "2026-08-13 06:20:13", true, true},
		{"IMG_20260813_062013.jpg", "2026-08-13 06:20:13", true, true},
		{"Screenshot_2026-08-13-06-20-13.png", "2026-08-13 06:20:13", true, true},
		{"IMG-20260813-WA0001.jpg", "2026-08-13 00:00:00", false, true},
		{"mmexport1786600000000.jpg", time.UnixMilli(1786600000000).Format("2006-01-02 15:04:05"), true, true},
		{"微信图片_20260813062013.jpg", "2026-08-13 06:20:13", true, true},
		{"微信图片_2026-08-13_062013_123.jpg", "2026-08-13 06:20:13", true, true},
		{"wx_camera_1786600000000.jpg", time.UnixMilli(1786600000000).Format("2006-01-02 15:04:05"), true, true},
		{"DSC01234.JPG", "", false, false},
		{"P1234567890123.jpg", "", false, false}, // 13 位数字但不是微信导出的
		{"20261340.jpg", "", false, false},
	} {
		tm, clock, ok := FromFilename(tc.name)
		if ok != tc.ok || clock != tc.clock {
			t.Errorf("%s: ok=%v clock=%v", tc.name, ok, clock)
			continue
		}
		if ok && tm.Format("2006-01-02 15:04:05") != tc.want {
			t.Errorf("%s: 得到 %s，期望 %s", tc.name, tm.Format("2006-01-02 15:04:05"), tc.want)
		}
	}
}

func TestTextLinesSplitsRows(t *testing.T) {
	const w, h = 1000, 750
	gray := make([]uint8, w*h)
	line := func(y0, y1, x1 int) {
		for y := y0; y < y1; y++ {
			for x := 20; x < x1; x++ {
				if x%5 != 0 {
					gray[y*w+x] = 255
				}
			}
		}
	}
	line(600, 620, 300) // 时间行
	line(632, 652, 400) // 备注行，间隔 12px
	lines := TextLines(gray, w, h, 235)
	if len(lines) != 2 {
		t.Fatalf("应拆出 2 行，得到 %v", lines)
	}
	if lines[0].Min.Y > 600 || lines[0].Max.Y > 632 {
		t.Errorf("第一行范围不对: %v", lines[0])
	}
}
