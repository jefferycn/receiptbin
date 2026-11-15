package main

// 调价与盯价提醒测试：用临时 SQLite 库，
// 以示例种子小票（09/12 足球 $19.99×5）为起点，模拟后续降价场景。

import (
	"testing"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	store, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore 失败：%v", err)
	}
	t.Cleanup(func() { store.Close() })
	app := &App{store: store, limiter: newLoginLimiter()}
	app.seedIfEmpty()
	return app
}

func mustReceipt(t *testing.T, app *App, id, purchasedAt string, items []ReceiptItem) *Receipt {
	t.Helper()
	subtotal := 0
	count := 0
	for _, it := range items {
		subtotal += it.NetCents
		count += it.Qty
	}
	r := &Receipt{
		ID: id, Store: "Fremont", StoreNo: "123", PurchasedAt: purchasedAt,
		SubtotalCents: subtotal, TaxCents: 0, TotalCents: subtotal,
		ItemCount: count,
	}
	if err := app.store.CreateReceipt(r, items); err != nil {
		t.Fatalf("CreateReceipt 失败：%v", err)
	}
	return r
}

func soccerItem(qty, unit, disc int) ReceiptItem {
	return ReceiptItem{
		ItemNo: "1000008", Name: "SOCCER BALL", Qty: qty,
		UnitPriceCents: unit, DiscountCents: disc, NetCents: qty * (unit - disc),
		Taxable: true, Category: "运动",
	}
}

func TestPriceDropAlert(t *testing.T) {
	app := testApp(t)
	// 种子小票：09/12 足球 $19.99×5。
	// 09/18 新小票：足球降到 $17.99 → 应生成 1 条调价提醒：
	// 差价 (1999−1799)×5 = $10.00，截止日 09/12+30 天 = 10/12。
	r := mustReceipt(t, app, "r2", "2026-09-18 15:00", []ReceiptItem{soccerItem(1, 1799, 0)})
	n := app.checkReceiptAlerts(r, []ReceiptItem{soccerItem(1, 1799, 0)})
	if n != 1 {
		t.Fatalf("新提醒数 = %d，想要 1", n)
	}
	alerts, err := app.store.ListAlerts("open")
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 {
		t.Fatalf("未处理提醒 = %d，想要 1", len(alerts))
	}
	a := alerts[0]
	if a.Kind != "price_drop" || a.ItemNo != "1000008" {
		t.Errorf("提醒 kind/item = %s/%s，想要 price_drop/1000008", a.Kind, a.ItemNo)
	}
	if a.OldPriceCents != 1999 || a.NewPriceCents != 1799 {
		t.Errorf("旧价/新价 = %d/%d，想要 1999/1799", a.OldPriceCents, a.NewPriceCents)
	}
	if a.DiffCents != 1000 || a.Qty != 5 {
		t.Errorf("差价/件数 = %d/%d，想要 1000/5", a.DiffCents, a.Qty)
	}
	if a.Deadline != "2026-10-12" {
		t.Errorf("截止日 = %q，想要 2026-10-12", a.Deadline)
	}
	if a.PurchaseDate != "2026-09-12" {
		t.Errorf("购买日 = %q，想要 2026-09-12", a.PurchaseDate)
	}
	// 重复跑一次不应产生重复提醒
	n = app.checkReceiptAlerts(r, []ReceiptItem{soccerItem(1, 1799, 0)})
	if n != 0 {
		t.Errorf("重复检查应产生 0 条新提醒，实际 %d", n)
	}
}

func TestPriceDropOutsideWindow(t *testing.T) {
	app := testApp(t)
	// 10/20 新小票（种子购买 09/12 已超过 30 天）：不应提醒。
	r := mustReceipt(t, app, "r2", "2026-10-20 15:00", []ReceiptItem{soccerItem(1, 1499, 0)})
	n := app.checkReceiptAlerts(r, []ReceiptItem{soccerItem(1, 1499, 0)})
	if n != 0 {
		t.Errorf("超出 30 天窗口应产生 0 条提醒，实际 %d", n)
	}
}

func TestPriceDropNoAlertWhenPriceUp(t *testing.T) {
	app := testApp(t)
	// 09/18 足球涨到 $21.99：不应提醒。
	r := mustReceipt(t, app, "r2", "2026-09-18 15:00", []ReceiptItem{soccerItem(1, 2199, 0)})
	n := app.checkReceiptAlerts(r, []ReceiptItem{soccerItem(1, 2199, 0)})
	if n != 0 {
		t.Errorf("涨价应产生 0 条提醒，实际 %d", n)
	}
}

