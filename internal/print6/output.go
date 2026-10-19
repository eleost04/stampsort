package print6

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"io"
	"os"

	"github.com/eleost04/stampsort/internal/jpegenc"
	"github.com/eleost04/stampsort/internal/photo"
)

// EncodeJPEG 按打印用参数编码：4:4:4 保住文字边缘，写入 DPI。
func EncodeJPEG(w io.Writer, im *image.RGBA, quality, dpi int) error {
	return jpegenc.Encode(w, im, &jpegenc.Options{Quality: quality, No444: true, DPI: dpi})
}

// SaveJPEG 编码并写文件。
func SaveJPEG(path string, im *image.RGBA, quality, dpi int) error {
	return SaveJPEGMark(path, im, quality, dpi, nil)
}

// SaveJPEGMark 编码并写文件；mark 不为 nil 时同时写入拍摄时间（EXIF + stampsort 标记）。
func SaveJPEGMark(path string, im *image.RGBA, quality, dpi int, mark *photo.Mark) error {
	var buf bytes.Buffer
	if err := EncodeJPEG(&buf, im, quality, dpi); err != nil {
		return err
	}
	data := buf.Bytes()
	if mark != nil {
		var err error
		if data, err = photo.WithMark(data, *mark); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// PDFPage 是 PDF 的一页：一张已编码的 JPEG 文件。
type PDFPage struct {
	JPEG string // 文件路径
	W, H int    // 像素
}

// WritePDF 生成多页 PDF：每页尺寸 = 像素 / dpi（6x4 英寸 = 432x288 pt），
// JPEG 原样嵌入（DCTDecode），不重新编码，逐页从磁盘读取，内存只占一页。
func WritePDF(path string, pages []PDFPage, dpi int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	cw := &countWriter{w: bw}
	n := len(pages)
	// 对象编号：1=Catalog 2=Pages，第 i 页：Page=3+3i, Image=4+3i, Content=5+3i
	offsets := make([]int64, 3+3*n)
	obj := func(id int, format string, args ...any) {
		offsets[id] = cw.n
		fmt.Fprintf(cw, "%d 0 obj\n"+format+"\nendobj\n", append([]any{id}, args...)...)
	}
	fmt.Fprint(cw, "%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	kids := new(bytes.Buffer)
	for i := range pages {
		fmt.Fprintf(kids, "%d 0 R ", 3+3*i)
	}
	obj(2, "<< /Type /Pages /Count %d /Kids [ %s] >>", n, kids.String())
	for i, p := range pages {
		pw := float64(p.W) * 72 / float64(dpi)
		ph := float64(p.H) * 72 / float64(dpi)
		page, img, content := 3+3*i, 4+3*i, 5+3*i
		obj(page, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.4g %.4g] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			pw, ph, img, content)
		data, err := os.ReadFile(p.JPEG)
		if err != nil {
			f.Close()
			return err
		}
		offsets[img] = cw.n
		fmt.Fprintf(cw, "%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
			img, p.W, p.H, len(data))
		cw.Write(data)
		fmt.Fprint(cw, "\nendstream\nendobj\n")
		cmd := fmt.Sprintf("q %.4g 0 0 %.4g 0 0 cm /Im0 Do Q", pw, ph)
		obj(content, "<< /Length %d >>\nstream\n%s\nendstream", len(cmd), cmd)
	}
	xref := cw.n
	fmt.Fprintf(cw, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(cw, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(cw, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	if cw.err != nil {
		f.Close()
		return cw.err
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

type countWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.err = err
	return n, err
}
