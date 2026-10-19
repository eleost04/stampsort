package stamp

import (
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

var (
	// 2026_08_13_06_20_13、IMG_20260813_062013、Screenshot_2026-08-13-06-20-13、20260813062013
	reNameFull = regexp.MustCompile(`(?:^|\D)((?:19|20)\d{2})[-_.]?(\d{2})[-_.]?(\d{2})[-_. T]?(\d{2})[-_.:]?(\d{2})(?:[-_.:]?(\d{2}))?(?:\D|$)`)
	// IMG-20260813-WA0001、2026-08-13
	reNameDate = regexp.MustCompile(`(?:^|\D)((?:19|20)\d{2})[-_.]?(\d{2})[-_.]?(\d{2})(?:\D|$)`)
	// mmexport1692000000000、wx_camera_1692000000000（毫秒时间戳）
	reNameMillis = regexp.MustCompile(`(?:^|\D)(1\d{12})(?:\D|$)`)
)

// FromFilename 从文件名里找拍摄时间。
func FromFilename(name string) (time.Time, bool, bool) {
	base := filepath.Base(name)
	if m := reNameFull.FindStringSubmatch(base); m != nil {
		y, mo, d, h, mi := atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4]), atoi(m[5])
		s := 0
		if m[6] != "" {
			s = atoi(m[6])
		}
		if validDate(y, mo, d) && h < 24 && mi < 60 && s < 60 {
			return time.Date(y, time.Month(mo), d, h, mi, s, 0, time.Local), true, true
		}
	}
	if m := reNameDate.FindStringSubmatch(base); m != nil {
		y, mo, d := atoi(m[1]), atoi(m[2]), atoi(m[3])
		if validDate(y, mo, d) {
			return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.Local), false, true
		}
	}
	if m := reNameMillis.FindStringSubmatch(base); m != nil {
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		t := time.UnixMilli(ms).In(time.Local)
		if t.Year() >= 2000 && t.Year() <= 2100 {
			return t, true, true
		}
	}
	return time.Time{}, false, false
}
