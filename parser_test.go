package main

// 小票解析测试：夹具是一张虚构的示例小票（Fremont #123），
// 商品、价格、日期、卡号全是编的演示数据，不含任何真实购物记录。
// 干净转录夹具 = 示例小票原文；
// OCR 夹具 = 手写模拟的 tesseract 识别结果（带典型 OCR 噪声），
// 验证解析器对 OCR 噪声的容忍度。
// 全部金额已对账：原价 278.84 − 折扣 35.00 = 小计 243.84，税 14.52，总计 258.36。

import (
	_ "embed"
	"strings"
	"testing"
)

//go:embed testdata/ocr_example.txt
var ocrFixture string

//go:embed testdata/ocr_example_go.txt
var ocrGoFixture string

const cleanFixture = `COSTCO WHOLESALE
Fremont #123
1000 Example Blvd
Fremont, CA 94536
(510) 555-0100
E 1000001 KS ALMONDS 15.99
E 1000002 SOURDOUGH BREAD 6.49
1000003 PAPER TOWELS 12PK 21.99 A
0000999001 / 1000003 4.00-A
E 1000004 STRAWBERRIES 7.99
E 1000005 ROTISSERIE CHKN 5.99
1000006 DISH SOAP 9.99 A
************Bottom of Basket************
1000007 LED SHOP LIGHT 29.99 A
0000999002 / 1000007 6.00-A
************BOB Count 1 ************
1000008 SOCCER BALL 24.99 A
0000999003 /1000008 5.00-A
1000008 SOCCER BALL 24.99 A
0000999003 /1000008 5.00-A
1000008 SOCCER BALL 24.99 A
0000999003 /1000008 5.00-A
1000008 SOCCER BALL 24.99 A
0000999003 /1000008 5.00-A
1000008 SOCCER BALL 24.99 A
0000999003 /1000008 5.00-A
E 1000011 GREEK YOGURT 9.49
E 1000012 SALMON FILLET 22.99
1000014 TRASH BAGS 13.99 A
E 1000015 ORANGE JUICE 8.99
SUBTOTAL 243.84
TAX 14.52
**** TOTAL 258.36
XXXXXXXXXXXX4242 H
AID: A0000000031010
Seq# 1234 App#: 567890
Visa Resp: APPROVED
APPROVED - Purchase
AMOUNT: $258.36
Visa 258.36
CHANGE 0.00
A 8.75% TAX 14.52
TOTAL TAX 14.52
TOTAL NUMBER OF ITEMS SOLD = 16
INSTANT SAVINGS $ 35.00
OP#: 001 Name: Sample
Thank You! Please Come Again
Total BOB Item Count = 1
Items Sold: 16
BP 09/12/2026 14:25
`

