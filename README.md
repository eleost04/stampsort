# stampsort

水印相机照片整理工具：**自动识别照片左下角的水印时间，按 年/月/日 分类**；把 4:3 照片**排成 6 寸（4x6 英寸）满版打印文件**，水印不变形、不被裁掉。

- 网页版：手机（安卓/iOS）、电脑浏览器打开即用，上传照片或 zip，下载整理好的 zip；可"添加到主屏幕"当 App 用
- Windows 桌面版：独立窗口，**可以直接从微信拖照片进来**或 Ctrl+V 粘贴；识别后能逐张核对、手动改时间，再一键导出；完全离线
- Linux / Docker：一条命令部署

```
时间 2026.08.13 06:20          ──►  分类/2026/08/13/2026-08-13_06-20.jpg
备注 xx 建设工程有限公司             6寸/2026/08/13/2026-08-13_06-20_6寸.jpg   (1800x1200 @300dpi)
                                    6寸/2026/08/13/待打印_横版.pdf            (每页 6x4 英寸)
                                    report.csv                                 (识别结果清单)
```

## 识别时间

按以下顺序，前一种失败才用后一种：

1. **水印 OCR**（PP-OCRv4 识别模型 + onnxruntime），按以下顺序找"时间"那一行：
   - **记住的水印位置**：同一个水印相机拍的照片，水印位置基本固定，只是背景在变。认出一张后就记下位置，后面的照片直接到这个位置读，每张约 20–40ms，背景是浅色路面、白墙、天空都不受影响。桌面版会把位置保存下来，下次打开直接用。
   - **亮色文字行定位**：在左下区域找白色文字行，逐行识别。
   - **左下角扫描兜底**：上面两种都失败时，按几种字号把左下角切成横条逐条识别。
   - 解析能容忍 OCR 的常见错误：分隔符认错（`2026.10:19`、`2026,10.19`）、分隔符全丢（`202610191424`）、形近字（O→0、l→1）。支持 `2026.08.13 06:20`、`2026-08-13`、`2026/8/13`、`2026年8月13日 06:20:13`；日期和时间分在两行也能拼起来。
2. **EXIF 拍摄时间**：只要照片没被微信等 App 抹掉 EXIF，就能用。
3. **文件名**：例如 `2026_08_13_06_20_13.jpg`、`IMG_20260813_062013.jpg`、`mmexport1786600000000.jpg`。
4. 以上都没有，放进 `未识别/`，保留原文件名。

同一分钟有多张照片时，按输入顺序编号 `_2`、`_3`。重复运行时，内容相同的照片不会被再复制一份。

## 6 寸排版

| 模式 | 做法 | 适合 |
|---|---|---|
| `c`（默认） | 顶部少裁一点，底部水印带保持原比例；剩下的高度差由画面主体承担，竖向轻微压缩（4:3 原图约 7%） | 画面和水印都要完整 |
| `crop` | 完全不变形，只从顶部裁到 3:2，不会裁到水印 | 不接受任何变形 |

- 输出：1800x1200 @300dpi，JPEG 质量 95，色度不抽样（4:4:4），水印文字边缘清晰。
- 竖图输出竖版 4x6。比 3:2 还扁的图左右裁切，但会避开左下角的水印。
- 可选**出血版**（`--bleed 2.5`）：四周镜像外扩并模糊，无边距打印时被打印机啃掉的只是外扩的假边。
- 可选**多页 PDF**：每页正好 6x4 英寸，按日期、横竖版分组，每 30 页一个文件，可以一次打印一批。

方案 C 的算法移植自原来的 `photo6c.py`。在输入像素相同的情况下，与旧脚本的输出逐通道差值 ≤1（见 `scripts/compare_py.sh`）。

### 打印设置（以 Epson L3258 为例）

文档尺寸 10x15cm(4x6in) / 横向 / Epson 光泽照片纸 / 质量 高。要满版就勾选"无边距"，并把"扩展"调成"最小"；打印 PDF 时选"实际大小/100%"。

## 使用

### 网页版（服务器部署）

```sh
git clone https://github.com/eleost04/stampsort && cd stampsort
./deploy.sh          # 首次运行会生成随机访问密码写入 .env，然后构建并启动容器
```

