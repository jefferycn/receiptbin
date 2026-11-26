package main

// PDF 小票导入：读 Costco 官网导出的 In-Warehouse Receipt PDF，
// 也读扫描/拍照存成的图片 PDF。
//
// 文本层优先：调 pdftotext（poppler-utils）提文本，导出格式与照片格式
// 两种解析器都试，按打分选优（官网导出走导出格式；iPhone 扫描的 PDF
// 自带 OCR 文本层，内容是纸质小票格式，走照片格式）。
// 文本层没有（纯图片 PDF）或两路都解析得不够好时，用 pdftoppm
// （同属 poppler-utils）把 PDF 渲染成图片再 OCR。
//
// 隐私：导出的 PDF 里含会员号。提文本后立刻去掉会员号相关内容，
// 解析、日志、raw_text、错误信息里都不会出现会员号。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// pdfTimeout 是单次 pdftotext 的上限：纯提文本很快，60 秒绰绰有余。
const pdfTimeout = 60 * time.Second

// pdftotextBin 是 pdftotext 可执行文件名（PATH 里找；容器内由 apk 装 poppler-utils 提供）。
// 用变量而不用常量，方便测试时换成不存在的路径验证报错。
var pdftotextBin = "pdftotext"

// pdftoppmBin 是 pdftoppm 可执行文件名（同属 poppler-utils，图片 PDF 渲染用）。
var pdftoppmBin = "pdftoppm"

// errNoPDFText 是 PDF 文本层太少的哨兵错误：调用方据此改走渲染 + OCR，
// 而不是直接报错让用户换照片。
var errNoPDFText = errors.New("这个 PDF 里没有可读的文字（可能是扫描件），请上传小票照片")

// pdfRenderMaxPages 是图片 PDF 最多渲染的页数：Costco 小票 1–2 页，
// 5 页封顶，避免一个几十页的 PDF 把 OCR 拖垮。
const pdfRenderMaxPages = 5

// isPDFFile 检查文件头是不是 PDF（%PDF，避免扩展名欺骗）。
func isPDFFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := f.Read(head); err != nil {
		return false
	}
	return bytes.Equal(head, []byte{'%', 'P', 'D', 'F'})
}

// ExtractPDFText 用 pdftotext -layout 提 PDF 文本，并去掉会员号。
// 文本层太少时返回 errNoPDFText（调用方改走渲染 + OCR）；
// pdftotext 缺失、超时、执行失败时返回明确的错误。
func ExtractPDFText(pdfPath string) (string, error) {
	if _, err := exec.LookPath(pdftotextBin); err != nil {
		return "", fmt.Errorf("服务器未安装 pdftotext，无法读取 PDF，请上传小票照片")
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, pdftotextBin, "-layout", pdfPath, "-")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("读取 PDF 超时，请重试或上传小票照片")
		}
		return "", fmt.Errorf("读取 PDF 失败，请确认文件是 Costco 导出的小票 PDF，或上传小票照片")
	}
	text := stripMemberNumbers(out.String())
	if len(strings.TrimSpace(text)) < 20 {
		return "", errNoPDFText
	}
	return text, nil
}

// ParsePDFReceipt 解析一个 PDF 小票，返回解析结果、解析用的文本、
// 以及是否需要货号纠错（OCR 路径需要，导出格式不需要）。
//
// 顺序：文本层导出格式 → 文本层照片格式 → 渲染成图再 OCR。
// 每路结果都按解析打分选优；文本层结果足够好时不渲染，省时间。
// 渲染出来的 OCR 文本也走双 PSM（6 先、3 后），与照片导入同一逻辑。
func ParsePDFReceipt(pdfPath string) (*ParsedReceipt, string, bool, error) {
	var best *ParsedReceipt
	var bestText string
	var bestSnap bool
	consider := func(p *ParsedReceipt, text string, snap bool) {
		if p == nil {
			return
		}
		if best == nil || parseScore(p) > parseScore(best) {
			best, bestText, bestSnap = p, text, snap
		}
	}

	text, err := ExtractPDFText(pdfPath)
	if err != nil && !errors.Is(err, errNoPDFText) {
		return nil, "", false, err
	}
	if err == nil {
		// 文本层：两种格式都试（官网导出 vs 扫描件自带的 OCR 文本层）。
		if p, err := ParseExportReceiptText(text); err == nil {
			consider(p, text, false)
		}
		if p, err := ParseReceiptText(text); err == nil {
			consider(p, text, true)
		}
		if best != nil && parseGoodEnough(best) {
			return best, bestText, bestSnap, nil
		}
	}
	// 文本层没有（纯图片 PDF）或解析得不够好：渲染成图再 OCR。
	dir, pages, err := renderPDFPages(pdfPath, pdfRenderMaxPages)
	if err != nil {
		if best != nil {
			return best, bestText, bestSnap, nil
		}
		return nil, "", false, err
	}
	defer os.RemoveAll(dir)
	ocrText6, err := ocrPages(pages, "6")
	if err == nil {
		if p, err := ParseReceiptText(ocrText6); err == nil {
			consider(p, ocrText6, true)
		}
	}
	if best == nil || !parseGoodEnough(best) {
		if ocrText3, err := ocrPages(pages, "3"); err == nil {
			if p, err := ParseReceiptText(ocrText3); err == nil {
				consider(p, ocrText3, true)
			}
		}
	}
	if best == nil {
		return nil, "", false, fmt.Errorf("这个 PDF 读不出小票内容，请确认拍的是 Costco 小票，或上传小票照片")
	}
	return best, bestText, bestSnap, nil
}

