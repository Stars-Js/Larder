# syntax=docker/dockerfile:1

# 注意：sqlite-vec 通过 ncruces/go-sqlite3 + wazero 纯 Go 运行，无需 CGO。

# ---------- 构建阶段 ----------
FROM golang:1.27-alpine AS builder

WORKDIR /src

# 先复制依赖描述文件，命中缓存
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags "-s -w" -o /out/larder .

# ---------- 运行阶段 ----------
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /out/larder /app/larder

# 数据目录：把 -v 挂到这个目录即可持久化知识库（每个库为 <id>.db）。
ENV LARDER_DATA_DIR=/data
VOLUME ["/data"]

USER nonroot:nonroot
EXPOSE 8765
ENTRYPOINT ["/app/larder"]
# 容器内以纯后台服务运行（不打开窗口/浏览器）。
CMD ["-addr", "0.0.0.0:8765", "-no-open"]
