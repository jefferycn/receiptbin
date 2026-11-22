package main

// 小票 OCR：调 tesseract CLI（运行镜像里装 tesseract-ocr + 英文数据）。
// 实测：原图直接 OCR 错误多（折扣金额错读、TOTAL 行读坏）；
// 先转灰度 + 放大 2 倍再 OCR，示例小票的 12 行商品、
// 5 个足球、全部合计金额都能正确读出（见 parser_test.go 的 OCR 夹具）。

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // 注册 JPEG 解码器（image.Decode 用）
	"image/png"
	"math"
	"os"
	"os/exec"
	"time"
)

// ocrTimeout 是单次 OCR 的上限：tesseract 在大图上可能跑几十秒，
// 超时直接失败，让用户换张更清晰的照片重试。
const ocrTimeout = 120 * time.Second

// tesseractBin 是 tesseract 可执行文件名（PATH 里找；容器内由 apk 提供）。
var tesseractBin = "tesseract"

// ocrPSM 是 tesseract 的页面分段模式：6 = 均匀文本块（v1.0 实测最稳）。
// OCRParseReceiptBest 在 PSM 6 效果不好时会自动用 PSM 3 重跑一遍
// （2026-11-21 实测：带折痕的小票上 3 比 6 读得全，底部大号日期行、
// 折痕处的价格 6 会丢，3 能读出；但另一张小票上 3 只读出 7 件、6 全对，
// 所以两个模式都要试，按解析打分选优，不能只换默认模式）。
var ocrPSM = "6"

// OCRReceipt 对小票图片做 OCR，返回识别出的文本。
// 流程：解码 → 灰度 + 放大 2 倍 → 写临时 PNG → tesseract --psm 6。
// 图片解码失败时（如 iPhone 的 HEIC）退化为直接对原文件跑 tesseract。
func OCRReceipt(imgPath string) (string, error) {
	return OCRReceiptWithPSM(imgPath, ocrPSM)
}

// OCRReceiptWithPSM 与 OCRReceipt 相同，但指定页面分段模式
// （图片 PDF 渲染后逐页 OCR 时，PSM 6 与 PSM 3 各跑一遍）。
func OCRReceiptWithPSM(imgPath, psm string) (string, error) {
	workPath := imgPath
	if pre, err := preprocessReceiptImage(imgPath); err == nil {
		workPath = pre
		defer os.Remove(pre)
	}
	// else: 预处理失败（非常见格式），直接用原图试一次
	return runTesseract(workPath, psm)
}

// runTesseract 在一张图上跑一次 tesseract，返回识别文本。
func runTesseract(workPath, psm string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ocrTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tesseractBin, workPath, "stdout", "--psm", psm)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("OCR 超时（超过 %s），请换张更清晰的照片重试", ocrTimeout)
		}
		return "", fmt.Errorf("OCR 失败：%v（%s）", err, errBuf.String())
	}
	text := out.String()
	if len(text) < 20 {
		return "", fmt.Errorf("OCR 几乎没读出文字，请确认照片拍的是小票、字迹清晰")
	}
	return text, nil
}

// OCRParseReceiptBest 对小票图片做 OCR + 解析，自动选最优结果：
// 先用 PSM 6 跑一遍，解析结果不理想（对账不过、或缺日期/门店）时
// 再用 PSM 3 在同一张预处理图上重跑一遍，按解析打分取高的那份。
// PSM 6 效果好时只跑一次，不增加耗时；效果不好时多花一次 OCR 的时间
// （约几秒），换一张读得全的小票。返回最佳解析结果与对应的 OCR 文本。
func OCRParseReceiptBest(imgPath string) (*ParsedReceipt, string, error) {
	workPath := imgPath
	if pre, err := preprocessReceiptImage(imgPath); err == nil {
		workPath = pre
		defer os.Remove(pre)
	}
	text6, err := runTesseract(workPath, "6")
	if err != nil {
		return nil, "", err
	}
	best, err6 := ParseReceiptText(text6)
	bestText := text6
	if err6 == nil && parseGoodEnough(best) {
		return best, bestText, nil
	}
	text3, err := runTesseract(workPath, "3")
	if err != nil {
		// 第二遍失败：第一遍能用就用第一遍
		if err6 == nil {
			return best, bestText, nil
		}
		return nil, "", err6
	}
	alt, err3 := ParseReceiptText(text3)
	if err3 != nil {
		if err6 == nil {
			return best, bestText, nil
		}
		return nil, "", err6
	}
	if err6 == nil && parseScore(best) >= parseScore(alt) {
		return best, bestText, nil
	}
	return alt, text3, nil
}

