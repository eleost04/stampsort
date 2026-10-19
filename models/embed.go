// Package models 把 OCR 识别模型编进程序，发布时只需一个可执行文件 + onnxruntime 动态库。
// 模型文件不进 git，用 `make model`（go run ./tools/fetch model）下载。
package models

import _ "embed"

// Rec 是 PP-OCRv4 中文识别模型（来自 RapidOCR 1.4.4），字典在模型元数据 "character" 里。
//
//go:embed rec.onnx
var Rec []byte
