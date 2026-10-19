package photo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Mark 是 stampsort 写进导出照片里的拍摄时间。再次加入这些照片时直接读它，不用重新识别，
// 手动改过的时间也能保留。
type Mark struct {
	Time     time.Time
	HasClock bool
	Source   string // 时间最初的来源：ocr / exif / filename / manual
}

const (
	xmpHeader = "http://ns.adobe.com/xap/1.0/\x00"
	markNS    = "https://github.com/eleost04/stampsort/ns/1.0/"
)

var reMark = regexp.MustCompile(`stampsort:Time="([^"]+)"(?:[^>]*?stampsort:Clock="(true|false)")?(?:[^>]*?stampsort:Source="([a-z]+)")?`)

// WithMark 在 JPEG 里写入拍摄时间，返回新的文件内容（像素数据不变）：
//   - 没有 EXIF 时写一个标准 EXIF（拍摄日期 DateTimeOriginal），Windows 文件属性、相册软件都能看到；
//     已有相机 EXIF 时保持原样，不覆盖；
//   - 总是写一段 XMP（含 stampsort 标记），替换掉之前 stampsort 写过的那段。
//
// 不是 JPEG 时返回 ErrNotJPEG。
func WithMark(data []byte, m Mark) ([]byte, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, ErrNotJPEG
	}
	var keep [][]byte // SOI 之后、扫描数据之前的各个段（原样保留）
	hasExif := false
	i := 2
	for {
		if i+4 > len(data) || data[i] != 0xFF {
			return nil, errors.New("JPEG 结构不对")
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // 扫描数据开始：后面整体原样拷贝
			break
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			keep = append(keep, data[i:i+2])
			i += 2
			continue
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		end := i + 2 + n
		if n < 2 || end > len(data) {
			return nil, errors.New("JPEG 段长度不对")
		}
		seg := data[i:end]
		body := data[i+4 : end]
		switch {
		case marker == 0xE1 && bytes.HasPrefix(body, []byte("Exif\x00\x00")):
			hasExif = true
		case marker == 0xE1 && bytes.HasPrefix(body, []byte(xmpHeader)) && bytes.Contains(body, []byte(markNS)):
			i = end // 我们以前写的 XMP：丢掉，下面写新的
			continue
		}
		keep = append(keep, seg)
		i = end
	}

	var out bytes.Buffer
	out.Grow(len(data) + 1024)
	out.Write(data[:2]) // SOI
	// JFIF（APP0）按惯例紧跟 SOI，EXIF/XMP 放在它后面
	start := 0
	if len(keep) > 0 && keep[0][1] == 0xE0 {
		out.Write(keep[0])
		start = 1
	}
	if !hasExif {
		writeSegment(&out, 0xE1, exifFor(m))
	}
	writeSegment(&out, 0xE1, xmpFor(m))
	for _, seg := range keep[start:] {
		out.Write(seg)
	}
	out.Write(data[i:])
	return out.Bytes(), nil
}

// ErrNotJPEG 表示不是 JPEG（PNG 等不写标记）。
var ErrNotJPEG = errors.New("不是 JPEG")

func writeSegment(b *bytes.Buffer, marker byte, body []byte) {
	b.Write([]byte{0xFF, marker, byte((len(body) + 2) >> 8), byte(len(body) + 2)})
	b.Write(body)
}

// exifFor 生成最小的 EXIF：IFD0（Software、DateTime、Exif 指针）+ Exif IFD（DateTimeOriginal、DateTimeDigitized）。
func exifFor(m Mark) []byte {
	ts := m.Time.Format("2006:01:02 15:04:05") + "\x00" // 20 字节
	soft := "stampsort\x00"
	le := binary.LittleEndian
	var t bytes.Buffer
	t.WriteString("II")
	binary.Write(&t, le, uint16(42))
	binary.Write(&t, le, uint32(8)) // IFD0 偏移

	// 布局：IFD0(3 项) 在 8，Exif IFD(2 项) 紧随，之后是字符串数据
	ifd0 := 8
	ifd0Size := 2 + 3*12 + 4
	exifIFD := ifd0 + ifd0Size
	exifSize := 2 + 2*12 + 4
	dataOff := exifIFD + exifSize
	softOff := dataOff
	tsOff := softOff + len(soft)

	entry := func(tag, typ uint16, count, value uint32) {
		binary.Write(&t, le, tag)
		binary.Write(&t, le, typ)
		binary.Write(&t, le, count)
		binary.Write(&t, le, value)
	}
	binary.Write(&t, le, uint16(3))
	entry(0x0131, 2, uint32(len(soft)), uint32(softOff)) // Software
	entry(0x0132, 2, uint32(len(ts)), uint32(tsOff))     // DateTime
	entry(0x8769, 4, 1, uint32(exifIFD))                 // Exif IFD 指针
	binary.Write(&t, le, uint32(0))
	binary.Write(&t, le, uint16(2))
	entry(0x9003, 2, uint32(len(ts)), uint32(tsOff)) // DateTimeOriginal
	entry(0x9004, 2, uint32(len(ts)), uint32(tsOff)) // DateTimeDigitized
	binary.Write(&t, le, uint32(0))
	t.WriteString(soft)
	t.WriteString(ts)
	return append([]byte("Exif\x00\x00"), t.Bytes()...)
}

func xmpFor(m Mark) []byte {
	ts := m.Time.Format("2006-01-02T15:04:05")
	if !m.HasClock {
		ts = m.Time.Format("2006-01-02")
	}
	src := m.Source
	if src == "" {
		src = "ocr"
	}
	x := fmt.Sprintf(`<?xpacket begin="%s" id="W5M0MpCehiHzreSzNTczkc9d"?>`+
		`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`+
		`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/" xmlns:stampsort="%s"`+
		` xmp:CreateDate="%s" photoshop:DateCreated="%s" stampsort:Time="%s" stampsort:Clock="%t" stampsort:Source="%s"/>`+
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`, "\ufeff", markNS, ts, ts, ts, m.HasClock, src)
	return append([]byte(xmpHeader), x...)
}

// parseMark 在 APP1 XMP 段里找 stampsort 标记。
func parseMark(body []byte) (Mark, bool) {
	sm := reMark.FindSubmatch(body)
	if sm == nil {
		return Mark{}, false
	}
	v := string(sm[1])
	m := Mark{HasClock: string(sm[2]) != "false" && strings.Contains(v, "T"), Source: string(sm[3])}
	var err error
	if m.HasClock {
		m.Time, err = time.ParseInLocation("2006-01-02T15:04:05", v, time.Local)
	} else {
		m.Time, err = time.ParseInLocation("2006-01-02", v[:min(len(v), 10)], time.Local)
	}
	if err != nil || m.Time.Year() < 1990 {
		return Mark{}, false
	}
	return m, true
}
