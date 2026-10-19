package stamp

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// 分隔符故意放宽：OCR 常把 "." 认成 ":"、","，或者干脆漏掉（2026.10:19、2026,10.19）
	reDate = regexp.MustCompile(`((?:19|20)\d{2})\s*[.\-/年_,:：·]\s*(\d{1,2})\s*[.\-/月_,:：·]\s*(\d{1,2})\s*日?`)
	reTime = regexp.MustCompile(`(\d{1,2})\s*[:：.]\s*(\d{2})(?:\s*[:：.]\s*(\d{2}))?`)
	// 分隔符全丢时只剩一串数字：202610191424 / 20261019142405
	reDigits = regexp.MustCompile(`(?:19|20)\d{2}`)
)

// OCR 常见的形近字误识别，只在原文解析失败时才替换。
var confusables = strings.NewReplacer(
	"O", "0", "o", "0", "D", "0", "Q", "0",
	"l", "1", "I", "1", "|", "1", "!", "1", "i", "1",
	"Z", "2", "z", "2", "S", "5", "s", "5", "B", "8", "g", "9",
	"。", ".", "，", ",", "·", ".", "一", "-", "—", "-",
)

// Parsed 是从一段文字里解析出的日期/时间，缺失的部分 has* 为 false。
type Parsed struct {
	Y, Mo, D, H, Mi, S int
	HasDate, HasClock  bool
}

// ParseText 从 OCR 文本中解析日期和时间。全角数字会先转半角。
func ParseText(s string) Parsed {
	s = toHalfWidth(s)
	p := parse(s)
	if p.HasDate {
		return p
	}
	if p2 := parse(confusables.Replace(s)); p2.HasDate || !p.HasClock {
		p = p2
	}
	if !p.HasDate {
		if p3 := parseDigits(confusables.Replace(s)); p3.HasDate {
			return p3
		}
	}
	return p
}

// parseDigits 兜底：从年份开始只取数字，按 年4 月2 日2 时2 分2 [秒2] 的位置拆。
// 只在数字足够多（≥12 位）时使用，避免把别的数字串误当成日期。
func parseDigits(s string) Parsed {
	loc := reDigits.FindStringIndex(s)
	if loc == nil {
		return Parsed{}
	}
	var d []byte
	for _, c := range []byte(s[loc[0]:]) {
		if c >= '0' && c <= '9' {
			d = append(d, c)
		}
	}
	if len(d) < 12 {
		return Parsed{}
	}
	n := func(i, j int) int { return atoi(string(d[i:j])) }
	p := Parsed{Y: n(0, 4), Mo: n(4, 6), D: n(6, 8), H: n(8, 10), Mi: n(10, 12)}
	if len(d) >= 14 && n(12, 14) < 60 {
		p.S = n(12, 14)
	}
	if !validDate(p.Y, p.Mo, p.D) || p.H > 23 || p.Mi > 59 {
		return Parsed{}
	}
	p.HasDate, p.HasClock = true, true
	return p
}

func parse(s string) Parsed {
	var p Parsed
	rest := s
	if m := reDate.FindStringSubmatchIndex(s); m != nil {
		y, mo, d := atoi(s[m[2]:m[3]]), atoi(s[m[4]:m[5]]), atoi(s[m[6]:m[7]])
		if validDate(y, mo, d) {
			p.Y, p.Mo, p.D, p.HasDate = y, mo, d, true
			rest = s[m[1]:] // 时间在日期之后找，避免把日期里的数字当成时间
		}
	}
	// 取第一个合法的时间（日期无效时 rest 里还有日期数字，比如 "26.02" 要跳过）
	for _, m := range reTime.FindAllStringSubmatch(rest, -1) {
		h, mi, sec := atoi(m[1]), atoi(m[2]), 0
		if m[3] != "" {
			sec = atoi(m[3])
		}
		if h < 24 && mi < 60 && sec < 60 {
			p.H, p.Mi, p.S, p.HasClock = h, mi, sec, true
			break
		}
	}
	return p
}

func validDate(y, mo, d int) bool {
	if y < 1990 || y > 2100 || mo < 1 || mo > 12 || d < 1 {
		return false
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	return t.Day() == d
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func toHalfWidth(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		if r == '　' {
			return ' '
		}
		return r
	}, s)
}