- 默认只监听 `127.0.0.1:8930`，需要在 nginx/caddy 里加一条反代并配好 HTTPS。
- 也可以在 `.env` 里把 `STAMPSORT_BIND` 改成 `0.0.0.0`，在局域网里直接访问。
- 在网页上选照片或 zip，选择处理内容和排版方式，上传后能看到逐张进度和识别结果（识别失败的标红），完成后下载结果 zip。
- 任务结果保留 24 小时。容器设置为只读文件系统、去掉所有 capability、非 root 用户、内存上限 512MB。

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `STAMPSORT_PASSWORD` | 必填 | 访问密码 |
| `STAMPSORT_BIND` / `STAMPSORT_PORT` | `127.0.0.1` / `8930` | 宿主机监听地址 |
| `STAMPSORT_MAX_UPLOAD_MB` | `1024` | 单次上传上限 |
| `STAMPSORT_MEM_LIMIT` | `512m` | 容器内存上限 |

### Windows 桌面版

**安装**：下载 `stampsort-setup-<版本>.exe` 双击安装。
- 只装给当前用户（`%LOCALAPPDATA%\Programs\stampsort`），**不需要管理员权限**。
- 安装后在开始菜单、桌面找得到；右键菜单「**发送到 → stampsort**」可以把选中的照片或文件夹直接发给它。
- 卸载：在"设置 → 应用"里卸载。
- 也可以下载绿色版 `stampsort-windows-amd64.zip`，解压后直接运行。注意 exe、`onnxruntime.dll`、4 个 `msvcp140*` / `vcruntime140*` 要放在同一个目录。

> **关于"Windows 已保护你的电脑"（SmartScreen）**：程序没有购买代码签名证书，从网上下载的安装包第一次运行会出现这个提示，点「更多信息 → 仍要运行」即可。安装后的程序和之后的自动更新都不会再提示。

**使用**：
1. **加入照片**，以下几种方式都可以：
   - 从**微信**聊天窗口、资源管理器把照片、文件夹或 zip 拖进窗口；
   - 微信里右键"复制"后，回到窗口按 `Ctrl+V`；
   - 点「添加」选择；
   - 用右键「发送到」。
   - 程序已经开着时，再打开或「发送到」，文件会交给已打开的窗口。
2. **核对**：照片会自动识别时间（多张同时识别，显示速度），按日期分组显示。没识别出来的标红；时间来自 EXIF 或文件名的标黄。点开照片能看大图和 6 寸打印效果，改时间后按 `Enter` 跳到下一张。一批识别完后，会用学到的水印位置把没认出的照片自动再认一遍。
3. **导出**：默认**直接导出到保存位置**，可以选**按什么分文件夹**：

   | 选项 | 例子（保存位置 `D:\资料\10`） |
   |---|---|
   | 年 / 月 / 日 | `D:\资料\10\分类\2026\10\19\2026-10-19_08-21.jpg` |
   | 年 / 月 | `…\分类\2026\10\…` |
   | 月 / 日 | `…\分类\10\19\…` |
   | 日 | `D:\资料\10\分类\19\08-21.jpg`（保存位置已经是某个月的文件夹时用） |
   | 年-月-日（一层） | `…\分类\2026-10-19\…` |

   - 6 寸打印文件在对应的 `6寸\<同样的层级>\` 下，每个日期文件夹一份"待打印"PDF。多次导出到同一个文件夹时，PDF 会自动包含之前导出的照片。
   - 照片跨月份却只按"日"分时，导出前会提醒。
   - 每次导出的清单保存在 `导出记录\`。

**设置**（右上角齿轮）：
- 导出方式（直接导出，或每次新建子文件夹）、分文件夹的层级、文件名（日期_时间 / 只要时间 / 保留原名）；
- 6 寸分辨率、图片质量、出血版；
- 同时识别张数、重新学习水印位置；
- 自动检查更新、更新来源（网盘 / GitHub）；
- 打开日志、拖放诊断。

**自动更新**：启动时检查新版本（每天最多一次，可关闭）。有新版时顶部会出现提示，点「立即更新」后自动下载、校验签名并安装，装完自动重新打开。

补充说明：
- 界面用的是系统自带的 Microsoft Edge WebView2（Win10/11 一般都已安装）。没有装的电脑会改用浏览器打开同一个界面。
- 设置和日志在 `%APPDATA%\stampsort\`。
- **从微信拖入没反应时**：点右上角的「拖放诊断」，把里面的记录发给开发者。
- 在 cmd 或 PowerShell 里带子命令运行（如 `stampsort all D:\照片`）仍然是命令行版。因为 exe 是窗口程序，提示符会先返回；写脚本时用 `start /wait stampsort all ...`。

### 命令行

```
stampsort all   照片/目录/zip... [-o 输出目录]   分类 + 6 寸（默认生成 PDF）
stampsort sort  照片/目录/zip... [--move]        只分类（默认复制，原图不动）
stampsort print 照片/目录/zip... [--pdf] [--bleed 2.5] [--mode crop]
stampsort web   [--addr 127.0.0.1:8930] [--password xxx]
```

- 路径和参数可以混写。
- 不写 `-o` 时，输出放在第一个输入旁边：`<名字>_整理`、`_分类` 或 `_6寸`。
- 方案 C 的细调参数和旧脚本一致：`--crop-ratio 0.5`（多余高度里靠裁顶消化的比例）、`--crop-top N`。
- 其它参数：`--dpi`、`--quality`、`--chunk`、`--no-jpg`、`-j`（并行数）、`--no-ocr`。

## 构建

需要 Go 1.27+ 和 C 编译器（onnxruntime 通过 cgo 加载）。

```sh
make model ort     # 下载 OCR 模型（编进程序）和本机 onnxruntime 动态库，固定版本并校验 sha256
make test          # 单元测试
make build         # -> ./stampsort，运行时 libonnxruntime.so 放在同目录或 lib/ 下
make windows       # 交叉编译 Windows 桌面版（需要 x86_64-w64-mingw32-gcc；图标/清单由 go-winres 生成）
make docker        # 构建镜像
make installer     # Windows 安装程序（需要 makensis）
```

### 发布（维护者）

自动更新的清单 `latest.json` 用 ed25519 签名。私钥只放在发布机的 `~/.config/stampsort-release/ed25519.key`，程序里只有公钥。下载的安装包必须和签过名的清单里的大小、sha256 一致才会安装。

1. 在 `RELEASE_NOTES.md` 写好 `## vX.Y.Z` 的更新说明。
2. 构建并签名：
   ```sh
   make installer VERSION=vX.Y.Z && make manifest VERSION=vX.Y.Z
   ```
