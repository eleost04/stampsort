// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package jpegenc 是 Go 标准库 image/jpeg 编码器的分叉（BSD 许可，见 LICENSE），
// 增加了 4:4:4 色度不抽样（保住水印文字边缘）和 JFIF 分辨率（DPI）写入。
package jpegenc

// 以下取自 image/jpeg/reader.go。
const (
	sof0Marker = 0xc0 // Start Of Frame (Baseline Sequential).
	dhtMarker  = 0xc4 // Define Huffman Table.
	dqtMarker  = 0xdb // Define Quantization Table.
	app0Marker = 0xe0
)

// unzig maps from the zig-zag ordering to the natural ordering. For example,
// unzig[3] is the column and row of the fourth element in zig-zag order. The
// value is 16, which means first column (16%8 == 0) and third row (16/8 == 2).
var unzig = [blockSize]int{
	0, 1, 8, 16, 9, 2, 3, 10,
	17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34,
	27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36,
	29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46,
	53, 60, 61, 54, 47, 55, 62, 63,
}
