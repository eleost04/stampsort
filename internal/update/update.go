// Package update 实现自动更新：检查新版本、下载安装包并校验签名。
//
// 发布的 latest.json 用 ed25519 签名（私钥只在发布机上），公钥编进程序。下载的安装包
// 必须与签过名的清单里的大小、sha256 一致才会运行，镜像源被篡改也装不进来。
//
// 更新来源：
//   - openlist：游客调用 /api/fs/get 拿到带签名的下载地址。地址在构建时用
//     -ldflags "-X .../internal/update.OpenlistURL=https://..." 注入，源码里不写死。
//   - GitHub Releases：latest.json 作为 Release 附件。
package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 发布签名公钥（私钥不在仓库里）。
const publicKeyB64 = "d3wHNtNBkhAc4U/+f+CGTljQD/6Y6BLuYeXpPeqKp5Y="

// OpenlistURL 是 openlist 站点地址，构建时注入；为空表示不用 openlist 源。
var OpenlistURL = ""

// OpenlistDir 是 latest.json 和安装包在 openlist 游客视角下的目录。
var OpenlistDir = "/stampsort"

// GitHubRepo 是发布 Release 的仓库。
var GitHubRepo = "eleost04/stampsort"

// ManifestName 是清单文件名。
const ManifestName = "latest.json"

// Asset 是一个可下载文件。
type Asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest 是 latest.json 的内容。
type Manifest struct {
	Version   string `json:"version"`
	Published string `json:"published"`
	Notes     string `json:"notes"`
	Installer Asset  `json:"installer"`
	Portable  *Asset `json:"portable,omitempty"`
	Sig       string `json:"sig,omitempty"`
}

// payload 是签名的内容：去掉 sig 后的 JSON（字段顺序固定）。
func (m Manifest) payload() []byte {
	m.Sig = ""
	b, _ := json.Marshal(m)
	return b
}

// Sign 用私钥签名（发布工具用）。
func (m *Manifest) Sign(priv ed25519.PrivateKey) {
	m.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, m.payload()))
}

// Verify 校验签名。
func (m *Manifest) Verify(pub ed25519.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(m.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, m.payload(), sig) {
		return errors.New("更新清单签名不对")
	}
	if !strings.HasSuffix(strings.ToLower(m.Installer.Name), ".exe") || m.Installer.Size <= 0 || len(m.Installer.SHA256) != 64 {
		return errors.New("更新清单内容不完整")
	}
	return nil
}

// PublicKey 返回编进程序的公钥。
func PublicKey() ed25519.PublicKey {
	b, _ := base64.StdEncoding.DecodeString(publicKeyB64)
	return ed25519.PublicKey(b)
}