3. 把 `dist/` 里的安装包、zip、`latest.json` 上传到更新源（openlist 目录，或 GitHub Release 附件）。
4. 验证：`go run ./tools/release -check -source github`（或 `-source openlist -openlist <地址>`）。

openlist 更新源的地址不写进仓库：放在本地的 `release.env`（已在 .gitignore 里），内容为 `STAMPSORT_OPENLIST=https://…`，构建时注入。不配置时只用 GitHub 更新。

| 部分 | 大小 |
|---|---|
| `stampsort` 程序（含 10.8MB 模型；Windows 版含桌面界面） | ~19MB / Windows 20.4MB |
| onnxruntime | Linux 28MB / Windows 16MB |
| Docker 镜像 | ~110MB（解压后；distroless 基础镜像 + 程序 + onnxruntime） |

没有 onnxruntime 也能运行，只是跳过水印 OCR，只用 EXIF 和文件名识别时间。

### 目录结构

```
cmd/stampsort      命令行入口
internal/stamp     时间识别：水印行定位、OCR、文本/文件名解析
internal/print6    6 寸排版：方案 C、纯裁切、出血、PDF
internal/imgx      图像基础操作（与 Pillow 逐像素一致的 Lanczos）
internal/jpegenc   标准库 JPEG 编码器的分叉，增加 4:4:4 和 DPI
internal/job       命令行和网页共用的处理流程
internal/web       网页服务 + 前端（原生 HTML/JS，go:embed）
internal/desktop   桌面版：本机服务 + WebView2 窗口 + 界面（拖放/粘贴/核对/导出/设置）+ Windows 原生功能
internal/update    自动更新：检查、下载、ed25519 签名校验
installer/         NSIS 安装程序脚本
tools/             下载依赖、打包、生成图标的小工具
```

## 致谢

- OCR 模型：[PaddleOCR](https://github.com/PaddlePaddle/PaddleOCR) PP-OCRv4，ONNX 版本取自 [RapidOCR](https://github.com/RapidAI/RapidOCR)（Apache-2.0）
- 推理：[onnxruntime](https://github.com/microsoft/onnxruntime)（MIT），通过 [onnxruntime_go](https://github.com/yalue/onnxruntime_go) 调用（MIT）
- `internal/jpegenc` 分叉自 Go 标准库（BSD-3-Clause，见该目录下的 LICENSE）

## 许可

MIT
