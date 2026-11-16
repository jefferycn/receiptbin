package main

// Costco 小票解析：把 OCR 文本（或干净转录）解析成结构化小票。
// 金额一律整数分，避免浮点误差。
//
// Costco 小票格式（示例，见 parser_test.go 的虚构夹具）：
//   E 1000001 KS ALMONDS 15.99          商品行：E=食品免税，A=应税（行尾）
//   1000003 PAPER TOWELS 12PK 21.99 A
//   0000999001 / 1000003 4.00-A          折扣行：紧跟在对应商品行后面
//   SUBTOTAL 243.84 / TAX 14.52 / **** TOTAL 258.36
//   TOTAL NUMBER OF ITEMS SOLD = 16 / INSTANT SAVINGS $ 35.00
//   Fremont #123 / BP 09/12/2026 14:25
//
// OCR 噪声容忍：
//   - 金额小数点常被读成逗号（3,79）或感叹号（5 !00-A），行首常有杂字；
//   - 折扣行的 / 可能被读成 7，货号可能被截断（如 /10000）；
//   - TOTAL 行可能整行读坏，用 AMOUNT: $258.36 兜底，再兜底用小计+税推导。
// 解析结果进确认页人工核对，对账不通过不许入库——解析器不追求 100% 正确，
// 追求“错得明显、改得方便”。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParsedItem 是解析出的一个商品（同一小票内同货号已聚合）。
// NameZh 由商品词典填（uploadHandler 查 products 表），解析器本身不填。
type ParsedItem struct {
	ItemNo         string `json:"item_no"`
	Name           string `json:"name"`
	NameZh         string `json:"name_zh"`
	Qty            int    `json:"qty"`
	UnitPriceCents int    `json:"unit_price_cents"`
	DiscountCents  int    `json:"discount_cents"` // 每件折扣
	NetCents       int    `json:"net_cents"`
	Taxable        bool   `json:"taxable"`
	FoodExempt     bool   `json:"food_exempt"`
	Category       string `json:"category"`
	// IsFee 是押金/费用行（如 CA CRV 押金）：算钱，不算件数。
	// 小票打印的件数不含押金行，对账时跳过它，否则件数总多一件。
	IsFee bool `json:"is_fee"`
}

// ParsedReceipt 是解析出的一张小票。
type ParsedReceipt struct {
	Store         string       `json:"store"`
	StoreNo       string       `json:"store_no"`
	PurchasedAt   string       `json:"purchased_at"`   // "2006-01-02 15:04"
	TerminalNo    string       `json:"terminal_no"`    // 收银台号（Trm），页脚或交易行读出，读不出为空
	TransactionNo string       `json:"transaction_no"` // 交易号（Trn），同上；与门店+日期合起来是小票的身份
	Items         []ParsedItem `json:"items"`
	GrossCents    int          `json:"gross_cents"`
	DiscountCents int          `json:"discount_cents"` // 折扣合计：单件折扣 + 整单立减
	// ReceiptDiscountCents 是整单立减部分（官网导出 PDF 的折扣行，不挂给单件商品）。
	// 纸质小票路径恒为 0。入库时并入 savings_cents，不单独存库。
	ReceiptDiscountCents int      `json:"receipt_discount_cents"`
	SubtotalCents        int      `json:"subtotal_cents"`
	TaxCents             int      `json:"tax_cents"`
	TotalCents           int      `json:"total_cents"`
	SavingsCents         int      `json:"savings_cents"`
	ItemCount            int      `json:"item_count"`    // 小票打印的件数
	TotalDerived         bool     `json:"total_derived"` // total 由小计+税推导（TOTAL 行没读出）
	Warnings             []string `json:"warnings"`
}

