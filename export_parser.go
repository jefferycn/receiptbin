package main

// Costco 官网导出的 In-Warehouse Receipt PDF 解析。
// 文本来自 pdftotext（自带文本层，不用 OCR），格式与纸质小票不同：
//
//   RIVERTON #456                       门店头（页眉）
//   Member / 12 位会员号                 会员号：解析前已去掉，见 stripMemberNumbers
//   E  3000001 GREEN TEA   12.49 N      商品行：单字母前缀（E=食品免税，F=FSA 合格商品等），N/Y 后缀（Y=应税）
//   2 @ 4.50                            数量行：在商品行之前，商品行金额是小计
//   3000002 PAPER 9.99 Y + 下一行 NAPKINS  品名可能换行续在下一行
//   400530 /2056254  25.00-             折扣行：优惠券号 /货号 + 金额，挂给对应商品
//   3000099  3.00-                      折扣行（无货号引用）：整单立减，不挂给商品
//   SUBTOTAL / TAX / **** TOTAL / INSTANT SAVINGS / ITEMS SOLD = N
//   08/15/2026 16:40 456 3 123 789      交易时间（取第一个；末尾 P7 行是 12 小时制，别用）
//
// 注意：这里的数字全是虚构的格式示例，不是真实小票；测试夹具见 pdfimport_test.go。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// exportAmountPat 匹配金额：含千分位逗号（1,008.43）或小数点逗号（12,49）。
// 千分位形式优先匹配，避免 "1,008.43" 被读成 8.43（0629 实测）。
const exportAmountPat = `\d{1,3}(?:,\d{3})*\.\d{2}|\d{1,4}[.,]\d{2}`

var (
	// exportQtyRe 匹配数量行："2 @ 6.69"（在对应商品行之前）。
	exportQtyRe = regexp.MustCompile(`^(\d+)\s+@\s+(` + exportAmountPat + `)\s*$`)
	// exportDiscountRe 匹配折扣行：金额带尾随减号，如 "400530 /2056254  25.00-"。
	exportDiscountRe = regexp.MustCompile(`(` + exportAmountPat + `)-\s*$`)
	// exportDiscountRefRe 匹配折扣行里的货号引用："/2056254" 或 "/ 1951699"。
	exportDiscountRefRe = regexp.MustCompile(`/\s*(\d{3,7})\b`)
	// exportItemRe 匹配商品行：可选单字母前缀、3–7 位货号、品名（可空）、金额、可选税码后缀。
	// 税码不止 N/Y：Concord 小票上有 B 类税（页脚 (A) A / (B) B 分开计税），
	// 所以后缀接受任意单个大写字母；只有 N 是免税，其余都算应税。
	// 前缀不止 E：F 是 FSA 合格商品（如卫生用品、绷带），只认 E 会把整行漏掉（2026-11-14 实测）。
	// 只有 E 算食品免税，其他字母只作前缀接受、不记标志。
	// 货号有 3 位的（如夹具里的 402）；1–2 位不收，避免把价格、重量误认成货号。
	exportItemRe = regexp.MustCompile(`^([A-Z])?\s*(\d{3,7})\s+(.*?)\s*(` + exportAmountPat + `)\s*([A-Z])?\s*$`)
	// exportWhseRe 匹配页脚门店号："Whse: 663"（页眉门店行读不出时的兜底）。
	exportWhseRe = regexp.MustCompile(`Whse:\s*(\d{1,3})\b`)
	// exportContinuationRe 匹配品名续行：大写单词，如 NAPKINS、BLUEBERRIES。
	// exportContinuationRe 匹配品名续行：无金额的大写单词行。
	// 首字符可以是数字（"1L"、"2PK" 这类单位续行，0918 实测）；
	// 是否含字母在调用处再判断，避免纯数字行（条码、会员号）被当成品名。
	exportContinuationRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9 .&/'-]{0,40}$`)
)

