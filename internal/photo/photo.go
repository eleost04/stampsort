// Package photo 负责读图：解码 JPEG/PNG、读取 EXIF 拍摄时间和方向，并把图摆正。
package photo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"time"

	"github.com/eleost04/stampsort/internal/imgx"
)

// Exts 是支持的图片扩展名（小写）。
var Exts = []string{".jpg", ".jpeg", ".png"}

// IsImage 按扩展名判断。
func IsImage(name string) bool {
	n := strings.ToLower(name)
	for _, e := range Exts {
		if strings.HasSuffix(n, e) {
			return true
		}
	}
	return false
}

// Meta 是从 EXIF 读到的信息。
type Meta struct {
	Taken       time.Time // 零值表示没有
	Orientation int
}

// Load 读图并按 EXIF 方向摆正（等价于 ImageOps.exif_transpose）。
func Load(path string) (*image.RGBA, Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Meta{}, err
	}
	meta := ParseExif(data)
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, meta, fmt.Errorf("解码失败: %w", err)
	}
	return imgx.Orient(imgx.ToRGBA(img), meta.Orientation), meta, nil
}

// ParseExif 从 JPEG 的 APP1 段读取 DateTimeOriginal（没有则用 DateTime）和 Orientation。
// 不是 JPEG 或没有 EXIF 时返回零值。
func ParseExif(data []byte) Meta {
	var m Meta
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return m
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return m
		}
		marker := data[i+1]
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // 开始扫描数据，后面不会再有 EXIF
			return m
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		end := i + 2 + n
		if n < 2 || end > len(data) {
			return m
		}
		seg := data[i+4 : end]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			parseTIFF(seg[6:], &m)
			return m
		}
		i = end
	}
	return m
}

const (
	tagOrientation      = 0x0112
	tagDateTime         = 0x0132
	tagExifIFD          = 0x8769
	tagDateTimeOriginal = 0x9003
)

func parseTIFF(t []byte, m *Meta) {
	if len(t) < 8 {
		return
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return
	}
	var dateTime string
	ifd0 := readIFD(t, bo, int(bo.Uint32(t[4:])))
	if v, ok := ifd0[tagOrientation]; ok {
		m.Orientation = int(v.short)
	}
	if v, ok := ifd0[tagDateTime]; ok {
		dateTime = v.str
	}
	if v, ok := ifd0[tagExifIFD]; ok {
		if v2, ok := readIFD(t, bo, int(v.long))[tagDateTimeOriginal]; ok && v2.str != "" {
			dateTime = v2.str
		}
	}
	if tm, err := time.ParseInLocation("2006:01:02 15:04:05", strings.TrimRight(dateTime, "\x00 "), time.Local); err == nil && tm.Year() > 1970 {
		m.Taken = tm
	}
}

type entry struct {
	short uint16
	long  uint32
	str   string
}

// readIFD 只解析我们需要的几种类型：SHORT、LONG、ASCII。
func readIFD(t []byte, bo binary.ByteOrder, off int) map[uint16]entry {
	out := map[uint16]entry{}
	if off <= 0 || off+2 > len(t) {
		return out
	}
	n := int(bo.Uint16(t[off:]))
	for i := 0; i < n; i++ {
		p := off + 2 + 12*i
		if p+12 > len(t) {
			break
		}
		tag, typ, cnt := bo.Uint16(t[p:]), bo.Uint16(t[p+2:]), int(bo.Uint32(t[p+4:]))
		val := t[p+8 : p+12]
		switch typ {
		case 3: // SHORT
			out[tag] = entry{short: bo.Uint16(val)}
		case 4: // LONG
			out[tag] = entry{long: bo.Uint32(val)}
		case 2: // ASCII
			if cnt <= 4 {
				out[tag] = entry{str: string(val[:cnt])}
			} else if o := int(bo.Uint32(val)); o >= 0 && cnt <= len(t) && o <= len(t)-cnt {
				out[tag] = entry{str: string(t[o : o+cnt])}
			}
		}
	}
	return out
}