var (
	// amountRe 匹配金额：小数点可能是 . 或 ,（OCR 常把 . 读成 ,），
	// 也可能是 : 或 °（如 24:99、8°99）。冒号要当心时间 "14:25"，
	// 见 findAmounts 里的过滤规则。
	amountRe = regexp.MustCompile(`(\d{1,4})([.,:°])(\d{2})`)
	// itemNoRe 匹配货号：3–7 位数字，前后不能是数字（避免把条码/会员号切碎）。
	// 货号有 3 位的（如夹具里的 402）；1–2 位不收，避免把价格小数、重量误认成货号。
	itemNoRe = regexp.MustCompile(`(?:^|[^\d])(\d{3,7})(?:\s|$)`)
	// discountRefRe 匹配折扣行的 0000 开头长号。
	discountRefRe = regexp.MustCompile(`0{4,}\d+`)
	// feeRefRe 匹配押金引用行：EE/货号，紧跟在 CRV 押金行后面，
	// 说明上一件商品是押金（算钱，不算件数），不是真正的商品。
	feeRefRe    = regexp.MustCompile(`^EE/(\d{3,7})$`)
	storeRe     = regexp.MustCompile(`([A-Za-z][A-Za-z .]*?)\s+#(\d{1,3})\b`)
	dateRe      = regexp.MustCompile(`(\d{1,2})/(\d{1,2})/(\d{4})\s+(\d{1,2}):(\d{2})`)
	itemsSoldRe = regexp.MustCompile(`ITEMS SOLD\s*[:=]?\s*(\d+)`)
	// txnFooterRe 匹配页脚交易号："Whse: 21 Trm: 6 Trn: 487 OPT: 676"。
	txnFooterRe = regexp.MustCompile(`Trm:\s*(\d+)\s+Trn:\s*(\d+)`)
	// txnLineRe 匹配交易行："11/14/2026 16:40 21 6 487 676"（日期后依次是门店、收银台、交易号、操作员）。
	txnLineRe = regexp.MustCompile(`\d{1,2}/\d{1,2}/\d{4}\s+\d{1,2}:\d{2}\s+\d{1,3}\s+(\d{1,3})\s+(\d{1,6})\s+\d{1,4}\b`)
)

// extractTxnRef 从小票文本里找收银台号与交易号：先找带标签的页脚，
// 找不到再找交易行。照片裁掉页脚或 OCR 读残时返回空串，调用方用总价判重兜底。
func extractTxnRef(lines []string) (terminal, transaction string) {
	for _, line := range lines {
		if m := txnFooterRe.FindStringSubmatch(line); m != nil {
			return m[1], m[2]
		}
	}
	for _, line := range lines {
		if m := txnLineRe.FindStringSubmatch(line); m != nil {
			return m[1], m[2]
		}
	}
	return "", ""
}

// normalizeAmountText 修正常见的金额 OCR 误读：
// "5 !00" → "5.00"（! 是 . 的误读），"1O.99" → "10.99"（字母 O 是 0 的误读）。
func normalizeAmountText(s string) string {
	// "5 !00" / "5!00"：感叹号前后可能有空格，先整体处理
	s = regexp.MustCompile(`(\d)\s*!\s*(\d)`).ReplaceAllString(s, `$1.$2`)
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if (r == '!' || r == 'O' || r == 'o') && i > 0 && i < len(runes)-1 &&
			runes[i-1] >= '0' && runes[i-1] <= '9' &&
			runes[i+1] >= '0' && runes[i+1] <= '9' {
			if r == '!' {
				b.WriteByte('.')
			} else {
				b.WriteByte('0')
			}
			continue
		}
		b.WriteRune(r)
	}
	// "5 .00" → "5.00"：数字与小数点之间的空格去掉
	out := b.String()
	out = regexp.MustCompile(`(\d)\s+([.,])\s*(\d)`).ReplaceAllString(out, `$1$2$3`)
	return out
}

// findAmounts 返回一行里所有金额（分），按出现顺序。
// 冒号分隔符只在“分”大于 59 时才算金额：14:25 是时间，24:99 是 $24.99。
func findAmounts(line string) []int {
	var out []int
	for _, m := range amountRe.FindAllStringSubmatch(normalizeAmountText(line), -1) {
		d, _ := strconv.Atoi(m[1])
		c, _ := strconv.Atoi(m[3])
		if m[2] == ":" && c <= 59 {
			continue // 时间，不是金额
		}
		out = append(out, d*100+c)
	}
	return out
}

// lastAmount 返回一行里最后一个金额，没有则 ok=false。
func lastAmount(line string) (int, bool) {
	amts := findAmounts(line)
	if len(amts) == 0 {
		return 0, false
	}
	return amts[len(amts)-1], true
}