// renderPDFPages 用 pdftoppm 把 PDF 前 maxPages 页渲染成 JPEG（宽度封顶 2000px），
// 返回临时目录与按页排序的文件列表。调用方负责删除目录。
func renderPDFPages(pdfPath string, maxPages int) (string, []string, error) {
	if _, err := exec.LookPath(pdftoppmBin); err != nil {
		return "", nil, fmt.Errorf("服务器未安装 pdftoppm，无法读取图片 PDF，请上传小票照片")
	}
	dir, err := os.MkdirTemp("", "receiptbin-pdf-*")
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfTimeout)
	defer cancel()
	prefix := filepath.Join(dir, "page")
	// 宽度封顶 2000px（高度按比例）、输出 JPEG：iPhone 存的图片 PDF
	// 页面能有 45 英寸高，按 300dpi 渲染 PNG 是 4300×13500 的巨图，
	// 光渲染就超 60 秒（反代先 504）；封顶 + JPEG 后约 2 秒。
	// 2000px 宽对小票 OCR 足够。
	cmd := exec.CommandContext(ctx, pdftoppmBin, "-jpeg",
		"-scale-to-x", "2000", "-scale-to-y", "-1",
		"-f", "1", "-l", fmt.Sprintf("%d", maxPages), pdfPath, prefix)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		if ctx.Err() == context.DeadlineExceeded {
			return "", nil, fmt.Errorf("渲染 PDF 超时，请重试或上传小票照片")
		}
		return "", nil, fmt.Errorf("渲染 PDF 失败：%s", strings.TrimSpace(errBuf.String()))
	}
	pages, err := filepath.Glob(prefix + "-*.jpg")
	if err != nil || len(pages) == 0 {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("这个 PDF 里没有可读的页面，请上传小票照片")
	}
	sort.Strings(pages)
	return dir, pages, nil
}

// ocrPages 对一组页面图逐页 OCR（指定 PSM），返回拼接后的文本。
// 单页 OCR 失败时跳过该页（另一页可能还读得出）；全部失败才报错。
func ocrPages(pages []string, psm string) (string, error) {
	var parts []string
	for _, pg := range pages {
		text, err := OCRReceiptWithPSM(pg, psm)
		if err != nil {
			continue
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("OCR 没有读出文字")
	}
	return strings.Join(parts, "\n"), nil
}

// memberLineRe 匹配独立成行的 12 位数字：Costco 会员号是 12 位。
// 交易流水号是 23 位、货号是 3–7 位，都不会误伤。
var memberLineRe = regexp.MustCompile(`^\d{12}$`)

// memberInlineRe 匹配行内独立的 12 位数字（防 "Member 123456789012" 这种同一行写法）。
var memberInlineRe = regexp.MustCompile(`\b\d{12}\b`)

// stripMemberNumbers 去掉文本里的会员号：
//   - "Member" 行本身；
//   - "Member" 下一行如果是纯数字（会员号在下一行）；
//   - 独立成行的 12 位数字；
//   - 行内独立的 12 位数字。
//
// 处理后的文本可安全进解析、日志与 raw_text 存储。
func stripMemberNumbers(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.EqualFold(t, "Member") {
			continue
		}
		if i > 0 && strings.EqualFold(strings.TrimSpace(lines[i-1]), "Member") && isAllDigits(t) {
			continue
		}
		if memberLineRe.MatchString(t) {
			continue
		}
		line = memberInlineRe.ReplaceAllString(line, "")
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// isAllDigits 判断字符串是否全是数字（且非空）。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
