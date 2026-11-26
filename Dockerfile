# ReceiptBin：多阶段构建，静态 Go 二进制 + alpine + tesseract。
# 构建阶段
FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
# BuildKit 缓存挂载（模块缓存 + 编译缓存，留在构建器里不进镜像）：
# 改代码后重建只编改动部分，不再每次全量编译 modernc.org/sqlite。
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /receiptbin .

# 运行阶段：alpine（约 7MB）+ tesseract OCR（英文数据）+ poppler-utils（pdftotext/pdftoppm）。
# 不用 scratch：群晖老版本 Docker 在 FROM scratch 之后会报 unexpected EOF
# 导致构建失败；alpine 还自带 shell，方便排障。
# 二进制是纯静态的（CGO_ENABLED=0），不需要任何运行时依赖；
# 外部依赖只有三个 CLI：tesseract（照片 OCR）、pdftotext（PDF 提文本）、
# pdftoppm（图片 PDF 渲染成图再 OCR，与 pdftotext 同属 poppler-utils）。
FROM alpine:3.21
ENV DATA_DIR=/data
RUN apk add --no-cache tesseract-ocr tesseract-ocr-data-eng poppler-utils
COPY --from=build /receiptbin /receiptbin
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/receiptbin"]