// assertReceipt 是三个夹具共用的断言：12 行聚合商品、5 个足球、全部金额。
func assertReceipt(t *testing.T, p *ParsedReceipt) {
	t.Helper()
	if p.Store != "FREMONT" || p.StoreNo != "123" {
		t.Errorf("门店 = %q #%q，想要 FREMONT #123（店名统一大写）", p.Store, p.StoreNo)
	}
	if p.PurchasedAt != "2026-09-12 14:25" {
		t.Errorf("购买时间 = %q，想要 2026-09-12 14:25", p.PurchasedAt)
	}
	if len(p.Items) != 12 {
		t.Fatalf("聚合商品行 = %d，想要 12", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	qty := 0
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
		qty += it.Qty
	}
	if qty != 16 {
		t.Errorf("总件数 = %d，想要 16", qty)
	}
	ball, ok := byNo["1000008"]
	if !ok {
		t.Fatalf("没解析出足球 1000008")
	}
	if ball.Qty != 5 {
		t.Errorf("足球数量 = %d，想要 5", ball.Qty)
	}
	if ball.UnitPriceCents != 2499 || ball.DiscountCents != 500 || ball.NetCents != 9995 {
		t.Errorf("足球单价/折扣/实付 = %d/%d/%d，想要 2499/500/9995",
			ball.UnitPriceCents, ball.DiscountCents, ball.NetCents)
	}
	// 逐项核对关键商品
	want := map[string][3]int{ // 货号 → 单价，折扣，实付
		"1000001": {1599, 0, 1599},
		"1000003": {2199, 400, 1799},
		"1000007": {2999, 600, 2399},
		"1000012": {2299, 0, 2299},
		"1000014": {1399, 0, 1399},
	}
	for no, w := range want {
		it, ok := byNo[no]
		if !ok {
			t.Errorf("没解析出货号 %s", no)
			continue
		}
		if it.UnitPriceCents != w[0] || it.DiscountCents != w[1] || it.NetCents != w[2] {
			t.Errorf("货号 %s 单价/折扣/实付 = %d/%d/%d，想要 %d/%d/%d",
				no, it.UnitPriceCents, it.DiscountCents, it.NetCents, w[0], w[1], w[2])
		}
	}
	if p.GrossCents != 27884 {
		t.Errorf("原价合计 = %d，想要 27884", p.GrossCents)
	}
	if p.DiscountCents != 3500 {
		t.Errorf("折扣合计 = %d，想要 3500", p.DiscountCents)
	}
	if p.SubtotalCents != 24384 {
		t.Errorf("小计 = %d，想要 24384", p.SubtotalCents)
	}
	if p.TaxCents != 1452 {
		t.Errorf("税 = %d，想要 1452", p.TaxCents)
	}
	if p.TotalCents != 25836 {
		t.Errorf("总计 = %d，想要 25836", p.TotalCents)
	}
	if p.SavingsCents != 3500 {
		t.Errorf("立减 = %d，想要 3500", p.SavingsCents)
	}
	if p.ItemCount != 16 {
		t.Errorf("打印件数 = %d，想要 16", p.ItemCount)
	}
}

func TestParseCleanReceipt(t *testing.T) {
	p, err := ParseReceiptText(cleanFixture)
	if err != nil {
		t.Fatalf("干净夹具解析失败：%v", err)
	}
	assertReceipt(t, p)
	// 干净夹具上 E/A 标志应全部正确
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	if !byNo["1000001"].FoodExempt || byNo["1000001"].Taxable {
		t.Errorf("KS ALMONDS 应为食品免税、非应税")
	}
	if !byNo["1000003"].Taxable || byNo["1000003"].FoodExempt {
		t.Errorf("PAPER TOWELS 应为应税、非食品免税")
	}
	if p.TotalDerived {
		t.Errorf("干净夹具的 TOTAL 行明明在，不应走推导")
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("干净夹具对账应通过，问题：%v", problems)
	}
}

func TestParseOCRReceipt(t *testing.T) {
	p, err := ParseReceiptText(ocrFixture)
	if err != nil {
		t.Fatalf("OCR 夹具解析失败：%v", err)
	}
	assertReceipt(t, p)
	// OCR 夹具里 TOTAL 行读坏了，应走 AMOUNT 兜底（不是推导）
	if p.TotalDerived {
		t.Errorf("OCR 夹具应走 AMOUNT 兜底，不应走小计+税推导")
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("OCR 夹具对账应通过，问题：%v", problems)
	}
	if len(p.Warnings) > 0 {
		t.Logf("OCR 夹具 warnings（可接受）：%v", p.Warnings)
	}
}

// TestParseOCRGoFixture 是 Go 自带预处理管线（灰度 + Lanczos-3 放大 2 倍）
// 的 OCR 结果：其中一行足球折扣被读成 "a00-A"，靠同商品折扣补填才对上账。
// 这是线上真实走的管线，必须锁死。
func TestParseOCRGoFixture(t *testing.T) {
	p, err := ParseReceiptText(ocrGoFixture)
	if err != nil {
		t.Fatalf("Go 管线 OCR 夹具解析失败：%v", err)
	}
	assertReceipt(t, p)
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("Go 管线 OCR 夹具对账应通过，问题：%v", problems)
	}
	found := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "补填") {
			found = true
		}
	}
	if !found {
		t.Errorf("应有一条折扣补填提示，实际 warnings：%v", p.Warnings)
	}
}

// P7 行是 12 小时制（如 04:40），即使在最后也不能当购买时间
// （否则照片导入的时刻与 PDF 导入差 12 小时，重复导入也判不出来）。
func TestParseSkipsP7Line(t *testing.T) {
	p, err := ParseReceiptText(cleanFixture + "\nP7 09/12/2026 02:25\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.PurchasedAt != "2026-09-12 14:25" {
		t.Errorf("购买时间 = %q，想要 2026-09-12 14:25（BP 行，不是 P7 行的 02:25）", p.PurchasedAt)
	}
}

