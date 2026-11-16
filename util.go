package main

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxUploadSize = 20 << 20 // 小票照片上限 20MB
	sessionDays   = 30       // 会话有效期 30 天
	// adjustmentDays 是 Costco 调价窗口：购买后 30 天内降价可退差价。
	adjustmentDays = 30
)

// nowStr 返回 UTC 的 RFC3339Nano 时间字符串；同一精度下可直接按字符串排序。
func nowStr() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randBase62 生成 n 位随机 base62 字符串（crypto/rand），用于小票 id。
func randBase62(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(base62))))
		if err != nil {
			panic(err) // 随机源不可用是致命错误，不应继续
		}
		b.WriteByte(base62[v.Int64()])
	}
	return b.String()
}

// randHex 生成 n 字节随机数的 hex 字符串，用于 session id。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// validUsername：小写字母/数字/-/下划线，1–32 位，以字母或数字开头。
func validUsername(s string) bool { return usernameRe.MatchString(s) }

// validPassword：最短 8 位。不做复杂度要求，自建小工具够用。
func validPassword(s string) bool { return len(s) >= 8 }

// truncateRunes 按 rune 截断字符串，超长加省略号。
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// ---------- 金额：全程整数分，避免浮点误差 ----------

// thousandsRe 匹配带千分位逗号的金额（1,008.43 / 12,345.67）。
var thousandsRe = regexp.MustCompile(`\d{1,3}(?:,\d{3})+\.\d{2}`)

// stripThousandsCommas 去掉金额里的千分位逗号（"1,008.43" → "1008.43"）。
// 小数点逗号（"12,49"）不匹配这个正则，不动，由后续逻辑按小数点处理。
func stripThousandsCommas(s string) string {
	return thousandsRe.ReplaceAllStringFunc(s, func(m string) string {
		return strings.ReplaceAll(m, ",", "")
	})
}

// parseMoneyToCents 把 "12.49" / "12,49" / "$12.49" / "1,008.43" 转成分；失败返回 ok=false。
func parseMoneyToCents(s string) (cents int, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = stripThousandsCommas(s)
	s = strings.ReplaceAll(s, ",", ".")
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	parts := strings.SplitN(s, ".", 2)
	if len(parts) == 1 {
		parts = append(parts, "00")
	}
	if len(parts[1]) == 1 {
		parts[1] += "0"
	}
	if len(parts[1]) > 2 {
		return 0, false
	}
	dollars, err1 := strconv.Atoi(parts[0])
	frac, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || dollars < 0 || frac < 0 {
		return 0, false
	}
	cents = dollars*100 + frac
	if neg {
		cents = -cents
	}
	return cents, true
}

// formatCents 把分格式化成 "$12.49"（负数 "-$5.00"）。
func formatCents(cents int) string {
	if cents < 0 {
		return "-$" + formatCents(-cents)[1:]
	}
	return "$" + strconv.Itoa(cents/100) + "." + pad2(cents%100)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// ---------- 日期：小票时间存为 "2006-01-02 15:04"（收据本地时间，无时区） ----------

const receiptTimeLayout = "2006-01-02 15:04"

// addDaysToReceiptDate 给小票日期加 n 天，返回 "2006-01-02"。
func addDaysToReceiptDate(purchasedAt string, days int) string {
	t, err := time.Parse(receiptTimeLayout, purchasedAt)
	if err != nil {
		// 只有日期没有时间的旧数据也能处理
		t, err = time.Parse("2006-01-02", purchasedAt)
		if err != nil {
			return ""
		}
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

// daysUntil 返回从今天（本地）到 date（"2006-01-02"）还剩几天，负数表示已过。
func daysUntil(date string) int {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	return int(t.Sub(today).Hours() / 24)
}

// todayLocal 返回今天本地日期 "2006-01-02"。
func todayLocal() string { return time.Now().Format("2006-01-02") }
