package photo

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"
)

func plainJPEG(t *testing.T) []byte {
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 24)), nil)
	return b.Bytes()
}

func TestMarkRoundTrip(t *testing.T) {
	src := plainJPEG(t)
	tm := time.Date(2026, 10, 19, 8, 21, 0, 0, time.Local)
	out, err := WithMark(src, Mark{Time: tm, HasClock: true, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	// 像素不变：仍能解码，尺寸一致
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 32 {
		t.Fatalf("写入后解码失败: %v", err)
	}
	m := ParseExif(out)
	if m.Mark == nil || !m.Mark.Time.Equal(tm) || !m.Mark.HasClock || m.Mark.Source != "manual" {
		t.Fatalf("标记读不回来: %+v", m.Mark)
	}
	if !m.Taken.Equal(tm) {
		t.Errorf("EXIF 拍摄日期不对: %v", m.Taken)
	}

	// 再写一次：替换旧标记，不会叠加多段
	tm2 := time.Date(2025, 1, 2, 0, 0, 0, 0, time.Local)
	out2, _ := WithMark(out, Mark{Time: tm2, HasClock: false, Source: "ocr"})
	if bytes.Count(out2, []byte(markNS)) != 1 {
		t.Error("重复写入后应只有一段 stampsort XMP")
	}
	m2 := ParseExif(out2)
	if m2.Mark == nil || !m2.Mark.Time.Equal(tm2) || m2.Mark.HasClock {
		t.Errorf("第二次标记不对: %+v", m2.Mark)
	}
	if !m2.Taken.Equal(tm) {
		t.Error("已有 EXIF 时不应覆盖（相机原始信息要保留）")
	}
}

func TestMarkNotJPEG(t *testing.T) {
	if _, err := WithMark([]byte("\x89PNG\r\n"), Mark{Time: time.Now()}); err != ErrNotJPEG {
		t.Errorf("PNG 应返回 ErrNotJPEG: %v", err)
	}
}