// 交易号从页脚读出（Whse/Trm/Trn 行），是小票的身份标识，判重用。
func TestParseTxnRefFromFooter(t *testing.T) {
	p, err := ParseReceiptText(cleanFixture + "\nWhse: 123 Trm: 6 Trn: 487 OPT: 676\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.TerminalNo != "6" || p.TransactionNo != "487" {
		t.Errorf("收银台/交易号 = %q/%q，想要 6/487", p.TerminalNo, p.TransactionNo)
	}
}

// 页脚被裁掉时从交易行读出（日期后依次是门店、收银台、交易号、操作员）。
func TestParseTxnRefFromTxnLine(t *testing.T) {
	p, err := ParseReceiptText(cleanFixture + "\n09/12/2026 14:25 123 6 487 676\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.TerminalNo != "6" || p.TransactionNo != "487" {
		t.Errorf("收银台/交易号 = %q/%q，想要 6/487", p.TerminalNo, p.TransactionNo)
	}
}

// 纸质小票的 B 类税商品：金额后标 B 也算应税（与导出格式的 B 税码对应）。
// 纸质小票的 CRV 押金行：EE/货号 标明上一件是押金，算钱不算件数。
func TestParseOCRCRVFee(t *testing.T) {
	fixture := `COSTCO WHOLESALE
Fremont #123
1000 Example Blvd
Fremont, CA 94536
(510) 555-0100
E 1000021 SMOOTHIE 7.49
0000999004 /1000021 2.50-A
6000002 CA REDEMP VAL 0.10
EE/1000021
E 1000022 ORANGE JUICE 8.99
SUBTOTAL 14.08
TAX 0.00
**** TOTAL 14.08
XXXXXXXXXXXX4242 H
APPROVED - Purchase
AMOUNT: $14.08
Visa 14.08
CHANGE 0.00
TOTAL TAX 0.00
TOTAL NUMBER OF ITEMS SOLD = 2
BP 09/12/2026 14:25
`
	p, err := ParseReceiptText(fixture)
	if err != nil {
		t.Fatalf("押金纸质小票解析失败：%v", err)
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	fee, ok := byNo["6000002"]
	if !ok {
		t.Fatalf("押金行 6000002 没解析出来")
	}
	if !fee.IsFee {
		t.Errorf("押金行应标为费用（IsFee）")
	}
	if fee.Name != "CA REDEMP VAL" {
		t.Errorf("押金行品名 = %q，想要 CA REDEMP VAL", fee.Name)
	}
	problems, warnings := Reconcile(p)
	if len(problems) != 0 || len(warnings) != 0 {
		t.Errorf("对账应通过且无警告（押金不算件数），问题：%v 警告：%v", problems, warnings)
	}
}

func TestParseOCRBFlagTaxable(t *testing.T) {
	fixture := `COSTCO WHOLESALE
Fremont #123
1000 Example Blvd
Fremont, CA 94536
(510) 555-0100
1000016 GLUE GUN 29.99 B
1000017 PAPER TOWEL 9.99 A
E 1000018 ORANGE JUICE 8.99
SUBTOTAL 48.97
TAX 4.29
**** TOTAL 53.26
XXXXXXXXXXXX4242 H
APPROVED - Purchase
AMOUNT: $53.26
Visa 53.26
CHANGE 0.00
A 8.75% TAX 4.29
TOTAL TAX 4.29
TOTAL NUMBER OF ITEMS SOLD = 3
BP 09/12/2026 14:25
`
	p, err := ParseReceiptText(fixture)
	if err != nil {
		t.Fatalf("B 税码纸质小票解析失败：%v", err)
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	gun, ok := byNo["1000016"]
	if !ok {
		t.Fatalf("B 税码商品 1000016 没解析出来")
	}
	if !gun.Taxable {
		t.Errorf("金额后标 B 的商品应为应税")
	}
	if !byNo["1000017"].Taxable {
		t.Errorf("金额后标 A 的商品应为应税")
	}
	if byNo["1000018"].Taxable || !byNo["1000018"].FoodExempt {
		t.Errorf("ORANGE JUICE 应为食品免税、非应税")
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}

func TestReconcileMismatch(t *testing.T) {
	p, err := ParseReceiptText(cleanFixture)
	if err != nil {
		t.Fatal(err)
	}
	// 篡改一个单价，对账必须拦住
	p.Items[0].UnitPriceCents += 100
	p.Items[0].NetCents += 100
	p.GrossCents += 100
	problems, _ := Reconcile(p)
	if len(problems) == 0 {
		t.Fatalf("单价被改了 1 美元，对账应报错")
	}
	if !strings.Contains(problems[0], "小计") {
		t.Errorf("报错信息应提到小计，实际：%v", problems)
	}
	// 1 分误差应放行（税额舍入）
	p2, _ := ParseReceiptText(cleanFixture)
	p2.TotalCents++
	problems, _ = Reconcile(p2)
	if len(problems) != 0 {
		t.Errorf("1 分误差应放行，实际：%v", problems)
	}
	// 2 分误差应拦住
	p2.TotalCents++
	problems, _ = Reconcile(p2)
	if len(problems) == 0 {
		t.Errorf("2 分误差应拦住")
	}
}

func TestAmountSeparators(t *testing.T) {
	// 冒号与度数符号作小数点：24:99 是 $24.99，8°99 是 $8.99；
	// 但 14:25 是时间，不是金额。
	text := `Fremont #123
1000008 SOCCER BALL 24:99 A
0000999003 /1000008 5.00-A
1000015 ORANGE JUICE 8°99
SUBTOTAL 28.98
TAX 0.00
**** TOTAL 28.98
BP 09/12/2026 14:25
`
	p, err := ParseReceiptText(text)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(p.Items) != 2 {
		t.Fatalf("商品行 = %d，想要 2（时间 14:25 不能变成商品）", len(p.Items))
	}
	if p.Items[0].UnitPriceCents != 2499 {
		t.Errorf("24:99 应解析为 2499，实际 %d", p.Items[0].UnitPriceCents)
	}
	if p.Items[1].UnitPriceCents != 899 {
		t.Errorf("8°99 应解析为 899，实际 %d", p.Items[1].UnitPriceCents)
	}
	if p.PurchasedAt != "2026-09-12 14:25" {
		t.Errorf("购买时间 = %q，想要 2026-09-12 14:25", p.PurchasedAt)
	}
}

func TestSnapItemNumbers(t *testing.T) {
	// 1000008 被读成 1000009（8→9）：同名同价，应合并回 1000008；
	// 库里已知 1000008，平局时也应选它。
	p := &ParsedReceipt{
		Items: []ParsedItem{
			{ItemNo: "1000008", Name: "SOCCER BALL", Qty: 3, UnitPriceCents: 2499, DiscountCents: 500, NetCents: 5997},
			{ItemNo: "1000009", Name: "SOCCER BALL", Qty: 1, UnitPriceCents: 2499, DiscountCents: 500, NetCents: 1999},
			{ItemNo: "1000003", Name: "PAPER TOWELS 12PK", Qty: 1, UnitPriceCents: 2199, DiscountCents: 400, NetCents: 1799},
		},
	}
	snapItemNumbers(p, map[string]bool{"1000008": true})
	if len(p.Items) != 2 {
		t.Fatalf("合并后行数 = %d，想要 2", len(p.Items))
	}
	if p.Items[0].ItemNo != "1000008" || p.Items[0].Qty != 4 || p.Items[0].NetCents != 7996 {
		t.Errorf("合并结果 = %+v，想要 1000008 qty=4 net=7996", p.Items[0])
	}
	if p.Items[0].DiscountCents != 500 {
		t.Errorf("合并后每件折扣 = %d，想要 500", p.Items[0].DiscountCents)
	}
	if len(p.Warnings) != 1 {
		t.Errorf("应有 1 条合并提示，实际 %v", p.Warnings)
	}
	// 品名不同时不合并
	p2 := &ParsedReceipt{
		Items: []ParsedItem{
			{ItemNo: "1000008", Name: "SOCCER BALL", Qty: 1, UnitPriceCents: 2499, NetCents: 2499},
			{ItemNo: "1000009", Name: "OTHER BALL", Qty: 1, UnitPriceCents: 2499, NetCents: 2499},
		},
	}
	snapItemNumbers(p2, map[string]bool{})
	if len(p2.Items) != 2 {
		t.Errorf("品名不同不应合并")
	}
	// 货号差两位时不合并
	p3 := &ParsedReceipt{
		Items: []ParsedItem{
			{ItemNo: "1000008", Name: "SOCCER BALL", Qty: 1, UnitPriceCents: 2499, NetCents: 2499},
			{ItemNo: "1000019", Name: "SOCCER BALL", Qty: 1, UnitPriceCents: 2499, NetCents: 2499},
		},
	}
	snapItemNumbers(p3, map[string]bool{})
	if len(p3.Items) != 2 {
		t.Errorf("货号差两位不应合并")
	}
}

// shortItemNoFixture：3 位短货号回归夹具（照片 OCR 路径）。
// 2026-11-14 真实小票：3 位短货号曾被漏掉。数字全是虚构的。
const shortItemNoFixture = `COSTCO WHOLESALE
Fremont #123
1000 Example Blvd
Fremont, CA 94536
E 402 RICE 50LB 26.99
1000001 KS ALMONDS 15.99
SUBTOTAL 42.98
TAX 0.00
**** TOTAL 42.98
XXXXXXXXXXXX4242 H
APPROVED - Purchase
AMOUNT: $42.98
Visa 42.98
CHANGE 0.00
TOTAL TAX 0.00
TOTAL NUMBER OF ITEMS SOLD = 2
Items Sold: 2
BP 09/12/2026 14:25
`

func TestParseShortItemNo(t *testing.T) {
	p, err := ParseReceiptText(shortItemNoFixture)
	if err != nil {
		t.Fatalf("短货号小票解析失败：%v", err)
	}
	if len(p.Items) != 2 {
		t.Fatalf("商品行 = %d，想要 2（3 位货号行不能漏）", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	qty := 0
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
		qty += it.Qty
	}
	rice, ok := byNo["402"]
	if !ok {
		t.Fatalf("3 位货号商品 402 没解析出来")
	}
	if rice.Name != "RICE 50LB" || rice.UnitPriceCents != 2699 {
		t.Errorf("402 品名/单价 = %q/%d，想要 RICE 50LB/2699", rice.Name, rice.UnitPriceCents)
	}
	if qty != 2 || p.ItemCount != 2 {
		t.Errorf("件数 = 解析 %d / 打印 %d，想要 2/2", qty, p.ItemCount)
	}
	if p.GrossCents != 4298 || p.SubtotalCents != 4298 || p.TotalCents != 4298 {
		t.Errorf("原价/小计/总计 = %d/%d/%d，想要 4298/4298/4298",
			p.GrossCents, p.SubtotalCents, p.TotalCents)
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := ParseReceiptText(""); err == nil {
		t.Errorf("空文本应返回错误")
	}
	if _, err := ParseReceiptText("hello world\nno receipt here\n"); err == nil {
		t.Errorf("没有商品行的文本应返回错误")
	}
}

func TestParseMoneyToCents(t *testing.T) {
	cases := map[string]int{
		"12.49": 1249, "3.79": 379, "$258.36": 25836,
		"12,49": 1249, "5": 500, "0.00": 0, "-5.00": -500,
		"1,008.43": 100843, "1,234,567.89": 123456789,
	}
	for in, want := range cases {
		got, ok := parseMoneyToCents(in)
		if !ok || got != want {
			t.Errorf("parseMoneyToCents(%q) = %d,%v，想要 %d", in, got, ok, want)
		}
	}
	if _, ok := parseMoneyToCents("abc"); ok {
		t.Errorf("非金额应解析失败")
	}
	if _, ok := parseMoneyToCents("12.345"); ok {
		t.Errorf("三位小数应解析失败")
	}
}

func TestSeedCSV(t *testing.T) {
	r, items, err := parseSeedCSV(seedCSV)
	if err != nil {
		t.Fatalf("种子 CSV 解析失败：%v", err)
	}
	if len(items) != 12 {
		t.Fatalf("种子商品行 = %d，想要 12", len(items))
	}
	if r.TotalCents != 25836 || r.SubtotalCents != 24384 || r.TaxCents != 1452 || r.SavingsCents != 3500 {
		t.Errorf("种子金额小计/税/总计/立减 = %d/%d/%d/%d，想要 24384/1452/25836/3500",
			r.SubtotalCents, r.TaxCents, r.TotalCents, r.SavingsCents)
	}
	if r.PurchasedAt != "2026-09-12 14:25" {
		t.Errorf("种子购买时间 = %q，想要 2026-09-12 14:25", r.PurchasedAt)
	}
	for _, it := range items {
		if it.ItemNo == "1000003" {
			if it.Category != "日用品" {
				t.Errorf("1000003 分类 = %q，想要 日用品（擦手纸）", it.Category)
			}
			if it.NetCents != 1799 {
				t.Errorf("1000003 实付 = %d，想要 1799", it.NetCents)
			}
		}
		if it.ItemNo == "1000008" {
			if it.Qty != 5 || it.NetCents != 9995 || it.Category != "运动" {
				t.Errorf("足球 qty/实付/分类 = %d/%d/%q，想要 5/9995/运动", it.Qty, it.NetCents, it.Category)
			}
		}
	}
}