// ParseReceiptText 解析小票文本。即使部分字段没读出也返回已解析的内容，
// 调用方（确认页）展示 warnings 让人补；只有一行商品都解析不出时才返回错误。
func ParseReceiptText(text string) (*ParsedReceipt, error) {
	p := &ParsedReceipt{}
	lines := strings.Split(text, "\n")

	// 先扫全局字段：门店、日期、件数（与行分类无关）。
	for _, line := range lines {
		if p.Store == "" {
			if m := storeRe.FindStringSubmatch(line); m != nil {
				// 排除 "App#"、"OP#" 这类带冒号的假匹配（storeRe 要求 # 前有空格，
				// App# 紧贴着 #，本来就不会匹配；这里再防一手冒号）。
				if !strings.Contains(line, "#:") {
					// 门店名统一大写：照片 OCR 读出 Danville、PDF 导出是 DANVILLE，
					// 不归一会被当成两个店（2026-11-14 实测）。
					p.Store = strings.ToUpper(strings.TrimSpace(m[1]))
					p.StoreNo = m[2]
				}
			}
		}
		if m := itemsSoldRe.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				p.ItemCount = n
			}
		}
	}
	// 日期取最后一个匹配：底部 "BP 09/12/2026 14:25" 是权威时间，
	// 前面的时间戳行常被 OCR 读残；P7 行是 12 小时制（如 04:40），不能用。
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "P7") {
			continue
		}
		if m := dateRe.FindStringSubmatch(line); m != nil {
			mo, _ := strconv.Atoi(m[1])
			da, _ := strconv.Atoi(m[2])
			ye, _ := strconv.Atoi(m[3])
			hh, _ := strconv.Atoi(m[4])
			mi, _ := strconv.Atoi(m[5])
			if mo >= 1 && mo <= 12 && da >= 1 && da <= 31 && ye >= 2000 && ye <= 2100 &&
				hh <= 23 && mi <= 59 {
				p.PurchasedAt = fmt.Sprintf("%04d-%02d-%02d %02d:%02d", ye, mo, da, hh, mi)
			}
		}
	}

	// 收银台号与交易号：页脚或交易行读出，判重用（读不出时为空）。
	p.TerminalNo, p.TransactionNo = extractTxnRef(lines)

	// 再逐行分类：折扣行 → 合计行 → 商品行。
	var order []string // 货号首次出现顺序
	byNo := map[string]*ParsedItem{}
	discLines := map[string]int{} // 每货号已挂上的折扣行数（同商品折扣补填用）
	discountTotal := 0
	totalFromAmount := 0
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)

		// ---- 押金行引用：EE/货号，上一件商品是 CRV 押金 ----
		// 押金行算钱不算件数；品名没读出来时用标准名，别让 EE/ 行本身变成品名。
		if feeRefRe.MatchString(line) {
			if len(order) > 0 {
				last := byNo[order[len(order)-1]]
				last.IsFee = true
				if last.Name == "" || last.Name == last.ItemNo {
					last.Name = "CA REDEMP VAL"
				}
			}
			continue
		}

		// ---- 折扣行：含 -A 且有 0000 开头的长号 ----
		if strings.Contains(line, "-A") && discountRefRe.MatchString(line) {
			// 折扣行紧跟在对应商品行后面，直接挂到上一件商品上；
			// 引用号只做一致性提示（OCR 常把 / 读成 7、把货号截断）。
			if len(order) == 0 {
				p.Warnings = append(p.Warnings, "折扣行前面没有商品行："+truncateRunes(line, 40))
				continue
			}
			last := byNo[order[len(order)-1]]
			ref := discountItemRef(line)
			if ref != "" && ref != last.ItemNo &&
				!strings.HasPrefix(last.ItemNo, ref) && !strings.HasPrefix(ref, last.ItemNo) {
				p.Warnings = append(p.Warnings, "折扣行引用货号 "+ref+" 与上一件商品 "+last.ItemNo+" 不一致，已按上一件处理")
			}
			amt, ok := lastAmount(line)
			if !ok {
				// 金额读坏了（如 "a00-A"）：同商品其他行的折扣如果一致，
				// 按那个值补填（Costco 同商品每件折扣相同）；补填不了就跳过，
				// 确认页会通过立减合计对不上提醒用户。
				if n := discLines[last.ItemNo]; n > 0 && last.DiscountCents%n == 0 {
					amt = last.DiscountCents / n
					ok = true
					p.Warnings = append(p.Warnings, "折扣行金额没读出来，按同商品其他行的 "+formatCents(amt)+" 补填，请核对")
				} else {
					p.Warnings = append(p.Warnings, "折扣行读不出金额："+truncateRunes(line, 40))
					continue
				}
			}
			last.DiscountCents += amt
			last.NetCents -= amt
			discountTotal += amt
			discLines[last.ItemNo]++
			continue
		}

		// ---- 合计行 ----
		if strings.Contains(upper, "SUBTOTAL") {
			if amt, ok := lastAmount(line); ok {
				p.SubtotalCents = amt
			}
			continue
		}
		if strings.Contains(upper, "INSTANT SAVINGS") {
			if amt, ok := lastAmount(line); ok {
				p.SavingsCents = amt
			}
			continue
		}
		if strings.Contains(upper, "TAX") && !strings.Contains(upper, "ITEMS") {
			// "TAX 13.60" / "A 8.75% TAX 13.60" / "TOTAL TAX 13.60" 都是同一个税额，
			// 取行内最后一个金额（避开 8.75% 这种百分比）。
			if amt, ok := lastAmount(line); ok {
				p.TaxCents = amt
			}
			continue
		}
		if strings.Contains(upper, "AMOUNT:") {
			if amt, ok := lastAmount(line); ok {
				totalFromAmount = amt
			}
			continue
		}
		if strings.Contains(upper, "TOTAL") && !strings.Contains(upper, "TOTAL TAX") &&
			!strings.Contains(upper, "TOTAL NUMBER") && !strings.Contains(upper, "ITEMS SOLD") {
			if amt, ok := lastAmount(line); ok {
				p.TotalCents = amt
			}
			continue
		}

		// ---- 商品行：货号 + 品名 + 金额 ----
		m := itemNoRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		itemNo := line[m[2]:m[3]]
		rest := line[m[3]:]
		restNorm := normalizeAmountText(rest)
		amts := findAmounts(rest)
		if len(amts) == 0 {
			continue // 有数字没金额：地址、电话、条码等，不是商品
		}
		// 品名 = 货号与金额之间的文字（用规范化后的串切，避免长度错位）
		amtIdx := amountRe.FindStringIndex(restNorm)
		name := restNorm
		if amtIdx != nil {
			name = restNorm[:amtIdx[0]]
		}
		name = cleanItemName(name)
		if name == "" {
			name = itemNo // 品名读不出来时先用货号占位，确认页可改
			p.Warnings = append(p.Warnings, "货号 "+itemNo+" 的品名没读出来")
		}
		price := amts[0]
		// E 标志：货号前面（行首到货号之间）的最后一个字母是 E
		// （OCR 杂字可能夹在中间，只要求 E 后面到货号之间没有其他字母数字）。
		before := line[:m[2]]
		foodExempt := regexp.MustCompile(`E[^A-Za-z0-9]*$`).MatchString(before)
		// A/B 标志：金额后面紧跟 A 或 B（应税）。金额后可能有 OCR 杂字，只看金额后前 4 个字符。
		// 纸质小票的应税标记是 A；B 类税的商品（如胶枪）在纸质小票上标 B。
		afterAmt := ""
		if amtIdx != nil {
			afterAmt = restNorm[amtIdx[1]:]
			if len(afterAmt) > 4 {
				afterAmt = afterAmt[:4]
			}
		}
		taxable := strings.Contains(afterAmt, "A") || strings.Contains(afterAmt, "B")

		if ex, ok := byNo[itemNo]; ok {
			// 同货号重复出现：数量 +1（篮球一次买 5 个就是 5 行）。
			if ex.UnitPriceCents != price {
				p.Warnings = append(p.Warnings, "货号 "+itemNo+" 在同一小票里单价不一致（"+
					formatCents(ex.UnitPriceCents)+" / "+formatCents(price)+"），按第一次计")
			}
			ex.Qty++
			ex.NetCents += price
			if taxable {
				ex.Taxable = true
			}
			if foodExempt {
				ex.FoodExempt = true
			}
		} else {
			it := &ParsedItem{
				ItemNo:         itemNo,
				Name:           name,
				Qty:            1,
				UnitPriceCents: price,
				NetCents:       price,
				Taxable:        taxable,
				FoodExempt:     foodExempt,
			}
			byNo[itemNo] = it
			order = append(order, itemNo)
		}
	}

	if len(order) == 0 {
		return nil, fmt.Errorf("没有解析出任何商品行，请检查照片是否清晰、是否拍全小票")
	}
	for _, no := range order {
		it := byNo[no]
		// 每件折扣 = 总折扣 / 件数（每行折扣单独挂，聚合后平均，四舍五入，
		// 避免截断丢分：如 2000/4 应得 500 而不是 499）
		if it.Qty > 0 {
			it.DiscountCents = (it.DiscountCents + it.Qty/2) / it.Qty
		}
		p.Items = append(p.Items, *it)
		p.GrossCents += it.Qty * it.UnitPriceCents
	}
	p.DiscountCents = discountTotal

	// 总价兜底：TOTAL 行没读出时先用 AMOUNT，再用小计+税推导。
	if p.TotalCents == 0 {
		if totalFromAmount > 0 {
			p.TotalCents = totalFromAmount
			p.Warnings = append(p.Warnings, "TOTAL 行没读出来，用 AMOUNT 金额代替，请核对")
		} else if p.SubtotalCents > 0 {
			p.TotalCents = p.SubtotalCents + p.TaxCents
			p.TotalDerived = true
			p.Warnings = append(p.Warnings, "TOTAL 行没读出来，用小计 + 税推导，请核对")
		}
	}
	if p.SavingsCents == 0 {
		p.SavingsCents = discountTotal
	}
	if p.PurchasedAt == "" {
		p.Warnings = append(p.Warnings, "没读出购买日期，请手动填写")
	}
	if p.Store == "" {
		p.Warnings = append(p.Warnings, "没读出门店，请手动填写")
	}
	return p, nil
}