func TestWatchlistAlerts(t *testing.T) {
	app := testApp(t)
	// 盯足球，目标价 $18.00。加盯时回填最近购买 $19.99。
	target := 1800
	if err := app.store.AddWatch("1000008", "SOCCER BALL", &target); err != nil {
		t.Fatal(err)
	}
	app.backfillWatchObservation("1000008")
	obs, err := app.store.LatestObservation("1000008")
	if err != nil || obs.PriceCents != 1999 {
		t.Fatalf("回填观察应为 $19.99，实际 %+v, err=%v", obs, err)
	}
	// 09/18 足球降到 $17.99：watch_drop + watch_target 各 1 条，
	// 再加 price_drop 1 条（09/12 买贵了），共 3 条。
	r := mustReceipt(t, app, "r2", "2026-09-18 15:00", []ReceiptItem{soccerItem(1, 1799, 0)})
	n := app.checkReceiptAlerts(r, []ReceiptItem{soccerItem(1, 1799, 0)})
	if n != 3 {
		t.Fatalf("新提醒数 = %d，想要 3（price_drop + watch_drop + watch_target）", n)
	}
	alerts, _ := app.store.ListAlerts("open")
	kinds := map[string]int{}
	for _, a := range alerts {
		kinds[a.Kind]++
	}
	if kinds["price_drop"] != 1 || kinds["watch_drop"] != 1 || kinds["watch_target"] != 1 {
		t.Errorf("提醒种类分布 = %v，想要各 1 条", kinds)
	}
	// 同价再来一张小票：不重复提醒
	r3 := mustReceipt(t, app, "r3", "2026-09-19 15:00", []ReceiptItem{soccerItem(1, 1799, 0)})
	n = app.checkReceiptAlerts(r3, []ReceiptItem{soccerItem(1, 1799, 0)})
	if n != 0 {
		t.Errorf("同价重复应产生 0 条新提醒，实际 %d", n)
	}
}

func TestManualPriceObservation(t *testing.T) {
	app := testApp(t)
	target := 1800
	if err := app.store.AddWatch("1000008", "SOCCER BALL", &target); err != nil {
		t.Fatal(err)
	}
	app.backfillWatchObservation("1000008")
	// 用户在店里看到足球 $18.99，随手记下（09/16）：
	// 低于上次观察 $19.99 → watch_drop；未达目标价 → 无 watch_target；
	// 低于 09/12 购买价 → price_drop（差价 (1999−1899)×5 = $5.00）。
	n := app.checkManualPrice("1000008", "SOCCER BALL", 1899, "Fremont", "2026-09-16")
	if n != 2 {
		t.Fatalf("新提醒数 = %d，想要 2（price_drop + watch_drop）", n)
	}
	alerts, _ := app.store.ListAlerts("open")
	for _, a := range alerts {
		if a.Kind == "price_drop" && a.DiffCents != 500 {
			t.Errorf("手动记价的调价差价 = %d，想要 500", a.DiffCents)
		}
		if a.Kind == "watch_target" {
			t.Errorf("未达目标价不应有 watch_target 提醒")
		}
	}
	// 手动记价也进了观察记录
	obs, err := app.store.LatestObservation("1000008")
	if err != nil || obs.PriceCents != 1899 || obs.Source != "manual" {
		t.Errorf("手动观察记录不对：%+v, err=%v", obs, err)
	}
}

func TestAlertStatusFlow(t *testing.T) {
	app := testApp(t)
	r := mustReceipt(t, app, "r2", "2026-09-18 15:00", []ReceiptItem{soccerItem(1, 1799, 0)})
	app.checkReceiptAlerts(r, []ReceiptItem{soccerItem(1, 1799, 0)})
	alerts, _ := app.store.ListAlerts("open")
	if len(alerts) != 1 {
		t.Fatalf("应有 1 条未处理提醒，实际 %d", len(alerts))
	}
	if err := app.store.SetAlertStatus(alerts[0].ID, "handled"); err != nil {
		t.Fatal(err)
	}
	open, _ := app.store.ListAlerts("open")
	if len(open) != 0 {
		t.Errorf("标记已处理后，未处理应为 0，实际 %d", len(open))
	}
	handled, _ := app.store.ListAlerts("handled")
	if len(handled) != 1 {
		t.Errorf("已处理应为 1，实际 %d", len(handled))
	}
}
