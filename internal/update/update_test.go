package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		l, c string
		want bool
	}{
		{"v0.4.0", "v0.3.0", true},
		{"v0.4.0", "v0.4.0", false},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.3.9", "v0.4.0", false},
		{"v0.4.1", "v0.4.0-2-gabcdef", true},
		{"v0.4.0", "dev", false}, // 开发版不提示更新
		{"garbage", "v0.1.0", false},
	} {
		if got := Newer(tc.l, tc.c); got != tc.want {
			t.Errorf("Newer(%s, %s) = %v", tc.l, tc.c, got)
		}
	}
}

// fakeServer 同时模拟 openlist 游客接口和 GitHub Release 接口。
type fakeServer struct {
	files       map[string][]byte // 文件名 -> 内容
	openlistOff bool
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("POST /api/fs/get", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Path string }
		json.NewDecoder(r.Body).Decode(&body)
		name := strings.TrimPrefix(body.Path, "/stampsort/")
		if f.openlistOff || f.files[name] == nil {
			json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "object not found"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{"raw_url": base + "/raw/" + name + "?sign=x"}})
	})
	mux.HandleFunc("GET /raw/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Write(f.files[r.PathValue("name")])
	})
	mux.HandleFunc("GET /repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		var assets []map[string]string
		for n := range f.files {
			assets = append(assets, map[string]string{"name": n, "browser_download_url": base + "/raw/" + n})
		}
		json.NewEncoder(w).Encode(map[string]any{"assets": assets})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if base == "" {
			base = "http://" + r.Host
		}
		mux.ServeHTTP(w, r)
	})
}

func release(t *testing.T, priv ed25519.PrivateKey, installer []byte) (*Manifest, []byte) {
	sum := sha256.Sum256(installer)
	m := &Manifest{Version: "v0.9.0", Notes: "测试", Installer: Asset{Name: "stampsort-setup-v0.9.0.exe", Size: int64(len(installer)), SHA256: hex.EncodeToString(sum[:])}}
	m.Sign(priv)
	b, _ := json.Marshal(m)
	return m, b
}

func TestCheckAndDownload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	installer := bytes.Repeat([]byte("MZ-installer"), 5000)
	_, manifest := release(t, priv, installer)
	fs := &fakeServer{files: map[string][]byte{"latest.json": manifest, "stampsort-setup-v0.9.0.exe": installer}}
	ts := httptest.NewServer(fs.handler(t))
	defer ts.Close()
	ol := Openlist{Base: ts.URL, Dir: "/stampsort"}
	gh := GitHub{Repo: "o/r", API: ts.URL}

	f, err := Check(context.Background(), []Source{ol, gh}, pub)
	if err != nil || f.Source.Name() != "openlist" || !Newer(f.Manifest.Version, "v0.4.0") {
		t.Fatalf("检查失败: %v %+v", err, f)
	}
	var last int64
	p, err := Download(context.Background(), f, []Source{gh}, t.TempDir(), func(d, _ int64) { last = d })
	if err != nil || last != int64(len(installer)) {
		t.Fatalf("下载失败: %v", err)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, installer) {
		t.Error("内容不一致")
	}

	// openlist 挂了 → 自动用 GitHub
	fs.openlistOff = true
	f, err = Check(context.Background(), []Source{ol, gh}, pub)
	if err != nil || f.Source.Name() != "github" {
		t.Fatalf("应回退到 GitHub: %v", err)
	}
}

func TestRejectTampered(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	installer := []byte("MZ-good-installer")
	m, manifest := release(t, priv, installer)

	// 1. 安装包被替换（清单没变）→ sha256 不一致
	fs := &fakeServer{files: map[string][]byte{"latest.json": manifest, m.Installer.Name: []byte("MZ-evil-installer")}}
	ts := httptest.NewServer(fs.handler(t))
	defer ts.Close()
	src := Openlist{Base: ts.URL, Dir: "/stampsort"}
	f, err := Check(context.Background(), []Source{src}, pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Download(context.Background(), f, nil, t.TempDir(), nil); err == nil {
		t.Error("被替换的安装包应被拒绝")
	}

	// 2. 清单被改（换了 sha256 指向恶意安装包）→ 签名不对
	evil := *m
	evil.Installer.SHA256 = strings.Repeat("0", 64)
	b, _ := json.Marshal(evil)
	fs.files["latest.json"] = b
	if _, err := Check(context.Background(), []Source{src}, pub); err == nil {
		t.Error("被改过的清单应被拒绝")
	}

	// 3. 用别的私钥签的清单
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	_, b = release(t, other, installer)
	fs.files["latest.json"] = b
	if _, err := Check(context.Background(), []Source{src}, pub); err == nil {
		t.Error("别的密钥签的清单应被拒绝")
	}
}

func TestPublicKeyValid(t *testing.T) {
	if len(PublicKey()) != ed25519.PublicKeySize {
		t.Fatal("内置公钥长度不对")
	}
}