// Newer 判断 latest 是否比 current 新。版本形如 v1.2.3；current 不是正式版本（如 dev）时不提示更新。
func Newer(latest, current string) bool {
	l, ok1 := parseVersion(latest)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 { // v0.4.0-3-gabc 这种 git describe 结果按 0.4.0 算
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ------------------------------------------------------------------ 来源

// Source 是一个更新来源。
type Source interface {
	Name() string
	// Open 下载来源目录里的一个文件。
	Open(ctx context.Context, name string) (io.ReadCloser, error)
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

func get(ctx context.Context, url string, header map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "stampsort-updater")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// Openlist 通过游客接口下载 openlist 上的文件。
type Openlist struct {
	Base string // https://pan.example.com
	Dir  string // 游客视角下的目录，如 /stampsort
}

func (o Openlist) Name() string { return "openlist" }

func (o Openlist) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	body, _ := json.Marshal(map[string]string{"path": strings.TrimRight(o.Dir, "/") + "/" + name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.Base, "/")+"/api/fs/get", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "stampsort-updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Code    int
		Message string
		Data    struct {
			RawURL string `json:"raw_url"`
			Sign   string `json:"sign"`
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("openlist 返回格式不对: %w", err)
	}
	if r.Code != 200 || r.Data.RawURL == "" {
		return nil, fmt.Errorf("openlist: %d %s", r.Code, r.Message)
	}
	dl, err := get(ctx, r.Data.RawURL, nil)
	if err != nil {
		return nil, err
	}
	return dl.Body, nil
}

// GitHub 从最新 Release 的附件下载。
type GitHub struct {
	Repo string
	API  string // 默认 https://api.github.com，测试时可替换
}

func (g GitHub) Name() string { return "github" }

func (g GitHub) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	api := g.API
	if api == "" {
		api = "https://api.github.com"
	}
	resp, err := get(ctx, api+"/repos/"+g.Repo+"/releases/latest", map[string]string{"Accept": "application/vnd.github+json"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	for _, a := range rel.Assets {
		if a.Name == name {
			dl, err := get(ctx, a.URL, nil)
			if err != nil {
				return nil, err
			}
			return dl.Body, nil
		}
	}
	return nil, fmt.Errorf("GitHub Release 里没有 %s", name)
}

// Sources 按偏好返回来源列表：auto = openlist 优先、GitHub 兜底。
// 环境变量 STAMPSORT_UPDATE_OPENLIST 可以换成自己的 openlist（私有镜像、测试用）。
func Sources(pref string) []Source {
	var ol, gh []Source
	base := OpenlistURL
	if v := os.Getenv("STAMPSORT_UPDATE_OPENLIST"); v != "" {
		base = v
	}
	if base != "" {
		ol = []Source{Openlist{Base: base, Dir: OpenlistDir}}
	}
	if GitHubRepo != "" {
		gh = []Source{GitHub{Repo: GitHubRepo}}
	}
	switch pref {
	case "openlist":
		return append(ol, gh...)
	case "github":
		return append(gh, ol...)
	}
	return append(ol, gh...)
}

// ------------------------------------------------------------------ 检查与下载

// Found 是一次检查的结果。
type Found struct {
	Manifest *Manifest
	Source   Source
}

// Check 依次尝试各来源，返回第一个签名有效的清单。
func Check(ctx context.Context, sources []Source, pub ed25519.PublicKey) (*Found, error) {
	if len(sources) == 0 {
		return nil, errors.New("没有配置更新来源")
	}
	var errs []string
	for _, src := range sources {
		m, err := fetchManifest(ctx, src, pub)
		if err != nil {
			errs = append(errs, src.Name()+": "+err.Error())
			continue
		}
		return &Found{Manifest: m, Source: src}, nil
	}
	return nil, errors.New(strings.Join(errs, "；"))
}

func fetchManifest(ctx context.Context, src Source, pub ed25519.PublicKey) (*Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rc, err := src.Open(ctx, ManifestName)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("清单格式不对: %w", err)
	}
	if err := m.Verify(pub); err != nil {
		return nil, err
	}
	return &m, nil
}

// Download 把安装包下载到 dir 并校验大小和 sha256；先用找到清单的来源，失败再换其它来源。
// progress 可为 nil。
func Download(ctx context.Context, f *Found, others []Source, dir string, progress func(done, total int64)) (string, error) {
	a := f.Manifest.Installer
	name := filepath.Base(a.Name)
	dst := filepath.Join(dir, name)
	srcs := append([]Source{f.Source}, others...)
	var errs []string
	tried := map[string]bool{}
	for _, src := range srcs {
		if tried[src.Name()] {
			continue
		}
		tried[src.Name()] = true
		if err := downloadOne(ctx, src, a, dst, progress); err != nil {
			errs = append(errs, src.Name()+": "+err.Error())
			os.Remove(dst)
			continue
		}
		return dst, nil
	}
	return "", errors.New(strings.Join(errs, "；"))
}

func downloadOne(ctx context.Context, src Source, a Asset, dst string, progress func(done, total int64)) error {
	rc, err := src.Open(ctx, a.Name)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > a.Size {
				f.Close()
				return errors.New("下载的文件比清单里的大")
			}
			if _, werr := f.Write(buf[:n]); werr != nil { // 磁盘满等：不能让截断的文件通过校验
				f.Close()
				return werr
			}
			h.Write(buf[:n])
			if progress != nil {
				progress(done, a.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if done != a.Size {
		return fmt.Errorf("下载不完整（%d / %d 字节）", done, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return errors.New("安装包校验失败（sha256 不一致）")
	}
	return nil
}
