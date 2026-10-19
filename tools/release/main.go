// release 生成并签名自动更新清单 latest.json。私钥只在发布机上，不进仓库。
//
//	go run ./tools/release -version v0.4.0 -key ~/.config/stampsort-release/ed25519.key \
//	    -notes RELEASE_NOTES.md -installer dist/stampsort-setup-v0.4.0.exe \
//	    -portable dist/stampsort-windows-amd64.zip -out dist/latest.json
//
// 发布后验证（像程序一样检查更新、下载安装包并校验签名和 sha256）：
//
//	go run ./tools/release -check -source openlist -openlist https://pan.example.com
//	go run ./tools/release -check -source github
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eleost04/stampsort/internal/update"
)

func main() {
	version := flag.String("version", "", "版本号，如 v0.4.0")
	keyPath := flag.String("key", "", "ed25519 私钥文件（base64）")
	notesPath := flag.String("notes", "", "更新说明文件（取第一个 ## 段落）")
	installer := flag.String("installer", "", "安装程序路径")
	portable := flag.String("portable", "", "绿色版 zip 路径（可选）")
	out := flag.String("out", "latest.json", "输出路径")
	check := flag.Bool("check", false, "验证已发布的版本，而不是生成清单")
	source := flag.String("source", "auto", "验证哪个来源：openlist | github | auto")
	openlist := flag.String("openlist", "", "openlist 地址（验证 openlist 来源时用）")
	flag.Parse()
	if *check {
		if err := verify(*source, *openlist); err != nil {
			fmt.Fprintln(os.Stderr, "release:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*version, *keyPath, *notesPath, *installer, *portable, *out); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(version, keyPath, notesPath, installer, portable, out string) error {
	if !strings.HasPrefix(version, "v") || strings.Contains(version, "-") {
		return fmt.Errorf("版本号要是正式版本，如 v0.4.0（得到 %q）", version)
	}
	keyB64, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyB64)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("私钥格式不对")
	}
	priv := ed25519.PrivateKey(key)
	if !priv.Public().(ed25519.PublicKey).Equal(update.PublicKey()) {
		return fmt.Errorf("私钥和程序里内置的公钥不匹配")
	}
	m := update.Manifest{Version: version, Published: time.Now().Format(time.RFC3339), Notes: readNotes(notesPath, version)}
	if m.Installer, err = asset(installer); err != nil {
		return err
	}
	if portable != "" {
		a, err := asset(portable)
		if err != nil {
			return err
		}
		m.Portable = &a
	}
	m.Sign(priv)
	if err := m.Verify(update.PublicKey()); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "已生成 %s：%s，安装包 %s（%d 字节）\n", out, version, m.Installer.Name, m.Installer.Size)
	return nil
}

func verify(source, openlist string) error {
	if openlist != "" {
		update.OpenlistURL = openlist
	}
	var srcs []update.Source
	for _, s := range update.Sources(source) {
		if source == "auto" || s.Name() == source {
			srcs = append(srcs, s)
		}
	}
	f, err := update.Check(context.Background(), srcs, update.PublicKey())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s：清单签名有效，版本 %s，安装包 %s（%d 字节）\n", f.Source.Name(), f.Manifest.Version, f.Manifest.Installer.Name, f.Manifest.Installer.Size)
	dir, err := os.MkdirTemp("", "stampsort-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p, err := update.Download(context.Background(), f, nil, dir, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s：安装包下载完成，大小和 sha256 一致（%s）\n", f.Source.Name(), filepath.Base(p))
	return nil
}

func asset(p string) (update.Asset, error) {
	f, err := os.Open(p)
	if err != nil {
		return update.Asset{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return update.Asset{}, err
	}
	return update.Asset{Name: filepath.Base(p), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// readNotes 取 RELEASE_NOTES.md 里 "## <版本>" 下面的内容。
func readNotes(p, version string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var lines []string
	in := false
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "## ") {
			if in {
				break
			}
			in = strings.TrimSpace(strings.TrimPrefix(l, "## ")) == version
			continue
		}
		if in {
			lines = append(lines, l)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