// preprocessReceiptImage：解码图片，先转灰度，宽 < 2000 时用 Lanczos-3 放大 2 倍，
// 写临时 PNG 返回路径。调用方负责删除。
//
// 管线选择是实测出来的（同一张小票照片，tesseract 5.3.4）：
//
//	灰度 + Lanczos-3 放大 2 倍 → 17 行全对，对账通过；
//	x/image 的 CatmullRom 放大 → 只对 15 行，丢 2 件商品、1 行折扣。
//
// x/image 没有 Lanczos，所以这里手写了一个可分离的 Lanczos-3（与 PIL 的 LANCZOS 同款核）。
func preprocessReceiptImage(imgPath string) (string, error) {
	f, err := os.Open(imgPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	// 先转灰度（再缩放，与 PIL convert('L') 后 resize 的顺序一致）
	gray := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gray.SetGray(x, y, color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray))
		}
	}
	dst := gray
	if w < 2000 {
		dst = lanczosScaleGray(gray, 2)
	}
	// 注意：不要加白边。2026-11-21 实测 30px 白边让 11/14 小票在 PSM 6 下
	// 从全对变成对账差 $5（丢一行折扣、多算一件），得不偿失。
	tmp, err := os.CreateTemp("", "receiptbin-ocr-*.png")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if err := png.Encode(tmp, dst); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// lanczos3 是 Lanczos 核（a=3，与 PIL LANCZOS 相同）。
func lanczos3(x float64) float64 {
	if x == 0 {
		return 1
	}
	if x < -3 || x > 3 {
		return 0
	}
	px := math.Pi * x
	return math.Sin(px) / px * math.Sin(px/3) / (px / 3)
}

type resampleTap struct {
	idx int
	w   float64
}

// axisTaps 计算一维重采样系数：每个输出位置对应的一组（输入下标，权重），
// 权重已归一化。只用于整数倍放大（scale >= 1），核半径为 3 个输入像素。
func axisTaps(inLen, outLen int) [][]resampleTap {
	scale := float64(outLen) / float64(inLen)
	taps := make([][]resampleTap, outLen)
	for o := 0; o < outLen; o++ {
		center := (float64(o)+0.5)/scale - 0.5
		lo := int(math.Ceil(center - 3))
		hi := int(math.Floor(center + 3))
		var ts []resampleTap
		sum := 0.0
		for i := lo; i <= hi; i++ {
			// 边缘镜像，避免黑边
			ii := i
			if ii < 0 {
				ii = -ii
			}
			if ii >= inLen {
				ii = 2*inLen - 2 - ii
			}
			if ii < 0 || ii >= inLen {
				continue
			}
			w := lanczos3(float64(i) - center)
			if w == 0 {
				continue
			}
			ts = append(ts, resampleTap{idx: ii, w: w})
			sum += w
		}
		for j := range ts {
			ts[j].w /= sum
		}
		taps[o] = ts
	}
	return taps
}

// lanczosScaleGray 用 Lanczos-3 把灰度图放大 scale 倍（可分离：先横后竖）。
func lanczosScaleGray(src *image.Gray, scale int) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w*scale, h*scale
	xtaps := axisTaps(w, ow)
	ytaps := axisTaps(h, oh)
	// 横向：每行输出 ow 个 float64
	tmp := make([]float64, ow*h)
	for y := 0; y < h; y++ {
		for ox := 0; ox < ow; ox++ {
			sum := 0.0
			for _, t := range xtaps[ox] {
				sum += t.w * float64(src.GrayAt(b.Min.X+t.idx, b.Min.Y+y).Y)
			}
			tmp[y*ow+ox] = sum
		}
	}
	// 纵向
	dst := image.NewGray(image.Rect(0, 0, ow, oh))
	for oy := 0; oy < oh; oy++ {
		for ox := 0; ox < ow; ox++ {
			sum := 0.0
			for _, t := range ytaps[oy] {
				sum += t.w * tmp[t.idx*ow+ox]
			}
			if sum < 0 {
				sum = 0
			}
			if sum > 255 {
				sum = 255
			}
			dst.SetGray(ox, oy, color.Gray{Y: uint8(sum + 0.5)})
		}
	}
	return dst
}

// decodeImageInfo 读图片尺寸（确认页展示用，失败返回 0）。
func decodeImageInfo(imgPath string) (w, h int) {
	f, err := os.Open(imgPath)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// extForContentType 由上传的 MIME 决定保存后缀（只允许常见图片格式）。
func extForContentType(ct string) (string, bool) {
	switch ct {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	case "image/heic", "image/heif":
		// HEIC 原样保存；Go 解码不了，OCR 时直接喂给 tesseract 试一次
		return ".heic", true
	}
	return "", false
}

// isJPEG 检查文件头是不是 JPEG（上传校验用，避免扩展名欺骗）。
func isImageFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 12)
	if _, err := f.Read(head); err != nil {
		return false
	}
	if bytes.Equal(head[:3], []byte{0xFF, 0xD8, 0xFF}) {
		return true // JPEG
	}
	if bytes.Equal(head[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		return true // PNG
	}
	if bytes.Equal(head[:4], []byte{'R', 'I', 'F', 'F'}) && bytes.Equal(head[8:12], []byte{'W', 'E', 'B', 'P'}) {
		return true // WebP
	}
	// HEIC: ftyp box，品牌 heic/heix/hevc/mif1
	if bytes.Equal(head[4:8], []byte{'f', 't', 'y', 'p'}) {
		brand := string(head[8:12])
		if brand == "heic" || brand == "heix" || brand == "hevc" || brand == "mif1" || brand == "msf1" {
			return true
		}
	}
	return false
}
