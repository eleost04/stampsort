# 常用命令：
#   make model ort   下载 OCR 模型和本机 onnxruntime（只需一次）
#   make test        单元测试
#   make build       本机构建 -> ./stampsort（需要 cgo）
#   make windows     交叉编译 Windows 桌面版 -> dist/stampsort-windows-amd64.zip（需要 x86_64-w64-mingw32-gcc、cabextract）
#   make installer   Windows 安装程序（需要 makensis）
#   make manifest    签名的更新清单 dist/latest.json
#   make linux       Linux 包 -> dist/stampsort-linux-<arch>.tar.gz
#   make docker      构建镜像
#   make compare     与旧 photo6c.py 做回归对比（需要 docker）
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# 自动更新用的 openlist 地址不写进仓库：放在本地 release.env（已 gitignore）或环境变量里
-include release.env
STAMPSORT_OPENLIST ?=
LDFLAGS := -s -w -X main.version=$(VERSION) -X github.com/eleost04/stampsort/internal/update.OpenlistURL=$(STAMPSORT_OPENLIST)
GOARCH  ?= $(shell go env GOARCH)
ORT_LINUX := $(if $(filter arm64,$(GOARCH)),linux-aarch64,linux-x64)

.PHONY: model ort build test windows installer manifest linux docker compare icon clean

# 去掉 v 前缀和 git describe 后缀：v0.4.0-3-gabc -> 0.4.0（Windows 版本号只认数字）
VERNUM := $(firstword $(subst -, ,$(patsubst v%,%,$(VERSION))))

model: models/rec.onnx
models/rec.onnx:
	go run ./tools/fetch model

ort: lib/libonnxruntime.so
lib/libonnxruntime.so:
	go run ./tools/fetch ort $(ORT_LINUX) lib

build: model
	CGO_ENABLED=1 go build -trimpath -ldflags="$(LDFLAGS)" -o stampsort ./cmd/stampsort

test: model
	go vet ./...
	go test ./...

# Windows：GUI 子系统（双击打开界面，不弹黑窗）+ 图标/清单/版本信息资源
WINRES := go run github.com/tc-hib/go-winres@v0.3.3
windows: model
	rm -rf dist/win && mkdir -p dist/win
	$(WINRES) make --in winres/winres.json --out cmd/stampsort/rsrc --arch amd64 \
		--product-version $(VERNUM).0 --file-version $(VERNUM).0 || \
		$(WINRES) make --in winres/winres.json --out cmd/stampsort/rsrc --arch amd64
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
		go build -trimpath -ldflags="$(LDFLAGS) -H windowsgui" -o dist/win/stampsort.exe ./cmd/stampsort
	rm -f cmd/stampsort/rsrc_windows_*.syso
	go run ./tools/fetch ort win-x64 dist/win
	go run ./tools/fetch vcredist dist/vc_redist.x64.exe
	scripts/vcrt.sh dist/vc_redist.x64.exe dist/win
	cp README.md dist/win/
	cd dist/win && rm -f ../stampsort-windows-amd64.zip && go run ../../tools/pack ../stampsort-windows-amd64.zip .

# 安装程序（NSIS，按用户安装、不需要管理员）-> dist/stampsort-setup-<版本>.exe
installer: windows
	makensis -V2 -DVERSION=$(VERNUM) -DSRC=../dist/win -DOUTFILE=../dist/stampsort-setup-$(VERSION).exe installer/stampsort.nsi

# 生成并签名更新清单 dist/latest.json（私钥只在发布机上）
RELEASE_KEY ?= $(HOME)/.config/stampsort-release/ed25519.key
manifest:
	go run ./tools/release -version $(VERSION) -key $(RELEASE_KEY) -notes RELEASE_NOTES.md \
		-installer dist/stampsort-setup-$(VERSION).exe -portable dist/stampsort-windows-amd64.zip -out dist/latest.json

linux: model
	rm -rf dist/linux && mkdir -p dist/linux
	CGO_ENABLED=1 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/linux/stampsort ./cmd/stampsort
	go run ./tools/fetch ort $(ORT_LINUX) dist/linux
	cp README.md dist/linux/
	tar -C dist/linux -czf dist/stampsort-linux-$(GOARCH).tar.gz .

docker:
	docker build --build-arg VERSION=$(VERSION) -t stampsort:latest .

compare:
	scripts/compare_py.sh $(PY_DIR) $(PHOTO)

icon:
	go run ./tools/icon internal/web/static 192 512
	go run ./tools/icon winres 16 32 48 256
	go run ./tools/icon winres/icon.ico 16 32 48 256
	cp internal/web/static/icon-192.png internal/desktop/ui/icon.png

clean:
	rm -rf dist stampsort
