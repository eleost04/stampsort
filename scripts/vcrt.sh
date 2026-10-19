#!/bin/sh
# 从微软 VC++ 运行库安装包里取出 onnxruntime.dll 依赖的 4 个 DLL，放到 exe 旁边（应用本地部署）。
# 这样没装过 VC++ 运行库的电脑也能加载 OCR。需要 cabextract。
#   scripts/vcrt.sh <VC_redist.x64.exe> <输出目录>
set -eu
exe=$(cd "$(dirname "$1")" && pwd)/$(basename "$1") # 下面会 cd，先转成绝对路径
out=$(cd "$2" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/outer" "$tmp/inner"
# 安装包里嵌着若干 cab，DLL 在其中一个 cab 里再套一层
(cd "$tmp/outer" && cabextract -q "$exe" >/dev/null 2>&1 || true)
for c in "$tmp"/outer/*; do
	(cd "$tmp/inner" && cabextract -q -F '*140*.dll_amd64' "$c" >/dev/null 2>&1 || true)
done
for f in msvcp140.dll msvcp140_1.dll vcruntime140.dll vcruntime140_1.dll; do
	if [ ! -f "$tmp/inner/${f}_amd64" ]; then
		echo "vcrt: 安装包里没有 $f" >&2
		exit 1
	fi
	cp "$tmp/inner/${f}_amd64" "$out/$f"
done
echo "vcrt: 已复制 msvcp140 / vcruntime140 运行库到 $out" >&2
