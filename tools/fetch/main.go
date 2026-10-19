// fetch 下载构建所需的外部文件并校验 sha256，跨平台（Windows CI 上也不依赖 curl/unzip）。
//
//	go run ./tools/fetch model             # -> models/rec.onnx
//	go run ./tools/fetch ort <平台> <目录>  # onnxruntime 动态库，平台见 ortAssets
//	go run ./tools/fetch vcredist <文件>    # 微软 VC++ 运行库安装包（Windows 包里要带它的 DLL）
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const ortVersion = "1.29.0" // 必须与 onnxruntime_go 使用的 C API 头文件版本一致

// RapidOCR 1.4.4 的 wheel 里带有 PP-OCRv4 识别模型，字典存在模型元数据里。
var model = asset{
	url:    "https://files.pythonhosted.org/packages/ba/12/1e5497183bdbe782dbb91bad1d0d2297dba4d2831b2652657f7517bfc6df/rapidocr_onnxruntime-1.4.4-py3-none-any.whl",
	sha256: "971d7d5f223a7a808662229df1ef69893809d8457d834e6373d3854bc1782cbf",
}

const modelMember = "rapidocr_onnxruntime/models/ch_PP-OCRv4_rec_infer.onnx"

type asset struct{ url, sha256 string }

// onnxruntime.dll 依赖 VC++ 2015-2022 运行库。固定版本 14.44.35211，
// 由 scripts/vcrt.sh 从中取出 msvcp140*.dll / vcruntime140*.dll 放到 exe 旁边（微软允许随程序分发）。
var vcredist = asset{
	url:    "https://download.visualstudio.microsoft.com/download/pr/990c720a-46ce-4dc7-87b9-c2eb615d34b9/CC0FF0EB1DC3F5188AE6300FAEF32BF5BEEBA4BDD6E8E445A9184072096B713B/VC_redist.x64.exe",
	sha256: "cc0ff0eb1dc3f5188ae6300faef32bf5beeba4bdd6e8e445a9184072096b713b",
}

var ortAssets = map[string]asset{
	"linux-x64": {ortURL("onnxruntime-linux-x64-" + ortVersion + ".tgz"),
		"c3fddc4f139a045b0c4902c57410f0694f1c2fdf9b6939fbe38b1aeae7cd14ba"},
	"linux-aarch64": {ortURL("onnxruntime-linux-aarch64-" + ortVersion + ".tgz"),
		"e1799098ebc054b370f6176a450f158720f297818c613e5dc99b92e2ec82346f"},
	"win-x64": {ortURL("onnxruntime-win-x64-" + ortVersion + ".zip"),
		"c9b4b7086b529ad814f428c1bad028e20a25d7dc0699836775faace4ab5b78b2"},
	"win-arm64": {ortURL("onnxruntime-win-arm64-" + ortVersion + ".zip"),
		"a094a49c3ced0f9fca554647cc7566ae99d93a63a8ce6bf47975561c2de7608e"},
}

func ortURL(name string) string {
	return "https://github.com/microsoft/onnxruntime/releases/download/v" + ortVersion + "/" + name
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "model":
		data, err := download(model)
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		for _, f := range zr.File {
			if f.Name == modelMember {
				return extractZip(f, filepath.Join("models", "rec.onnx"))
			}
		}
		return fmt.Errorf("wheel 里没有 %s", modelMember)

	case len(args) == 2 && args[0] == "vcredist":
		data, err := download(vcredist)
		if err != nil {
			return err
		}
		return writeFile(args[1], bytes.NewReader(data))

	case len(args) == 3 && args[0] == "ort":
		a, ok := ortAssets[args[1]]
		if !ok {
			return fmt.Errorf("未知平台 %q", args[1])
		}
		data, err := download(a)
		if err != nil {
			return err
		}
		if strings.HasSuffix(a.url, ".zip") {
			return ortFromZip(data, args[2])
		}
		return ortFromTgz(data, args[2])
	}
	return fmt.Errorf("用法: fetch model | fetch ort <%s> <目录> | fetch vcredist <文件>", strings.Join(keys(), "|"))
}

func keys() []string {
	var ks []string
	for k := range ortAssets {
		ks = append(ks, k)
	}
	return ks
}

func download(a asset) ([]byte, error) {
	fmt.Fprintln(os.Stderr, "下载", a.url)
	resp, err := http.Get(a.url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", a.url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != a.sha256 {
		return nil, fmt.Errorf("sha256 不匹配: %s\n  期望 %s\n  实际 %s", a.url, a.sha256, got)
	}
	return data, nil
}

// ortFromTgz 只取 lib/libonnxruntime.so.<版本>，在目录里存成 libonnxruntime.so。
func ortFromTgz(data []byte, dir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("压缩包里没有 libonnxruntime.so")
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == "libonnxruntime.so."+ortVersion {
			return writeFile(filepath.Join(dir, "libonnxruntime.so"), tr)
		}
	}
}

func ortFromZip(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if path.Base(f.Name) == "onnxruntime.dll" {
			return extractZip(f, filepath.Join(dir, "onnxruntime.dll"))
		}
	}
	return fmt.Errorf("压缩包里没有 onnxruntime.dll")
}

func extractZip(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return writeFile(dst, rc)
}

func writeFile(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	fmt.Fprintln(os.Stderr, "写入", dst)
	return f.Close()
}
