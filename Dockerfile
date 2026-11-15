# ReceiptBin：多阶段构建，静态 Go 二进制 + alpine + tesseract。
# 构建阶段
FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /receiptbin .

# 运行阶段：alpine（约 7MB）+ tesseract OCR（英文数据）+ poppler（pdftotext）。
# 不用 scratch：群晖老版本 Docker 在 FROM scratch 之后会报 unexpected EOF
# 导致构建失败；alpine 还自带 shell，方便排障。
# 二进制是纯静态的（CGO_ENABLED=0），不需要任何运行时依赖；
# 外部依赖只有两个 CLI：tesseract（照片 OCR）与 pdftotext（PDF 导小票提文本）。
FROM alpine:3.21
ENV DATA_DIR=/data
RUN apk add --no-cache tesseract-ocr tesseract-ocr-data-eng poppler
COPY --from=build /receiptbin /receiptbin
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/receiptbin"]
