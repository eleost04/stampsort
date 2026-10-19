# 多阶段构建：编译阶段下载模型和 onnxruntime（固定版本 + sha256 校验），运行阶段只有程序 + 动态库。
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY tools/fetch tools/fetch
ARG TARGETARCH
RUN go run ./tools/fetch model && \
    case "$TARGETARCH" in arm64) p=linux-aarch64 ;; *) p=linux-x64 ;; esac && \
    go run ./tools/fetch ort "$p" /out
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/stampsort ./cmd/stampsort
# onnxruntime 初始化时会 popen("... blkid ...") 探测设备，distroless 没有 /bin/sh 时 popen 返回 NULL，
# ORT 不检查就使用，导致段错误。这里放一个只返回 127（命令不存在）的静态桩，不引入真正的 shell。
RUN printf 'int main(void){return 127;}\n' | gcc -static -Os -s -x c - -o /sh-stub

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=build /sh-stub /bin/sh
COPY --from=build /out/ /app/
ENV STAMPSORT_ADDR=0.0.0.0:8930 STAMPSORT_DATA=/data TZ=Asia/Shanghai
EXPOSE 8930
VOLUME /data
HEALTHCHECK --interval=60s --timeout=5s CMD ["/app/stampsort", "healthcheck"]
ENTRYPOINT ["/app/stampsort", "web"]