// discountItemRef 从折扣行里提取引用的货号：
// "0000399036 / 1951699" → "1951699"；"/" 被读成 "7" 时退化为取金额前的最后一组数字。
func discountItemRef(line string) string {
	if i := strings.Index(line, "/"); i >= 0 {
		rest := line[i+1:]
		if m := regexp.MustCompile(`(\d{3,7})`).FindStringSubmatch(rest); m != nil {
			return m[1]
		}
	}
	// 兜底：金额前的最后一组 3–7 位数字（跳过 0 开头的长号碎片，如 0000399036 切出的 036）
	norm := normalizeAmountText(line)
	loc := amountRe.FindStringIndex(norm)
	head := norm
	if loc != nil {
		head = norm[:loc[0]]
	}
	groups := regexp.MustCompile(`\d{3,7}`).FindAllString(head, -1)
	for i := len(groups) - 1; i >= 0; i-- {
		if !strings.HasPrefix(groups[i], "0") {
			return groups[i]
		}
	}
	return ""
}

// cleanItemName 清理品名：去首尾杂字、压缩空格。
func cleanItemName(s string) string {
	s = strings.TrimSpace(s)
	// 去掉首尾的 OCR 杂字（保留品名里合法的 * " . / - 等）
	s = strings.Trim(s, ` |\/_—-·•~"'`)
	// 行首的 "E" 是免税标志不是品名（货号前的 E 已单独处理，这里防残留）
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	// 品名里常见的 OCR 误读：小写 z 实际是大写 Z（如 PIZzA）
	if strings.Contains(s, "z") {
		s = strings.ReplaceAll(s, "z", "Z")
	}
	return s
}

