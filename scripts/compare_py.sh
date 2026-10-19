#!/bin/sh
# 回归对比：用旧脚本 photo6c.py 生成参考图（PNG，无损），再跑 Go 的同一算法比较 PSNR。
#   scripts/compare_py.sh <photo6c.py 所在目录> <照片>
# 需要 docker；两边都在容器里跑，不改动宿主机环境。
set -eu
PYDIR=$(cd "$1" && pwd)
IMG=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=$(mktemp -d)

docker run --rm -v "$PYDIR":/py:ro -v "$(dirname "$IMG")":/img:ro -v "$OUT":/out python:3.12-slim sh -c '
  pip install -q --root-user-action=ignore pillow numpy >/dev/null 2>&1
  cd /py && python -I -c "
import sys; sys.path.insert(0, \"/py\")
from PIL import Image, ImageOps
import photo6c
im = ImageOps.exif_transpose(Image.open(\"/img/'"$(basename "$IMG")"'\")).convert(\"RGB\")
im.save(\"/out/src.png\")
img, info = photo6c.make_c(im, 300, 0.5, None)
img.save(\"/out/ref_c.png\")
photo6c.add_bleed(img, 2.5).save(\"/out/ref_bleed.png\")
print(\"python:\", info[\"crop_top\"], round(info[\"squash\"]*100, 2), info[\"wm\"])
"'

docker run --rm -v "$ROOT":/src -v ss-gomod:/go/pkg/mod -v ss-gocache:/root/.cache/go-build \
  -v "$(dirname "$IMG")":/img:ro -v "$OUT":/out -e GOFLAGS=-buildvcs=false -w /src golang:1.27-bookworm \
  env STAMPSORT_SAMPLE="/img/$(basename "$IMG")" STAMPSORT_REF=/out go test -run TestCompareWithPython -v ./internal/print6/
rm -rf "$OUT"
