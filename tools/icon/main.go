// icon 生成图标（PNG），结果提交在仓库里，改图标时重新运行：
//
//	go run ./tools/icon internal/web/static 192 512      网页 / PWA
//	go run ./tools/icon winres 16 32 48 256              Windows exe 图标
//	go run ./tools/icon winres/icon.ico 16 32 48 256     安装程序图标（多尺寸 ICO）
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// shape 返回 (x,y)∈[0,1]² 处的颜色；4x4 超采样抗锯齿。
func shape(x, y float64) color.NRGBA {
	blue := color.NRGBA{0x1f, 0x6f, 0xeb, 255}
	white := color.NRGBA{255, 255, 255, 255}
	pale := color.NRGBA{0xd6, 0xe6, 0xff, 255}
	clear := color.NRGBA{}
	if !roundRect(x, y, 0, 0, 1, 1, 0.22) {
		return clear
	}
	// 照片
	if roundRect(x, y, 0.18, 0.22, 0.82, 0.78, 0.06) {
		c := pale
		if math.Hypot(x-0.64, y-0.38) < 0.07 { // 太阳
			c = color.NRGBA{0xff, 0xc1, 0x07, 255}
		}
		if y > 0.42+0.9*math.Abs(x-0.4) || y > 0.52+0.8*math.Abs(x-0.68) { // 山
			c = color.NRGBA{0x5b, 0x9b, 0xff, 255}
		}
		// 左下角时间水印
		if roundRect(x, y, 0.24, 0.66, 0.52, 0.71, 0.02) {
			c = white
		}
		return c
	}
	return blue
}

func roundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return math.Hypot(x-cx, y-cy) <= r
}

func render(n int) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, n, n))
	const ss = 4
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := shape((float64(px)+(float64(sx)+0.5)/ss)/float64(n), (float64(py)+(float64(sy)+0.5)/ss)/float64(n))
					af := float64(c.A) / 255
					r += float64(c.R) * af
					g += float64(c.G) * af
					b += float64(c.B) * af
					a += af
				}
			}
			if a > 0 {
				im.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / ss / ss * 255)})
			}
		}
	}
	return im
}

func main() {
	dir, sizes := ".", []int{192, 512}
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if len(os.Args) > 2 {
		sizes = nil
		for _, a := range os.Args[2:] {
			n, err := strconv.Atoi(a)
			if err != nil {
				panic(err)
			}
			sizes = append(sizes, n)
		}
	}
	if strings.HasSuffix(dir, ".ico") {
		if err := writeICO(dir, sizes); err != nil {
			panic(err)
		}
		return
	}
	for _, n := range sizes {
		f, err := os.Create(filepath.Join(dir, "icon-"+strconv.Itoa(n)+".png"))
		if err != nil {
			panic(err)
		}
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(f, render(n)); err != nil {
			panic(err)
		}
		f.Close()
	}
}

// writeICO 写多尺寸 ICO（每个尺寸内嵌 PNG，Vista 起都支持）。
func writeICO(path string, sizes []int) error {
	var imgs [][]byte
	for _, n := range sizes {
		var b bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&b, render(n)); err != nil {
			return err
		}
		imgs = append(imgs, b.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, n := range sizes {
		dim := uint8(n)
		if n >= 256 {
			dim = 0 // 0 表示 256
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(offset)})
		offset += len(imgs[i])
	}
	for _, b := range imgs {
		out.Write(b)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}
