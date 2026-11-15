package main

// 种子数据：一张虚构的示例小票（Fremont #123，16 件，总计 $258.36，立减 $35.00，
// 数字已全部对账通过）。小票为空库时首次启动自动导入，开箱即有数据可看。
// CSV 由 //go:embed 打进二进制；里面的商品、价格、日期全是编的演示数据。

import (
	_ "embed"
	"encoding/csv"
	"log"
	"strconv"
	"strings"
)

//go:embed receipts/example-receipt.csv
var seedCSV string

// seedReceiptID 是种子小票的固定 id（可预测无妨：种子数据是公开的示例，
// 且应用本身在登录墙后）。
const seedReceiptID = "seed-example-001"

// seedIfEmpty 在小票表为空时导入种子小票；已有数据时什么都不做。
func (a *App) seedIfEmpty() {
	n, err := a.store.CountReceipts()
	if err != nil || n > 0 {
		return
	}
	r, items, err := parseSeedCSV(seedCSV)
	if err != nil {
		log.Printf("种子小票解析失败（跳过导入）: %v", err)
		return
	}
	if err := a.store.CreateReceipt(r, items); err != nil {
		log.Printf("种子小票导入失败: %v", err)
		return
	}
	log.Printf("种子小票已导入：%s %s，%d 件，总计 %s", r.Store, r.PurchasedAt, len(items), formatCents(r.TotalCents))
}

// parseSeedCSV 解析种子 CSV。列：
// date,store,store_no,item_no,name,qty,unit_price,discount_each,net,taxable,food_exempt,category_suggestion
// 末尾的 # 对账行跳过。购买时间 14:25 是示例小票设定的时间（CSV 里只有日期）。
func parseSeedCSV(data string) (*Receipt, []ReceiptItem, error) {
	lines := []string{}
	for _, ln := range strings.Split(data, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		lines = append(lines, ln)
	}
	if len(lines) < 2 {
		return nil, nil, errNotFound
	}
	rd := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true // 品名里有裸双引号（如 10" FL. TORT），容忍它
	records, err := rd.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	// 第一行是表头
	var items []ReceiptItem
	var store, storeNo, date string
	subtotal, tax, total, savings, count := 0, 0, 0, 0, 0
	for _, rec := range records[1:] {
		if len(rec) < 12 {
			continue
		}
		date, store, storeNo = rec[0], rec[1], rec[2]
		itemNo, name := rec[3], rec[4]
		qty, _ := strconv.Atoi(rec[5])
		unit, _ := parseMoneyToCents(rec[6])
		disc, _ := parseMoneyToCents(rec[7])
		net, _ := parseMoneyToCents(rec[8])
		category := rec[11]
		if !validCategory(category) {
			category = "其他"
		}
		items = append(items, ReceiptItem{
			ItemNo:         itemNo,
			Name:           name,
			Qty:            qty,
			UnitPriceCents: unit,
			DiscountCents:  disc,
			NetCents:       net,
			Taxable:        rec[9] == "A",
			FoodExempt:     rec[10] == "E",
			Category:       category,
		})
		subtotal += net
		savings += qty * disc
		count += qty
	}
	// 税与总计：CSV 里没有单列，用示例小票的数值（对账通过：278.84−35.00=243.84，税 14.52，总计 258.36）
	tax = 1452
	total = subtotal + tax
	r := &Receipt{
		ID:            seedReceiptID,
		Store:         store,
		StoreNo:       storeNo,
		PurchasedAt:   date + " 14:25",
		SubtotalCents: subtotal,
		TaxCents:      tax,
		TotalCents:    total,
		SavingsCents:  savings,
		ItemCount:     count,
	}
	return r, items, nil
}