// ---------- 货号纠错 ----------

// levenshtein 返回两个短字符串的编辑距离（货号只有 3–7 位，直接算即可）。
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}

// snapItemNumbers 合并疑似 OCR 误读的货号：同名、同单价、货号只差一位数字时
// （如 1000008 与 1000009：8 被读成 9），把少数派并入多数派。
// known 是库里已有的货号（种子/历史），平局时已知货号优先。
// 合并后重算合计；每次合并记一条 warning，确认页会展示给用户核对。
func snapItemNumbers(p *ParsedReceipt, known map[string]bool) {
	// 按（品名，单价）分组找候选
	type key struct {
		name  string
		price int
	}
	groups := map[key][]int{}
	for i, it := range p.Items {
		k := key{strings.ToUpper(strings.TrimSpace(it.Name)), it.UnitPriceCents}
		groups[k] = append(groups[k], i)
	}
	merged := map[int]bool{} // 被并入其他行的下标
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		// 找基准行：件数最多；平局时已知货号优先；再平局取先出现的
		best := idxs[0]
		for _, i := range idxs[1:] {
			a, b := p.Items[best], p.Items[i]
			if b.Qty > a.Qty || (b.Qty == a.Qty && known[b.ItemNo] && !known[a.ItemNo]) {
				best = i
			}
		}
		for _, i := range idxs {
			if i == best || merged[i] {
				continue
			}
			a, b := p.Items[best], p.Items[i]
			if levenshtein(a.ItemNo, b.ItemNo) > 1 {
				continue
			}
			// 合并：数量与实付相加，折扣按总额重算每件（四舍五入）
			totalDisc := a.Qty*a.DiscountCents + b.Qty*b.DiscountCents
			totalQty := a.Qty + b.Qty
			a.Qty = totalQty
			a.NetCents += b.NetCents
			a.DiscountCents = (totalDisc + totalQty/2) / totalQty
			a.Taxable = a.Taxable || b.Taxable
			a.FoodExempt = a.FoodExempt || b.FoodExempt
			p.Items[best] = a
			merged[i] = true
			p.Warnings = append(p.Warnings, "货号 "+b.ItemNo+" 与 "+a.ItemNo+" 只差一位且品名单价相同，已合并为 "+a.ItemNo+"，请核对")
		}
	}
	if len(merged) == 0 {
		return
	}
	kept := p.Items[:0]
	for i, it := range p.Items {
		if !merged[i] {
			kept = append(kept, it)
		}
	}
	p.Items = kept
	// 重算原价与折扣合计（小计/税/总计是小票印的数，不动）
	p.GrossCents = 0
	p.DiscountCents = 0
	for _, it := range p.Items {
		p.GrossCents += it.Qty * it.UnitPriceCents
		p.DiscountCents += it.Qty * it.DiscountCents
	}
}

