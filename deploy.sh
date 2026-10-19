#!/bin/sh
# 一键部署：首次运行生成随机密码写入 .env，准备数据目录，然后构建并启动容器。
set -eu
cd "$(dirname "$0")"

if [ ! -f .env ]; then
  cp .env.example .env
  pw=$(head -c 18 /dev/urandom | base64 | tr -d '/+=' | head -c 20)
  sed -i "s/^STAMPSORT_PASSWORD=.*/STAMPSORT_PASSWORD=$pw/" .env
  echo "已生成 .env，访问密码: $pw"
fi

. ./.env
data=${STAMPSORT_DATA_DIR:-./data}
mkdir -p "$data"
chown 65532:65532 "$data" 2>/dev/null || chmod 777 "$data"

docker compose up -d --build
echo "已启动: http://${STAMPSORT_BIND:-127.0.0.1}:${STAMPSORT_PORT:-8930}  （密码见 .env）"
