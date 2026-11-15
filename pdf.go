package main

// PDF 小票导入：读 Costco 官网导出的 In-Warehouse Receipt PDF。
// 这类 PDF 自带文本层，不用 OCR：调 pdftotext（poppler-utils）提文本，
// 再用 export_parser.go 的导出格式解析器解析。
//
// 隐私：导出的 PDF 里含会员号。提文本后立刻去掉会员号相关内容，
// 解析、日志、raw_text、错误信息里都不会出现会员号。

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// pdfTimeout 是单次 pdftotext 的上限：纯提文本很快，60 秒绰绰有余。
const pdfTimeout = 60 * time.Second

// pdftotextBin 是 pdftotext 可执行文件名（PATH 里找；容器内由 apk 装 poppler-utils 提供）。
// 用变量而不用常量，方便测试时换成不存在的路径验证报错。
var pdftotextBin = "pdftotext"

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
// pdftotext 缺失、超时、提不出文本时都返回明确的错误，
// 告诉用户改传小票照片（PDF 导入是锦上添花，不能堵死主路）。
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
		return "", fmt.Errorf("这个 PDF 里没有可读的文字（可能是扫描件），请上传小票照片")
	}
	return text, nil
}

// memberLineRe 匹配独立成行的 12 位数字：Costco 会员号是 12 位。
// 交易流水号是 23 位、货号是 4–7 位，都不会误伤。
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