// ---------- 对账 ----------

// reconcileTolerance 是对账允许的误差（分）：1 分，容忍税额舍入。
const reconcileTolerance = 1

// Reconcile 校验对账：原价 − 折扣 = 小计，小计 + 税 = 总计。
// 返回阻塞性问题列表（空 = 通过）；警告（件数/立减不一致）单独返回。
func Reconcile(p *ParsedReceipt) (problems []string, warnings []string) {
	abs := func(n int) int {
		if n < 0 {
			return -n
		}
		return n
	}
	if p.SubtotalCents > 0 {
		if d := abs(p.GrossCents - p.DiscountCents - p.SubtotalCents); d > reconcileTolerance {
			problems = append(problems, "原价 "+formatCents(p.GrossCents)+" − 折扣 "+
				formatCents(p.DiscountCents)+" = "+formatCents(p.GrossCents-p.DiscountCents)+
				"，与小计 "+formatCents(p.SubtotalCents)+" 不符（差 "+formatCents(d)+"）")
		}
	}
	if p.TotalCents > 0 && p.SubtotalCents > 0 {
		if d := abs(p.SubtotalCents + p.TaxCents - p.TotalCents); d > reconcileTolerance {
			problems = append(problems, "小计 "+formatCents(p.SubtotalCents)+" + 税 "+
				formatCents(p.TaxCents)+" = "+formatCents(p.SubtotalCents+p.TaxCents)+
				"，与总计 "+formatCents(p.TotalCents)+" 不符（差 "+formatCents(d)+"）")
		}
	}
	qty := 0
	for _, it := range p.Items {
		if it.IsFee {
			continue // 押金行不算件数（小票打印的件数不含它）
		}
		qty += it.Qty
	}
	if p.ItemCount > 0 && qty != p.ItemCount {
		warnings = append(warnings, "解析出 "+strconv.Itoa(qty)+" 件，小票打印 "+strconv.Itoa(p.ItemCount)+" 件")
	}
	if p.SavingsCents > 0 && abs(p.SavingsCents-p.DiscountCents) > reconcileTolerance {
		warnings = append(warnings, "立减合计 "+formatCents(p.DiscountCents)+"，小票打印 "+formatCents(p.SavingsCents))
	}
	return problems, warnings
}