// exportSkipPrefixes 是绝不作品名续行的行首（页脚杂项）。
var exportSkipPrefixes = []string{
	"APPROVED", "TOTAL", "SUBTOTAL", "TAX", "MEMBER", "CHIP", "VISA",
	"CHANGE", "THANK", "WHSE", "ITEMS SOLD", "INSTANT SAVINGS", "AMOUNT",
}

// ParseExportReceiptText 解析官网导出的小票文本（pdftotext 结果）。
// 入口先去掉会员号（即使调用方忘了剥离，这里也再剥一次）。
// 折扣行带 /货号 时挂给对应商品（与照片解析一致）；没有货号引用时
// 才是整单立减，计入 ReceiptDiscountCents，不挂给任何商品。
func ParseExportReceiptText(text string) (*ParsedReceipt, error) {
	text = stripMemberNumbers(text)
	p := &ParsedReceipt{}
	lines := strings.Split(text, "\n")

	// 先扫全局字段：门店、日期（取第一个）、件数。
	for _, line := range lines {
		if p.Store == "" {
			if m := storeRe.FindStringSubmatch(line); m != nil && !strings.Contains(line, "#:") {
				// 门店名统一大写（与照片解析一致，否则同一门店被当成两个店）。
				p.Store = strings.ToUpper(strings.TrimSpace(m[1]))
				p.StoreNo = m[2]
			}
		}
		if m := itemsSoldRe.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				p.ItemCount = n
			}
		}
	}
	// 门店兜底：页眉没读出时用页脚 "Whse: 663" 的门店号，店名先用 COSTCO 占位。
	if p.Store == "" {
		for _, line := range lines {
			if m := exportWhseRe.FindStringSubmatch(line); m != nil {
				p.Store = "COSTCO"
				p.StoreNo = m[1]
				p.Warnings = append(p.Warnings, "只读出门店号 "+m[1]+"，门店名请核对")
				break
			}
		}
	}
	// 日期取第一个匹配：交易时间行是权威时间；
	// 末尾的 P7 行是 12 小时制（如 03:56），不能用。
	for _, line := range lines {
		if m := dateRe.FindStringSubmatch(line); m != nil {
			mo, _ := strconv.Atoi(m[1])
			da, _ := strconv.Atoi(m[2])
			ye, _ := strconv.Atoi(m[3])
			hh, _ := strconv.Atoi(m[4])
			mi, _ := strconv.Atoi(m[5])
			if mo >= 1 && mo <= 12 && da >= 1 && da <= 31 && ye >= 2000 && ye <= 2100 &&
				hh <= 23 && mi <= 59 {
				p.PurchasedAt = fmt.Sprintf("%04d-%02d-%02d %02d:%02d", ye, mo, da, hh, mi)
				break
			}
		}
	}

	// 收银台号与交易号：页脚或交易行读出，判重用（读不出时为空）。
	p.TerminalNo, p.TransactionNo = extractTxnRef(lines)

	// 再逐行分类：数量行 → 折扣行 → 合计行 → 商品行 → 品名续行。
	var order []string
	byNo := map[string]*ParsedItem{}
	discountTotal := 0   // 全部折扣（含挂给商品的）
	receiptDiscount := 0 // 无货号引用的整单立减部分
	totalFromAmount := 0
	pendingQty := 0
	pendingName := "" // 提前/孤儿品名续行，留给下一件无品名商品用
	inItems := true   // SUBTOTAL 之后是合计与页脚，不再有商品行
	var lastItem *ParsedItem
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)

		// ---- 数量行：在商品行之前，声明下一件商品的数量与单价 ----
		if m := exportQtyRe.FindStringSubmatch(line); m != nil {
			q, _ := strconv.Atoi(m[1])
			if q >= 1 {
				pendingQty = q
			}
			continue
		}

		// ---- 合计行（SUBTOTAL 出现后商品区结束） ----
		if strings.Contains(upper, "SUBTOTAL") {
			if amt, ok := lastAmount(line); ok {
				p.SubtotalCents = amt
			}
			inItems = false
			lastItem = nil
			continue
		}
		if strings.Contains(upper, "INSTANT SAVINGS") {
			if amt, ok := lastAmount(line); ok {
				p.SavingsCents = amt
			}
			continue
		}
		if strings.Contains(upper, "TAX") && !strings.Contains(upper, "ITEMS") {
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
		if !inItems {
			continue // 页脚：卡号、致谢、Whse 行等，一律忽略
		}

		// ---- 折扣行：带 /货号 时挂给对应商品；不带货号时挂给上一件商品 ----
		// 导出格式的折扣行紧跟在它对应的商品后面（中间可能夹 /NO SHOW 这类备注行，
		// 不会清空 lastItem）；引用货号找不到时也退回挂给上一件商品——引用号
		// 可能是旧货号或优惠券册上的编号（0828 实测：/13664 不在小票上，
		// 但 14.00- 紧跟在鸡胸肉后面，就是它的折扣）。只有前面没有任何商品时，
		// 才算整单立减。
		// 注意：上一条折扣行会把 lastItem 清掉，所以连续两条折扣时，
		// 第二条前面没有商品，仍按整单立减处理。
		if m := exportDiscountRe.FindStringSubmatch(line); m != nil {
			if amt, ok := parseMoneyToCents(m[1]); ok {
				attached := false
				if ref := exportDiscountRefRe.FindStringSubmatch(line); ref != nil {
					if it, found := byNo[ref[1]]; found {
						it.DiscountCents += amt
						it.NetCents -= amt
						attached = true
					} else if lastItem != nil {
						// 引用货号不在小票上：退回挂给上一件商品。
						lastItem.DiscountCents += amt
						lastItem.NetCents -= amt
						attached = true
					} else {
						p.Warnings = append(p.Warnings, "折扣行引用货号 "+ref[1]+" 找不到对应商品，已按整单立减处理")
					}
				} else if lastItem != nil {
					// 不带货号引用的折扣属于上一件商品（1030 这张：2.00- 跟在 KBELL 后面）。
					lastItem.DiscountCents += amt
					lastItem.NetCents -= amt
					attached = true
				}
				if !attached {
					receiptDiscount += amt
				}
				discountTotal += amt
			}
			lastItem = nil
			continue
		}

		// ---- 商品行 ----
		if m := exportItemRe.FindStringSubmatch(line); m != nil {
			foodExempt := m[1] == "E"
			itemNo := m[2]
			name := cleanItemName(m[3])
			// 无品名商品行：用之前攒下的提前/孤儿续行当品名；
			// 不管用没用上，攒下的只对紧跟的这一件有效，用完即清。
			if name == "" && pendingName != "" {
				name = pendingName
			}
			pendingName = ""
			extended, ok := parseMoneyToCents(m[4])
			if !ok {
				continue
			}
			// 税码只有 N 是免税；Y、B 等任意字母都算应税（B 类税在页脚单独计，如 (B) B 0.36）。
			taxable := m[5] != "" && m[5] != "N"
			qty := 1
			unit := extended
			if pendingQty > 0 {
				// 商品行金额是小计：单价 = 小计 / 数量（四舍五入），实付按小计算
				qty = pendingQty
				unit = (extended + qty/2) / qty
				pendingQty = 0
			}
			if ex, dup := byNo[itemNo]; dup {
				if ex.UnitPriceCents != unit {
					p.Warnings = append(p.Warnings, "货号 "+itemNo+" 在同一小票里单价不一致（"+
						formatCents(ex.UnitPriceCents)+" / "+formatCents(unit)+"），按第一次计")
				}
				ex.Qty += qty
				ex.NetCents += qty * unit
				if taxable {
					ex.Taxable = true
				}
				if foodExempt {
					ex.FoodExempt = true
				}
				lastItem = ex
			} else {
				it := &ParsedItem{
					ItemNo:         itemNo,
					Name:           name,
					Qty:            qty,
					UnitPriceCents: unit,
					NetCents:       qty * unit,
					Taxable:        taxable,
					FoodExempt:     foodExempt,
				}
				byNo[itemNo] = it
				order = append(order, itemNo)
				lastItem = it
			}
			continue
		}

		// ---- 押金行引用：EE/货号，上一件商品是 CRV 押金 ----
		// 必须在品名续行之前判断：EE/ 行本身符合续行正则，不拦住会被当成品名。
		if feeRefRe.MatchString(line) {
			if lastItem != nil {
				lastItem.IsFee = true
				if lastItem.Name == "" || lastItem.Name == lastItem.ItemNo {
					lastItem.Name = "CA REDEMP VAL"
				}
			}
			continue
		}

		// ---- 品名续行：无金额的大写单词行（须含字母，纯数字行不算） ----
		// 三种归属：
		// 1. 正常续行：接在上一件商品的品名后面（PAPER + NAPKINS）。
		// 2. 提前的品名：续行后面紧跟一个无品名商品行、且续行本身较长
		//    （多词或 >4 字符）时，它其实是下一件商品的品名——PDF 排版
		//    把它提前了一行（0918 实测：NUBZ DOG 属于 921485、
		//    KSCOCOWTR 属于 1495250）；先攒起来，等下一件商品行用。
		//    短片段（ORG、1L、2PK）是上一件的单位/后缀，照常续接。
		// 3. 孤儿续行：上一件商品不存在时（如折扣行刚把 lastItem 清掉），
		//    攒起来给下一件无品名商品用（BOBS → BOBS OATMEAL；
		//    CA REDEMP VAL → 押金行的品名）。只在商品区内攒，页眉地址行不算。
		if exportContinuationRe.MatchString(line) && findAmounts(line) == nil &&
			strings.ContainsAny(line, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			skip := false
			for _, pre := range exportSkipPrefixes {
				if strings.HasPrefix(upper, pre) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			if lastItem == nil {
				if len(order) > 0 {
					pendingName = stripTrailingTaxFlag(line)
				}
				continue
			}
			if (strings.Contains(line, " ") || len(line) > 4) && nextLineIsNamelessItem(lines, i) {
				pendingName = line
				continue
			}
			if lastItem.Name == "" {
				lastItem.Name = line
			} else {
				lastItem.Name += " " + line
			}
			continue
		}
		// 其他行（地址、交易号、/NO SHOW 这类备注）：忽略
	}

	if len(order) == 0 {
		return nil, fmt.Errorf("没有解析出任何商品行，请确认这是 Costco 导出的 In-Warehouse 小票 PDF")
	}
	for _, no := range order {
		it := byNo[no]
		if it.Name == "" {
			it.Name = no
			p.Warnings = append(p.Warnings, "货号 "+no+" 的品名没读出来")
		}
		// 每件折扣 = 总折扣 / 件数（与照片解析一致，四舍五入，避免截断丢分）
		if it.Qty > 0 {
			it.DiscountCents = (it.DiscountCents + it.Qty/2) / it.Qty
		}
		p.Items = append(p.Items, *it)
		p.GrossCents += it.Qty * it.UnitPriceCents
	}
	p.DiscountCents = discountTotal
	p.ReceiptDiscountCents = receiptDiscount
	if receiptDiscount > 0 {
		p.Warnings = append(p.Warnings, "含整单立减 "+formatCents(receiptDiscount)+"（已计入对账，不挂给单件商品）")
	}

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

// nextLineIsNamelessItem 看下一行是不是无品名商品行（品名续行归属判断用）。
func nextLineIsNamelessItem(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	next := strings.TrimSpace(lines[i+1])
	m := exportItemRe.FindStringSubmatch(next)
	return m != nil && cleanItemName(m[3]) == ""
}

// stripTrailingTaxFlag 去掉行尾的税码（"CA REDEMP VAL N" → "CA REDEMP VAL"）。
// 只去 N/Y/B 三种实测过的税码，避免把 "VITAMIN D" 这类真品名截断。
func stripTrailingTaxFlag(s string) string {
	if len(s) > 2 && s[len(s)-2] == ' ' {
		switch s[len(s)-1] {
		case 'N', 'Y', 'B':
			return s[:len(s)-2]
		}
	}
	return s
}
