package main

// PDF 导入测试：官网导出小票的解析、会员号剥离、pdftotext 提文本。
// 夹具全是虚构的示例数据（Riverton #456），不含任何真实购物记录。
// 示例小票对账：原价 60.45 − 折扣 3.00（袜子单件折扣）= 小计 57.45，税 1.92，总计 59.37。

import (
	"encoding/json"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// exportFixture 是手写的官网导出小票文本（pdftotext -layout 风格）。
// 覆盖：E 标志、N/Y 税标、数量行、品名换行（续接与独占一行）、
// /NO SHOW 备注行、无货号折扣行（挂给上一件商品）、会员号（剥离用）、P7 12 小时制时间陷阱。
const exportFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
E                   3000001 GREEN TEA    12.49 N
                    3000002 PAPER        9.99 Y
                             NAPKINS
2 @ 4.50
E                   3000003 YOGURT CUPS  9.00 N
E                   3000004              7.99 N
                             BLUEBERRIES
                    3000005 SOCKS 6PK    11.99 Y
                             /NO SHOW
                    3000099              3.00-
                             SOC
E                   3000006 ORANGE JUICE 8.99 N
                             SUBTOTAL    57.45
                             TAX         1.92
                    ****     TOTAL       59.37


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $59.37
08/15/2026 16:40 456 3 123 789
VISA                                        59.37
CHANGE                                           0


                (A) A                       1.92
                TOTAL TAX                   1.92
TOTAL NUMBER OF ITEMS SOLD = 7
INSTANT SAVINGS                             $3.00
08/15/2026 16:40 456 3 123 789



                     Thank You!
                  Please Come Again

      Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 7
P7 08/15/2026 04:40
`

func TestParseExportReceipt(t *testing.T) {
	p, err := ParseExportReceiptText(exportFixture)
	if err != nil {
		t.Fatalf("导出小票解析失败：%v", err)
	}
	if p.Store != "RIVERTON" || p.StoreNo != "456" {
		t.Errorf("门店 = %q #%q，想要 RIVERTON #456", p.Store, p.StoreNo)
	}
	// 取第一个交易时间 16:40，不是末尾 P7 行的 12 小时制 04:40
	if p.PurchasedAt != "2026-08-15 16:40" {
		t.Errorf("购买时间 = %q，想要 2026-08-15 16:40", p.PurchasedAt)
	}
	// 交易号从页脚读出：Whse: 456 Trm: 3 Trn: 123 OPT: 789
	if p.TerminalNo != "3" || p.TransactionNo != "123" {
		t.Errorf("收银台/交易号 = %q/%q，想要 3/123", p.TerminalNo, p.TransactionNo)
	}
	if len(p.Items) != 6 {
		t.Fatalf("商品行 = %d，想要 6（折扣行不是商品）", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	qty := 0
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
		qty += it.Qty
	}
	if qty != 7 {
		t.Errorf("总件数 = %d，想要 7", qty)
	}
	// 品名续接：PAPER + NAPKINS；独占一行的 BLUEBERRIES
	if byNo["3000002"].Name != "PAPER NAPKINS" {
		t.Errorf("3000002 品名 = %q，想要 PAPER NAPKINS", byNo["3000002"].Name)
	}
	if byNo["3000004"].Name != "BLUEBERRIES" {
		t.Errorf("3000004 品名 = %q，想要 BLUEBERRIES", byNo["3000004"].Name)
	}
	// 数量展开：2 @ 4.50，小计 9.00 → 单价 4.50、数量 2、实付 9.00
	yog := byNo["3000003"]
	if yog.Qty != 2 || yog.UnitPriceCents != 450 || yog.NetCents != 900 {
		t.Errorf("酸奶 qty/单价/实付 = %d/%d/%d，想要 2/450/900", yog.Qty, yog.UnitPriceCents, yog.NetCents)
	}
	// N/Y 税标与 E 标志
	if !byNo["3000002"].Taxable || byNo["3000002"].FoodExempt {
		t.Errorf("PAPER NAPKINS 应为应税（Y）、非食品免税")
	}
	if !byNo["3000001"].FoodExempt || byNo["3000001"].Taxable {
		t.Errorf("GREEN TEA 应为食品免税（E）、非应税（N）")
	}
	// 无货号折扣挂给上一件商品：3.00 属于袜子（1030 实测），不是整单立减
	if p.DiscountCents != 300 || p.ReceiptDiscountCents != 0 {
		t.Errorf("折扣合计/整单立减 = %d/%d，想要 300/0", p.DiscountCents, p.ReceiptDiscountCents)
	}
	socks := byNo["3000005"]
	if socks.DiscountCents != 300 || socks.NetCents != 899 {
		t.Errorf("袜子 折扣/实付 = %d/%d，想要 300/899", socks.DiscountCents, socks.NetCents)
	}
	for _, it := range p.Items {
		if it.ItemNo != "3000005" && it.DiscountCents != 0 {
			t.Errorf("货号 %s 不应有单件折扣，实际 %d", it.ItemNo, it.DiscountCents)
		}
	}
	if _, ok := byNo["3000099"]; ok {
		t.Errorf("折扣行 3000099 不应成为商品")
	}
	if p.GrossCents != 6045 {
		t.Errorf("原价合计 = %d，想要 6045", p.GrossCents)
	}
	if p.SubtotalCents != 5745 {
		t.Errorf("小计 = %d，想要 5745", p.SubtotalCents)
	}
	if p.TaxCents != 192 {
		t.Errorf("税 = %d，想要 192", p.TaxCents)
	}
	if p.TotalCents != 5937 {
		t.Errorf("总计 = %d，想要 5937", p.TotalCents)
	}
	if p.SavingsCents != 300 {
		t.Errorf("立减 = %d，想要 300", p.SavingsCents)
	}
	if p.ItemCount != 7 {
		t.Errorf("打印件数 = %d，想要 7", p.ItemCount)
	}
	if p.TotalDerived {
		t.Errorf("TOTAL 行明明在，不应走推导")
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("导出小票对账应通过，问题：%v", problems)
	}
}

// exportFPrefixFixture：F 前缀商品（FSA 合格商品）与 3 位短货号回归夹具。
// 2026-11-14 真实小票：F 前缀行曾被商品正则漏掉；3 位短货号也曾被漏掉。数字全是虚构的。
// 对账：8.99 + 9.99 = 18.98，税 9.99×8.75% ≈ 0.87，总计 19.85。
const exportFPrefixFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
F                   4000001 BANDAGE ROLL 8.99 N
                    402 PAPER TOWEL     9.99 Y
                             SUBTOTAL    18.98
                             TAX         0.87
                    ****     TOTAL       19.85


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $19.85
08/15/2026 16:40 456 3 123 789
VISA                                        19.85
CHANGE                                           0


                (A) A                       0.87
                TOTAL TAX                   0.87
TOTAL NUMBER OF ITEMS SOLD = 2
08/15/2026 16:40 456 3 123 789



                     Thank You!
                  Please Come Again

      Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 2
P7 08/15/2026 04:40
`

// exportDiscountFixture：折扣行带 /货号 时挂给对应商品（2026-11-15 实测 1114.pdf 格式）。
// 原价 51.97 − 折扣 17.00（球 10.00 + 袜 5.00 + 整单 2.00）= 小计 34.97，税 3.23，总计 38.20。
const exportDiscountFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000

2 @ 19.99
                    3000010 BALL         39.98 Y
                    400530 /3000010      10.00-
                    3000011 SOCKS 6PK    11.99 Y
                    401104 /3000011       5.00-
                    3000099               2.00-
                             SUBTOTAL    34.97
                             TAX          3.23
                    ****     TOTAL       38.20
AMOUNT: $38.20
08/15/2026 16:40 456 3 123 789
VISA                                        38.20
TOTAL NUMBER OF ITEMS SOLD = 3
INSTANT SAVINGS                             $17.00
08/15/2026 16:40 456 3 123 789
      Whse: 456    Trm: 3   Trn: 123   OPT: 789
Items Sold: 3
P7 08/15/2026 04:40
`

func TestParseExportItemDiscounts(t *testing.T) {
	p, err := ParseExportReceiptText(exportDiscountFixture)
	if err != nil {
		t.Fatalf("导出小票解析失败：%v", err)
	}
	if len(p.Items) != 2 {
		t.Fatalf("商品行 = %d，想要 2（折扣行不是商品）", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	// 数量行 + 折扣：2 个球原价 39.98，折扣 10.00 → 每件折 5.00，实付 29.98
	ball := byNo["3000010"]
	if ball.Qty != 2 || ball.UnitPriceCents != 1999 || ball.DiscountCents != 500 || ball.NetCents != 2998 {
		t.Errorf("球 qty/单价/折扣/实付 = %d/%d/%d/%d，想要 2/1999/500/2998",
			ball.Qty, ball.UnitPriceCents, ball.DiscountCents, ball.NetCents)
	}
	socks := byNo["3000011"]
	if socks.DiscountCents != 500 || socks.NetCents != 699 {
		t.Errorf("袜子 折扣/实付 = %d/%d，想要 500/699", socks.DiscountCents, socks.NetCents)
	}
	// 折扣合计 17.00，其中只有无货号引用的 2.00 是整单立减
	if p.DiscountCents != 1700 || p.ReceiptDiscountCents != 200 {
		t.Errorf("折扣合计/整单立减 = %d/%d，想要 1700/200", p.DiscountCents, p.ReceiptDiscountCents)
	}
	if p.GrossCents != 5197 || p.SubtotalCents != 3497 || p.TotalCents != 3820 {
		t.Errorf("原价/小计/总计 = %d/%d/%d，想要 5197/3497/3820", p.GrossCents, p.SubtotalCents, p.TotalCents)
	}
	if problems, _ := Reconcile(p); len(problems) > 0 {
		t.Errorf("对账应通过，实际问题：%v", problems)
	}
}

const exportBFlagFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
                    4000002 GLUE GUN      29.99 B
                    388695 / 4000002      6.00-
                    402 PAPER TOWEL     9.99 Y
E                   4032 WATERMELON     5.99 N
                             SUBTOTAL    39.97
                             TAX         3.66
                    ****     TOTAL       43.63


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $43.63
08/15/2026 16:40 456 3 123 789
VISA                                        43.63
CHANGE                                           0


                (A) A                       3.30
                (B) B                       0.36
                TOTAL TAX                   3.66
TOTAL NUMBER OF ITEMS SOLD = 3
INSTANT SAVINGS                            $6.00
08/15/2026 16:40 456 3 123 789


                     Thank You!
                 Please Come Again

     Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 3
P7 08/15/2026 04:40
`

// B 类税商品不能漏：税码不止 N/Y，Concord 小票的胶枪标 B，页脚 (A)/(B) 分开计税。
// 旧正则只接受 [NY]，B 行整行被丢，连带它的 6 元折扣也挂不上。
const exportBareDiscountFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
                    4000003 SOCKS 10PK    9.99 Y
                             /NO SHOW
                    397261               2.00-
                             SOC
                    402 PAPER TOWEL     9.99 Y
                             SUBTOTAL    17.98
                             TAX         1.57
                    ****     TOTAL       19.55


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $19.55
08/15/2026 16:40 456 3 123 789
VISA                                        19.55
CHANGE                                           0


                (A) A                       1.57
                TOTAL TAX                   1.57
TOTAL NUMBER OF ITEMS SOLD = 2
INSTANT SAVINGS                            $2.00
08/15/2026 16:40 456 3 123 789


                     Thank You!
                 Please Come Again

     Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 2
P7 08/15/2026 04:40
`

// 不带货号引用的折扣行属于上一件商品（1030 实测：2.00- 跟在 KBELL 后面，
// 中间夹 /NO SHOW 备注行；旧逻辑一律按整单立减，错）。
const exportFeeFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
E                   4000004 SMOOTHIE      7.49 N
                    390171 /4000004       2.50-
                             CA REDEMP VAL N
                    6000001                0.10
                             EE/4000004
E                   4032 WATERMELON     5.99 N
                             SUBTOTAL    11.08
                             TAX         0.00
                    ****     TOTAL       11.08


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $11.08
08/15/2026 16:40 456 3 123 789
VISA                                        11.08
CHANGE                                           0


                TOTAL TAX                   0.00
TOTAL NUMBER OF ITEMS SOLD = 2
INSTANT SAVINGS                            $2.50
08/15/2026 16:40 456 3 123 789


                     Thank You!
                 Please Come Again

     Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 2
P7 08/15/2026 04:40
`

// CRV 押金行：EE/货号 标明上一件商品是押金，算钱不算件数。
// 1017 实测：押金 0.10 计入原价，但小票打印件数不含它，旧逻辑件数总多一件。
func TestParseExportCRVFee(t *testing.T) {
	p, err := ParseExportReceiptText(exportFeeFixture)
	if err != nil {
		t.Fatalf("押金小票解析失败：%v", err)
	}
	if len(p.Items) != 3 {
		t.Fatalf("商品行 = %d，想要 3（果昔 + 押金 + 西瓜）", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	fee, ok := byNo["6000001"]
	if !ok {
		t.Fatalf("押金行 6000001 没解析出来")
	}
	if !fee.IsFee {
		t.Errorf("押金行应标为费用（IsFee）")
	}
	if fee.Name != "CA REDEMP VAL" {
		t.Errorf("押金行品名 = %q，想要 CA REDEMP VAL（EE/ 行本身不能当品名）", fee.Name)
	}
	if fee.NetCents != 10 {
		t.Errorf("押金行实付 = %d，想要 10", fee.NetCents)
	}
	// 果昔的 2.50 折扣照常挂上
	if byNo["4000004"].DiscountCents != 250 || byNo["4000004"].NetCents != 499 {
		t.Errorf("果昔 折扣/实付 = %d/%d，想要 250/499",
			byNo["4000004"].DiscountCents, byNo["4000004"].NetCents)
	}
	if p.GrossCents != 1358 || p.SubtotalCents != 1108 || p.TotalCents != 1108 {
		t.Errorf("原价/小计/总计 = %d/%d/%d，想要 1358/1108/1108（含押金 0.10）",
			p.GrossCents, p.SubtotalCents, p.TotalCents)
	}
	// 件数：押金不算，解析 2 件 = 打印 2 件，不应有警告
	problems, warnings := Reconcile(p)
	if len(problems) != 0 || len(warnings) != 0 {
		t.Errorf("对账应通过且无警告，问题：%v 警告：%v", problems, warnings)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("解析不应有警告，实际：%v", p.Warnings)
	}
}

const exportInterleavedFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
E                   4000005 TEA BAGS      12.49 N
                    4000006 NAPKINS       11.99 Y
                             NUBZ DOG
                    4000007               15.99 Y
                             CHW
                             COCONUT WTR
E                   4000008               20.99 N
                             1L
E                   4000009 CABBAGE       2.99 N
F                   4000010 GLOVES        21.99 Y
E                   4000011 GREENS        6.99 N
                    391029 /4000011       2.00-
                             BOBS
E                   4000012               12.49 N
                             OATMEAL
                             SPICY
E                   4000013               13.99 N
                             WONTON
                             SUBTOTAL    117.91
                             TAX         5.00
                    ****     TOTAL       122.91


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $122.91
08/15/2026 16:40 456 3 123 789
VISA                                       122.91
CHANGE                                           0


                (A) A                       5.00
                TOTAL TAX                   5.00
TOTAL NUMBER OF ITEMS SOLD = 9
INSTANT SAVINGS                            $2.00
08/15/2026 16:40 456 3 123 789


                     Thank You!
                 Please Come Again

     Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 9
P7 08/15/2026 04:40
`

// 品名交错（0918 实测）：PDF 排版把下一件商品的品名提前了一行，
// 旧逻辑一律续接到上一件，导致品名串行（NAPKINS 沾上 NUBZ DOG、
// 狗咬胶沾上 KSCOCOWTR）；"1L" 这类数字开头续行还因正则不匹配被直接丢掉。
func TestParseExportInterleavedNames(t *testing.T) {
	p, err := ParseExportReceiptText(exportInterleavedFixture)
	if err != nil {
		t.Fatalf("交错品名小票解析失败：%v", err)
	}
	if len(p.Items) != 9 {
		t.Fatalf("商品行 = %d，想要 9", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	// 提前的品名属于下一件，不沾上一件
	if byNo["4000006"].Name != "NAPKINS" {
		t.Errorf("4000006 品名 = %q，想要 NAPKINS（NUBZ DOG 不属于它）", byNo["4000006"].Name)
	}
	if byNo["4000007"].Name != "NUBZ DOG CHW" {
		t.Errorf("4000007 品名 = %q，想要 NUBZ DOG CHW", byNo["4000007"].Name)
	}
	// 数字开头续行 "1L" 也要续接
	if byNo["4000008"].Name != "COCONUT WTR 1L" {
		t.Errorf("4000008 品名 = %q，想要 COCONUT WTR 1L", byNo["4000008"].Name)
	}
	// 孤儿续行 BOBS（折扣行之后）属于下一件无品名商品
	if byNo["4000012"].Name != "BOBS OATMEAL" {
		t.Errorf("4000012 品名 = %q，想要 BOBS OATMEAL", byNo["4000012"].Name)
	}
	if byNo["4000013"].Name != "SPICY WONTON" {
		t.Errorf("4000013 品名 = %q，想要 SPICY WONTON", byNo["4000013"].Name)
	}
	if byNo["4000011"].DiscountCents != 200 || byNo["4000011"].NetCents != 499 {
		t.Errorf("4000011 折扣/实付 = %d/%d，想要 200/499",
			byNo["4000011"].DiscountCents, byNo["4000011"].NetCents)
	}
	if p.GrossCents != 11991 || p.SubtotalCents != 11791 || p.TaxCents != 500 || p.TotalCents != 12291 {
		t.Errorf("原价/小计/税/总计 = %d/%d/%d/%d，想要 11991/11791/500/12291",
			p.GrossCents, p.SubtotalCents, p.TaxCents, p.TotalCents)
	}
	problems, warnings := Reconcile(p)
	if len(problems) != 0 || len(warnings) != 0 {
		t.Errorf("对账应通过且无警告，问题：%v 警告：%v", problems, warnings)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("解析不应有警告，实际：%v", p.Warnings)
	}
}

const exportUnresolvedRefFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
E                   4000020 CHICKEN      51.31 N
                    389506 /4000099      14.00-
                    389507 /4000098       1.00-
E                   4000021 MILK         12.99 N
                             SUBTOTAL    49.30
                             TAX         0.00
                    ****     TOTAL       49.30


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $49.30
08/15/2026 16:40 456 3 123 789
VISA                                        49.30
CHANGE                                           0


                TOTAL TAX                   0.00
TOTAL NUMBER OF ITEMS SOLD = 2
INSTANT SAVINGS                            $15.00
08/15/2026 16:40 456 3 123 789


                     Thank You!
                 Please Come Again

     Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 2
P7 08/15/2026 04:40
`

// 引用货号不在小票上时，折扣退回挂给上一件商品（0828 实测：/13664 不在小票上，
// 但 14.00- 紧跟在鸡胸肉后面，就是它的折扣）；只有前面没有任何商品时才算整单立减。
func TestParseExportUnresolvedRefFallsBackToPreviousItem(t *testing.T) {
	p, err := ParseExportReceiptText(exportUnresolvedRefFixture)
	if err != nil {
		t.Fatalf("引用未解析折扣小票解析失败：%v", err)
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	// 14.00 挂给鸡胸肉，不是整单立减
	chicken := byNo["4000020"]
	if chicken.DiscountCents != 1400 || chicken.NetCents != 3731 {
		t.Errorf("鸡胸肉 折扣/实付 = %d/%d，想要 1400/3731（/4000099 引用找不到时退回挂给上一件）",
			chicken.DiscountCents, chicken.NetCents)
	}
	// 第二条折扣前面没有商品（上一条折扣清掉了 lastItem），仍是整单立减
	if p.DiscountCents != 1500 || p.ReceiptDiscountCents != 100 {
		t.Errorf("折扣合计/整单立减 = %d/%d，想要 1500/100", p.DiscountCents, p.ReceiptDiscountCents)
	}
	// 警告只提 4000098（真正无处可挂的那条），不提已归属的 4000099
	for _, w := range p.Warnings {
		if strings.Contains(w, "4000099") {
			t.Errorf("不应有 4000099 的警告（已挂给鸡胸肉），实际：%v", p.Warnings)
		}
	}
	found := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "4000098") {
			found = true
		}
	}
	if !found {
		t.Errorf("应有 4000098 无处可挂的警告，实际：%v", p.Warnings)
	}
	problems, warnings := Reconcile(p)
	if len(problems) != 0 || len(warnings) != 0 {
		t.Errorf("对账应通过且无警告，问题：%v 警告：%v", problems, warnings)
	}
}

const exportThousandsFixture = `RIVERTON #456
1200 Sample Road
Riverton, CA 94000


21045600999999999999999

Member
123456789012
E                   4000030 TV           1,299.99 Y
                    4000031 SOCKS        11.99 Y
E                   4000032 WATER        0.00 N
                             SUBTOTAL    1,311.98
                             TAX         120.00
                    ****     TOTAL       1,431.98


                                         CHIP
XXXXXXXXXXXX4242                         read
APPROVED - PURCHASE
AMOUNT: $1,431.98
08/15/2026 16:40 456 3 123 789
VISA                                     1,431.98
CHANGE                                           0


                (A) A                     120.00
                TOTAL TAX                 120.00
TOTAL NUMBER OF ITEMS SOLD = 2
INSTANT SAVINGS                            $0.00
08/15/2026 16:40 456 3 123 789


                     Thank You!
                 Please Come Again

     Whse: 456    Trm: 3   Trn: 123   OPT: 789

Items Sold: 2
P7 08/15/2026 04:40
`

// 千分位逗号与 0.00 元商品（0629 实测）：TOTAL 1,008.43 曾被读成 8.43
// （逗号被当成小数点）；0.00 元的瓶装水收银机不计入件数，解析出 11 件对打印 10 件。
func TestParseExportThousandsAndZeroPrice(t *testing.T) {
	p, err := ParseExportReceiptText(exportThousandsFixture)
	if err != nil {
		t.Fatalf("千分位小票解析失败：%v", err)
	}
	if len(p.Items) != 3 {
		t.Fatalf("商品行 = %d，想要 3（电视 + 袜子 + 0 元水）", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	if byNo["4000030"].UnitPriceCents != 129999 {
		t.Errorf("电视单价 = %d，想要 129999（1,299.99 不是 299.99）", byNo["4000030"].UnitPriceCents)
	}
	if p.GrossCents != 131198 || p.SubtotalCents != 131198 || p.TaxCents != 12000 || p.TotalCents != 143198 {
		t.Errorf("原价/小计/税/总计 = %d/%d/%d/%d，想要 131198/131198/12000/143198",
			p.GrossCents, p.SubtotalCents, p.TaxCents, p.TotalCents)
	}
	// 件数：0.00 元的水不算，解析 2 件 = 打印 2 件，不应有警告
	problems, warnings := Reconcile(p)
	if len(problems) != 0 || len(warnings) != 0 {
		t.Errorf("对账应通过且无警告，问题：%v 警告：%v", problems, warnings)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("解析不应有警告，实际：%v", p.Warnings)
	}
}

// findAmounts 遇千分位逗号应读出完整金额，不把逗号当小数点。
func TestFindAmountsThousandsSeparator(t *testing.T) {
	amts := findAmounts("****    TOTAL           1,008.43")
	if len(amts) != 1 || amts[0] != 100843 {
		t.Errorf("findAmounts(1,008.43) = %v，想要 [100843]", amts)
	}
	// 小数点逗号（OCR 误读）照常按小数点处理
	amts = findAmounts("TOTAL 12,49")
	if len(amts) != 1 || amts[0] != 1249 {
		t.Errorf("findAmounts(12,49) = %v，想要 [1249]", amts)
	}
}

func TestParseExportBareDiscountAttachesToPreviousItem(t *testing.T) {
	p, err := ParseExportReceiptText(exportBareDiscountFixture)
	if err != nil {
		t.Fatalf("无货号折扣小票解析失败：%v", err)
	}
	if len(p.Items) != 2 {
		t.Fatalf("商品行 = %d，想要 2", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	socks := byNo["4000003"]
	if socks.DiscountCents != 200 || socks.NetCents != 799 {
		t.Errorf("袜子 折扣/实付 = %d/%d，想要 200/799（2.00 应挂给袜子，不是整单立减）",
			socks.DiscountCents, socks.NetCents)
	}
	if socks.Name != "SOCKS 10PK" {
		t.Errorf("袜子品名 = %q，想要 SOCKS 10PK（折扣后的 SOC 续行不是品名）", socks.Name)
	}
	if p.ReceiptDiscountCents != 0 {
		t.Errorf("整单立减 = %d，想要 0", p.ReceiptDiscountCents)
	}
	for _, w := range p.Warnings {
		if strings.Contains(w, "整单立减") {
			t.Errorf("不应有整单立减警告，实际：%v", p.Warnings)
		}
	}
	if problems, _ := Reconcile(p); len(problems) > 0 {
		t.Errorf("对账应通过，实际问题：%v", problems)
	}
}

func TestParseExportBFlagTaxable(t *testing.T) {
	p, err := ParseExportReceiptText(exportBFlagFixture)
	if err != nil {
		t.Fatalf("B 税码小票解析失败：%v", err)
	}
	if len(p.Items) != 3 {
		t.Fatalf("商品行 = %d，想要 3（B 税码行不能漏）", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
	}
	gun, ok := byNo["4000002"]
	if !ok {
		t.Fatalf("B 税码商品 4000002 没解析出来")
	}
	if gun.Name != "GLUE GUN" {
		t.Errorf("4000002 品名 = %q，想要 GLUE GUN", gun.Name)
	}
	// B 不是 N：算应税；折扣 6.00 应挂给它，实付 23.99
	if !gun.Taxable {
		t.Errorf("B 税码商品应为应税")
	}
	if gun.NetCents != 2399 {
		t.Errorf("4000002 实付 = %d，想要 2399（29.99 − 6.00 折扣）", gun.NetCents)
	}
	if !byNo["402"].Taxable {
		t.Errorf("PAPER TOWEL 应为应税（Y）")
	}
	if byNo["4032"].Taxable || !byNo["4032"].FoodExempt {
		t.Errorf("WATERMELON 应为食品免税、非应税")
	}
	if p.GrossCents != 4597 || p.SubtotalCents != 3997 || p.TaxCents != 366 || p.TotalCents != 4363 {
		t.Errorf("原价/小计/税/总计 = %d/%d/%d/%d，想要 4597/3997/366/4363",
			p.GrossCents, p.SubtotalCents, p.TaxCents, p.TotalCents)
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}

func TestParseExportFPrefixAndShortItemNo(t *testing.T) {
	p, err := ParseExportReceiptText(exportFPrefixFixture)
	if err != nil {
		t.Fatalf("F 前缀小票解析失败：%v", err)
	}
	if len(p.Items) != 2 {
		t.Fatalf("商品行 = %d，想要 2（F 前缀行与 3 位货号行都不能漏）", len(p.Items))
	}
	byNo := map[string]ParsedItem{}
	qty := 0
	for _, it := range p.Items {
		byNo[it.ItemNo] = it
		qty += it.Qty
	}
	band, ok := byNo["4000001"]
	if !ok {
		t.Fatalf("F 前缀商品 4000001 没解析出来")
	}
	if band.Name != "BANDAGE ROLL" {
		t.Errorf("4000001 品名 = %q，想要 BANDAGE ROLL", band.Name)
	}
	// F 不是 E：不算食品免税；N 后缀：不算应税
	if band.FoodExempt || band.Taxable {
		t.Errorf("F 前缀商品应为非食品免税、非应税")
	}
	if !byNo["402"].Taxable {
		t.Errorf("PAPER TOWEL 应为应税（Y）")
	}
	if byNo["402"].Name != "PAPER TOWEL" {
		t.Errorf("402 品名 = %q，想要 PAPER TOWEL（3 位货号不能漏）", byNo["402"].Name)
	}
	if qty != p.ItemCount || p.ItemCount != 2 {
		t.Errorf("件数 = 解析 %d / 打印 %d，想要 2/2", qty, p.ItemCount)
	}
	if p.GrossCents != 1898 || p.SubtotalCents != 1898 || p.TaxCents != 87 || p.TotalCents != 1985 {
		t.Errorf("原价/小计/税/总计 = %d/%d/%d/%d，想要 1898/1898/87/1985",
			p.GrossCents, p.SubtotalCents, p.TaxCents, p.TotalCents)
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}

func TestParseExportMemberStripped(t *testing.T) {
	// 夹具里的假会员号 123456789012 不得出现在解析结果的任何地方
	p, err := ParseExportReceiptText(exportFixture)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "123456789012") {
		t.Errorf("解析结果里出现了会员号")
	}
	// 提文本阶段就已剥离：raw_text 也不会有会员号
	clean := stripMemberNumbers(exportFixture)
	if strings.Contains(clean, "123456789012") || strings.Contains(clean, "Member") {
		t.Errorf("stripMemberNumbers 后仍有会员号内容")
	}
}

func TestStripMemberNumbers(t *testing.T) {
	in := "RIVERTON #456\nMember\n123456789012\n21045600999999999999999\nE 1 ITEM 1.00 N\n"
	out := stripMemberNumbers(in)
	if strings.Contains(out, "123456789012") || strings.Contains(out, "Member") {
		t.Errorf("会员号行没去掉：%q", out)
	}
	// 23 位交易流水号不是会员号，必须保留
	if !strings.Contains(out, "21045600999999999999999") {
		t.Errorf("交易流水号被误删：%q", out)
	}
	// 同一行写法 "Member 123456789012" 也要去掉数字
	out2 := stripMemberNumbers("Member 123456789012\nE 1 ITEM 1.00 N\n")
	if strings.Contains(out2, "123456789012") {
		t.Errorf("行内会员号没去掉：%q", out2)
	}
}

func TestParseExportStoreFallback(t *testing.T) {
	// 页眉门店行缺失时，用页脚 Whse 号兜底
	variant := strings.Replace(exportFixture, "RIVERTON #456\n", "", 1)
	p, err := ParseExportReceiptText(variant)
	if err != nil {
		t.Fatal(err)
	}
	if p.StoreNo != "456" {
		t.Errorf("门店号 = %q，想要 456（Whse 兜底）", p.StoreNo)
	}
	if p.Store == "" {
		t.Errorf("门店名不应为空（应有 Costco 占位）")
	}
}

func TestParseExportEmpty(t *testing.T) {
	if _, err := ParseExportReceiptText(""); err == nil {
		t.Errorf("空文本应返回错误")
	}
	if _, err := ParseExportReceiptText("hello world\nno receipt here\n"); err == nil {
		t.Errorf("没有商品行的文本应返回错误")
	}
}

func TestExtractPDFMissingBinary(t *testing.T) {
	old := pdftotextBin
	pdftotextBin = "/nonexistent/pdftotext-for-test"
	t.Cleanup(func() { pdftotextBin = old })
	_, err := ExtractPDFText("whatever.pdf")
	if err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("应报 pdftotext 缺失，实际 err=%v", err)
	}
}

func requirePdftotext(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(pdftotextBin); err != nil {
		t.Skip("未安装 pdftotext，跳过")
	}
}

func TestExtractPDFEmptyText(t *testing.T) {
	requirePdftotext(t)
	// 空白页 PDF：提不出文本，应报明确错误，让用户改传照片
	path := makeTextPDF(t, nil)
	_, err := ExtractPDFText(path)
	if err == nil || !strings.Contains(err.Error(), "照片") {
		t.Fatalf("空白 PDF 应报“没有可读文字、请传照片”，实际 err=%v", err)
	}
}

func TestExtractPDFEndToEnd(t *testing.T) {
	requirePdftotext(t)
	// 把示例导出小票生成真正的 PDF，走完整链路：提文本 → 解析 → 对账
	lines := strings.Split(strings.Trim(exportFixture, "\n"), "\n")
	path := makeTextPDF(t, lines)
	text, err := ExtractPDFText(path)
	if err != nil {
		t.Fatalf("提文本失败：%v", err)
	}
	if strings.Contains(text, "123456789012") {
		t.Fatalf("提文本后仍有会员号")
	}
	p, err := ParseExportReceiptText(text)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if p.Store != "RIVERTON" || p.StoreNo != "456" {
		t.Errorf("门店 = %q #%q，想要 RIVERTON #456", p.Store, p.StoreNo)
	}
	if p.PurchasedAt != "2026-08-15 16:40" {
		t.Errorf("购买时间 = %q，想要 2026-08-15 16:40", p.PurchasedAt)
	}
	if len(p.Items) != 6 || p.TotalCents != 5937 || p.SubtotalCents != 5745 {
		t.Errorf("商品行/小计/总计 = %d/%d/%d，想要 6/5745/5937", len(p.Items), p.SubtotalCents, p.TotalCents)
	}
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}

// makeTextPDF 生成一页纯文本 PDF（每行一个 Tj），返回临时文件路径。
// 不写 xref 表，poppler 会重建；只用于测试 ExtractPDFText。
func makeTextPDF(t *testing.T, lines []string) string {
	t.Helper()
	var content strings.Builder
	content.WriteString("BT /F1 12 Tf 36 750 Td\n")
	for i, ln := range lines {
		if i > 0 {
			content.WriteString("0 -15 Td\n")
		}
		content.WriteString("(" + pdfEscape(ln) + ") Tj\n")
	}
	content.WriteString("ET\n")
	stream := content.String()
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	b.WriteString("1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n")
	b.WriteString("2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n")
	b.WriteString("3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj\n")
	b.WriteString("4 0 obj << /Length " + strconv.Itoa(len(stream)) + " >> stream\n" + stream + "endstream\nendobj\n")
	b.WriteString("5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj\n")
	b.WriteString("trailer << /Root 1 0 R /Size 6 >>\n%%EOF\n")
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// pdfEscape 转义 PDF 字符串里的特殊字符。
func pdfEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "(", "\\(")
	s = strings.ReplaceAll(s, ")", "\\)")
	return s
}

// 照片格式的文本层 PDF（iPhone 扫描件自带的 OCR 文本层，内容是纸质小票格式）：
// 导出格式解析不出（或对账不过），应改走照片格式解析，并标记需要货号纠错。
func TestParsePDFPhotoFormatTextLayer(t *testing.T) {
	requirePdftotext(t)
	lines := strings.Split(strings.Trim(cleanFixture, "\n"), "\n")
	path := makeTextPDF(t, lines)
	p, _, needsSnap, err := ParsePDFReceipt(path)
	if err != nil {
		t.Fatalf("照片格式文本层 PDF 解析失败：%v", err)
	}
	if !needsSnap {
		t.Errorf("照片格式路径应标记需要货号纠错")
	}
	assertReceipt(t, p)
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}

func requirePdftoppm(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(pdftoppmBin); err != nil {
		t.Skip("未安装 pdftoppm，跳过")
	}
}

func requireTesseract(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(tesseractBin); err != nil {
		t.Skip("未安装 tesseract，跳过")
	}
}

// 纯图片 PDF（扫描件，无文本层）：应渲染成图再 OCR，解析结果与照片导入一致。
func TestParsePDFImageOnly(t *testing.T) {
	requirePdftotext(t)
	requirePdftoppm(t)
	requireTesseract(t)
	p, _, needsSnap, err := ParsePDFReceipt("testdata/image_receipt.pdf")
	if err != nil {
		t.Fatalf("图片 PDF 解析失败：%v", err)
	}
	if !needsSnap {
		t.Errorf("图片 PDF 路径应标记需要货号纠错")
	}
	assertReceipt(t, p)
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}

// 空白 PDF：文本层没有、渲染出来也没有文字，应报明确错误而不是假装成功。
func TestParsePDFBlank(t *testing.T) {
	requirePdftotext(t)
	requirePdftoppm(t)
	path := makeTextPDF(t, nil)
	_, _, _, err := ParsePDFReceipt(path)
	if err == nil {
		t.Fatalf("空白 PDF 应报错")
	}
}

// 高页图片 PDF（iPhone 存的 PDF 页面能有 45 英寸高）：渲染宽度必须封顶，
// 否则 300dpi 渲染出巨图、渲染本身就超时（反代先 504）。断言渲染出的
// PNG 宽度不超过 2000，且整条管线能解析成功。
func TestParsePDFTallImagePage(t *testing.T) {
	requirePdftotext(t)
	requirePdftoppm(t)
	requireTesseract(t)
	dir, pages, err := renderPDFPages("testdata/tall_image_receipt.pdf", pdfRenderMaxPages)
	if err != nil {
		t.Fatalf("渲染高页 PDF 失败：%v", err)
	}
	defer os.RemoveAll(dir)
	f, err := os.Open(pages[0])
	if err != nil {
		t.Fatalf("打开渲染图失败：%v", err)
	}
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		t.Fatalf("读渲染图尺寸失败：%v", err)
	}
	if cfg.Width > 2000 {
		t.Errorf("渲染图宽度 %d 超过封顶 2000", cfg.Width)
	}
	p, _, needsSnap, err := ParsePDFReceipt("testdata/tall_image_receipt.pdf")
	if err != nil {
		t.Fatalf("高页图片 PDF 解析失败：%v", err)
	}
	if !needsSnap {
		t.Errorf("图片 PDF 路径应标记需要货号纠错")
	}
	assertReceipt(t, p)
	problems, _ := Reconcile(p)
	if len(problems) != 0 {
		t.Errorf("对账应通过，问题：%v", problems)
	}
}
