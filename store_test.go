package main

// 门店名大小写归一（v1.6）测试：筛选下拉按门店号分组、旧数据迁移。
// 夹具全是虚构的示例门店名，不含任何真实购物记录。

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// insertRawReceipt 绕过 CreateReceipt 的归一，直接写一行指定大小写的门店名
// （模拟 v1.6 之前的旧数据）。
func insertRawReceipt(t *testing.T, app *App, id, store, storeNo string) {
	t.Helper()
	_, err := app.store.db.Exec(
		`INSERT INTO receipts (id, store, store_no, purchased_at, created_at, updated_at)
		 VALUES (?, ?, ?, '2026-09-12 14:25', '2026-09-12 14:25', '2026-09-12 14:25')`,
		id, store, storeNo)
	if err != nil {
		t.Fatalf("插入测试小票失败：%v", err)
	}
}

func TestListStoresGroupsByNumber(t *testing.T) {
	app := testApp(t)
	// 同一门店号、大小写不同的店名（旧数据混写）
	insertRawReceipt(t, app, "t1", "DANVILLE", "21")
	insertRawReceipt(t, app, "t2", "Danville", "21")
	insertRawReceipt(t, app, "t3", "Concord", "663")

	stores, err := app.store.ListStores()
	if err != nil {
		t.Fatalf("ListStores 失败：%v", err)
	}
	byNo := map[string][]string{}
	for _, s := range stores {
		byNo[s.StoreNo] = append(byNo[s.StoreNo], s.Store)
	}
	// #21 只出现一次，且是大写
	if len(byNo["21"]) != 1 || byNo["21"][0] != "DANVILLE" {
		t.Errorf("#21 门店 = %v，想要 [DANVILLE]（只出现一次）", byNo["21"])
	}
	if len(byNo["663"]) != 1 || byNo["663"][0] != "CONCORD" {
		t.Errorf("#663 门店 = %v，想要 [CONCORD]", byNo["663"])
	}
	for _, s := range stores {
		if s.Store != strings.ToUpper(s.Store) {
			t.Errorf("门店名 %q 未大写", s.Store)
		}
	}
}

func TestMigrateUppercaseStores(t *testing.T) {
	app := testApp(t)
	insertRawReceipt(t, app, "m1", "Danville", "21")
	if err := migrateUppercaseStores(app.store.db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	var name string
	if err := app.store.db.QueryRow(`SELECT store FROM receipts WHERE id = 'm1'`).Scan(&name); err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if name != "DANVILLE" {
		t.Errorf("迁移后店名 = %q，想要 DANVILLE", name)
	}
	// 幂等：再跑一次不应报错、不应改动
	if err := migrateUppercaseStores(app.store.db); err != nil {
		t.Fatalf("迁移第二次失败：%v", err)
	}
}

func TestListMonthsReturnsDistinctDesc(t *testing.T) {
	app := testApp(t)
	// 三张小票分属两个月（虚构日期），其中一个月两张
	for _, r := range []struct{ id, at string }{
		{"m1", "2026-09-12 14:25"}, {"m2", "2026-11-14 16:40"}, {"m3", "2026-09-04 10:00"},
	} {
		_, err := app.store.db.Exec(
			`INSERT INTO receipts (id, store, store_no, purchased_at, created_at, updated_at)
			 VALUES (?, 'FREMONT', '123', ?, ?, ?)`, r.id, r.at, r.at, r.at)
		if err != nil {
			t.Fatalf("插入测试小票失败：%v", err)
		}
	}
	months, err := app.store.ListMonths()
	if err != nil {
		t.Fatalf("ListMonths 失败：%v", err)
	}
	if len(months) != 2 || months[0] != "2026-11" || months[1] != "2026-09" {
		t.Errorf("月份 = %v，想要 [2026-11 2026-09]（去重、倒序）", months)
	}
}

func TestFindDuplicateReceiptMatchesByDay(t *testing.T) {
	app := testApp(t)
	// 用 999 号店避开种子数据；insertRawReceipt 的时间是 2026-09-12 14:25
	insertRawReceipt(t, app, "d1", "FREMONT", "999")
	// 同日不同时刻（照片 P7 行 12 小时制读出的 04:40）也应判重
	dup, err := app.store.FindDuplicateReceipt("999", "2026-09-12 04:40", 0, "", "")
	if err != nil || dup == nil || dup.ID != "d1" {
		t.Errorf("同日不同时刻应判重，dup=%v err=%v", dup, err)
	}
	// 不同日期不判重
	if dup, err := app.store.FindDuplicateReceipt("999", "2026-09-13 14:25", 0, "", ""); err == nil {
		t.Errorf("不同日期不应判重，dup=%v", dup)
	}
	// 同日同总价但不同门店不判重
	if dup, err := app.store.FindDuplicateReceipt("998", "2026-09-12 14:25", 0, "", ""); err == nil {
		t.Errorf("不同门店不应判重，dup=%v", dup)
	}
}

// insertRawReceiptTxn 写一行带收银台号与交易号的小票（判重测试用）。
func insertRawReceiptTxn(t *testing.T, app *App, id, store, storeNo, terminal, transaction string) {
	t.Helper()
	_, err := app.store.db.Exec(
		`INSERT INTO receipts (id, store, store_no, purchased_at, terminal_no, transaction_no, created_at, updated_at)
		 VALUES (?, ?, ?, '2026-09-12 14:25', ?, ?, '2026-09-12 14:25', '2026-09-12 14:25')`,
		id, store, storeNo, terminal, transaction)
	if err != nil {
		t.Fatalf("插入测试小票失败：%v", err)
	}
}

func TestFindDuplicateReceiptByTxn(t *testing.T) {
	app := testApp(t)
	insertRawReceiptTxn(t, app, "x1", "FREMONT", "999", "6", "487")
	// 交易号对上就是同一张，哪怕总价在确认页被改过（0 → 9999）
	dup, err := app.store.FindDuplicateReceipt("999", "2026-09-12 16:40", 9999, "6", "487")
	if err != nil || dup == nil || dup.ID != "x1" {
		t.Errorf("交易号对上应判重，dup=%v err=%v", dup, err)
	}
	// 交易号不同、总价相同：仍按总价判重（老数据没有交易号时靠总价兜底）
	dup, err = app.store.FindDuplicateReceipt("999", "2026-09-12 16:40", 0, "6", "488")
	if err != nil || dup == nil || dup.ID != "x1" {
		t.Errorf("交易号不同但总价相同应判重，dup=%v err=%v", dup, err)
	}
	// 交易号与总价都不同：不判重
	if dup, err := app.store.FindDuplicateReceipt("999", "2026-09-12 16:40", 9999, "6", "488"); err == nil {
		t.Errorf("交易号与总价都不同不应判重，dup=%v", dup)
	}
	// 不同日期、交易号相同：不判重（交易号每天从头编）
	if dup, err := app.store.FindDuplicateReceipt("999", "2026-09-13 16:40", 9999, "6", "487"); err == nil {
		t.Errorf("不同日期不应判重，dup=%v", dup)
	}
}

// ListReceipts 回归测试（v2.4.1 教训）：前缀版列清单漏了 terminal_no /
// transaction_no 两列，scanReceipt 扫 14 个字段只拿到 12 列，首页报数据库错误。
// 之前没有任何测试调过 ListReceipts，这条必须补上。
func TestListReceipts(t *testing.T) {
	app := testApp(t)
	rec := &Receipt{ID: "lr1", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		TerminalNo: "6", TransactionNo: "487",
		SubtotalCents: 1000, TaxCents: 88, TotalCents: 1088, ItemCount: 1}
	items := []ReceiptItem{{ItemNo: "3000001", Name: "TEST ITEM", Qty: 1,
		UnitPriceCents: 1000, NetCents: 1000, Category: "日用品"}}
	if err := app.store.CreateReceipt(rec, items); err != nil {
		t.Fatalf("建小票失败：%v", err)
	}
	// 按门店号筛，避开种子数据
	list, total, err := app.store.ListReceipts(ReceiptFilter{StoreNo: "999"})
	if err != nil {
		t.Fatalf("ListReceipts 失败：%v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("应查到 1 张小票，total=%d len=%d", total, len(list))
	}
	got := list[0]
	if got.ID != "lr1" || got.TotalCents != 1088 || got.TerminalNo != "6" || got.TransactionNo != "487" {
		t.Errorf("查出的小票字段不对：%+v", got)
	}
	// 不带筛选也应正常（首页最近小票就是这么查的）
	all, total, err := app.store.ListReceipts(ReceiptFilter{PerPage: 10})
	if err != nil {
		t.Fatalf("无筛选 ListReceipts 失败：%v", err)
	}
	if total < 2 || len(all) < 2 {
		t.Errorf("无筛选应至少查到 2 张（种子 + 新建），total=%d len=%d", total, len(all))
	}
}

func TestReplaceReceipt(t *testing.T) {
	app := testApp(t)
	old := &Receipt{ID: "old1", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		SubtotalCents: 1000, TaxCents: 88, TotalCents: 1088, ItemCount: 1}
	oldItems := []ReceiptItem{{ItemNo: "3000001", Name: "OLD ITEM", Qty: 1,
		UnitPriceCents: 1000, NetCents: 1000, Category: "日用品"}}
	if err := app.store.CreateReceipt(old, oldItems); err != nil {
		t.Fatalf("建旧小票失败：%v", err)
	}
	// 旧小票挂一条价格观察，覆盖后应随旧小票一起删掉
	if _, err := app.store.AddObservation(&Observation{ItemNo: "3000001", PriceCents: 1000,
		Store: "FREMONT", ObservedOn: "2026-09-12", Source: "receipt", ReceiptID: "old1"}); err != nil {
		t.Fatalf("建旧观察失败：%v", err)
	}
	newRec := &Receipt{ID: "new1", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		TerminalNo: "6", TransactionNo: "487",
		SubtotalCents: 2000, TaxCents: 175, TotalCents: 2175, ItemCount: 1}
	newItems := []ReceiptItem{{ItemNo: "3000002", Name: "NEW ITEM", Qty: 1,
		UnitPriceCents: 2000, NetCents: 2000, Category: "生鲜"}}
	if err := app.store.ReplaceReceipt("old1", newRec, newItems); err != nil {
		t.Fatalf("覆盖失败：%v", err)
	}
	if _, err := app.store.GetReceipt("old1"); err == nil {
		t.Errorf("旧小票应已被删掉")
	}
	got, err := app.store.GetReceipt("new1")
	if err != nil {
		t.Fatalf("新小票读不出来：%v", err)
	}
	if got.TotalCents != 2175 || got.TransactionNo != "487" {
		t.Errorf("新小票总价/交易号 = %d/%q，想要 2175/487", got.TotalCents, got.TransactionNo)
	}
	gotItems, err := app.store.GetReceiptItems("new1")
	if err != nil || len(gotItems) != 1 || gotItems[0].ItemNo != "3000002" {
		t.Errorf("新小票商品 = %v，想要 1 件 3000002", gotItems)
	}
	// 旧观察已删
	obs, _ := app.store.LatestObservation("3000001")
	if obs != nil && obs.ReceiptID == "old1" {
		t.Errorf("旧小票的价格观察应随覆盖删掉")
	}
	// 覆盖不存在的小票应报错，且新小票不能入库
	bad := &Receipt{ID: "new2", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25"}
	if err := app.store.ReplaceReceipt("missing", bad, newItems); err == nil {
		t.Errorf("覆盖不存在的小票应报错")
	}
	if _, err := app.store.GetReceipt("new2"); err == nil {
		t.Errorf("覆盖失败时新小票不应入库（事务应回滚）")
	}
}

// 旧库升级回归测试（v2.4 实测教训）：用没有 terminal_no / transaction_no
// 两列的旧表结构建库，openStore 必须成功启动并补齐列与索引，而不是崩溃。
// 新库测试测不出这个问题——新库建表时列已存在，索引能直接建好。
func TestOpenStoreUpgradesOldDB(t *testing.T) {
	dir := t.TempDir()
	dsn := fmt.Sprintf("file:%s/receiptbin.db?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", dir)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// 旧版 receipts 表：没有 terminal_no / transaction_no 两列
	_, err = db.Exec(`CREATE TABLE receipts (
		id TEXT PRIMARY KEY, store TEXT NOT NULL DEFAULT '', store_no TEXT NOT NULL DEFAULT '',
		purchased_at TEXT NOT NULL, subtotal_cents INTEGER NOT NULL DEFAULT 0,
		tax_cents INTEGER NOT NULL DEFAULT 0, total_cents INTEGER NOT NULL DEFAULT 0,
		savings_cents INTEGER NOT NULL DEFAULT 0, item_count INTEGER NOT NULL DEFAULT 0,
		photo_path TEXT NOT NULL DEFAULT '', raw_text TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := openStore(dir)
	if err != nil {
		t.Fatalf("旧库升级失败：%v", err)
	}
	defer store.Close()
	rows, err := store.db.Query(`PRAGMA table_info(receipts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		have[name] = true
	}
	if !have["terminal_no"] || !have["transaction_no"] {
		t.Errorf("迁移后应有 terminal_no / transaction_no 列，实际 %v", have)
	}
	// 索引也应建好
	var idxName string
	err = store.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_receipts_txn'`).Scan(&idxName)
	if err != nil {
		t.Errorf("迁移后应有 idx_receipts_txn 索引：%v", err)
	}
}

func TestValidCategoryGiftCard(t *testing.T) {
	// v1.7 新增分类：礼品卡（用户要求）；"其他" 仍为默认分类，留在最后一位
	if !validCategory("礼品卡") {
		t.Errorf("礼品卡 应为合法分类")
	}
	if Categories[len(Categories)-1] != "其他" {
		t.Errorf("最后一个分类应为 其他，实际 %q", Categories[len(Categories)-1])
	}
}

// 退货清单（v2.13）：加入、改数量、标记已处理、全部已处理、移除。
func TestReturnsLifecycle(t *testing.T) {
	app := testApp(t)
	rec := &Receipt{ID: "rt1", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		SubtotalCents: 4500, TaxCents: 0, TotalCents: 4500, ItemCount: 5}
	items := []ReceiptItem{{ItemNo: "3000010", Name: "TEST BALL", Qty: 5,
		UnitPriceCents: 1000, DiscountCents: 100, NetCents: 4500, Category: "运动"}}
	if err := app.store.CreateReceipt(rec, items); err != nil {
		t.Fatalf("建小票失败：%v", err)
	}
	got, err := app.store.GetReceiptItems("rt1")
	if err != nil || len(got) != 1 {
		t.Fatalf("取商品行失败：%v", err)
	}
	itemID := got[0].ID

	// 加入：qty 0 按购买数量，退款额 = 5 × (1000-100)
	rt, err := app.store.AddReturn(itemID, 0)
	if err != nil {
		t.Fatalf("AddReturn 失败：%v", err)
	}
	if rt.Qty != 5 || rt.RefundCents != 4500 || rt.Status != "pending" {
		t.Errorf("退货记录 = %+v，想要 qty 5 / refund 4500 / pending", rt)
	}
	// 重复加入只改数量，不建第二条
	rt2, err := app.store.AddReturn(itemID, 2)
	if err != nil {
		t.Fatalf("重复 AddReturn 失败：%v", err)
	}
	if rt2.ID != rt.ID || rt2.Qty != 2 {
		t.Errorf("重复加入 = %+v，想要同 id 且 qty 2", rt2)
	}
	// 数量夹在 1..购买数量
	if rt3, err := app.store.SetReturnQty(rt.ID, 99); err != nil || rt3.Qty != 5 {
		t.Errorf("SetReturnQty 99 = %+v, %v，想要 qty 5", rt3, err)
	}
	if rt3, err := app.store.SetReturnQty(rt.ID, 0); err != nil || rt3.Qty != 1 {
		t.Errorf("SetReturnQty 0 = %+v, %v，想要 qty 1", rt3, err)
	}
	// 小票详情用的待退映射
	m, err := app.store.PendingReturnMap("rt1")
	if err != nil || m[itemID].ID != rt.ID {
		t.Errorf("PendingReturnMap = %v, %v", m, err)
	}
	// 标记已处理 → 待退为空、已处理有一条；恢复后回来
	if err := app.store.SetReturnStatus(rt.ID, "done"); err != nil {
		t.Fatalf("SetReturnStatus 失败：%v", err)
	}
	if list, _ := app.store.ListReturns("pending"); len(list) != 0 {
		t.Errorf("已处理后待退应为空，实际 %d 条", len(list))
	}
	if list, _ := app.store.ListReturns("done"); len(list) != 1 || list[0].DoneAt == "" {
		t.Errorf("已处理列表 = %+v", list)
	}
	if err := app.store.SetReturnStatus(rt.ID, "pending"); err != nil {
		t.Fatalf("恢复失败：%v", err)
	}
	// 全部已处理
	n, err := app.store.MarkAllReturnsDone()
	if err != nil || n != 1 {
		t.Errorf("MarkAllReturnsDone = %d, %v，想要 1", n, err)
	}
	// 移除
	if err := app.store.RemoveReturn(rt.ID); err != nil {
		t.Fatalf("RemoveReturn 失败：%v", err)
	}
	if list, _ := app.store.ListReturns("done"); len(list) != 0 {
		t.Errorf("移除后应为空，实际 %d 条", len(list))
	}
	// 不存在的商品行
	if _, err := app.store.AddReturn(999999, 1); err == nil {
		t.Errorf("不存在的商品行应报错")
	}
}

// 覆盖保存小票时，待退货记录按货号带到新小票；货号没了就丢弃。
func TestReturnsCarryOverOnReplace(t *testing.T) {
	app := testApp(t)
	old := &Receipt{ID: "rc1", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		SubtotalCents: 3000, TotalCents: 3000, ItemCount: 3}
	oldItems := []ReceiptItem{{ItemNo: "3000011", Name: "TEST MUG", Qty: 3,
		UnitPriceCents: 1000, NetCents: 3000, Category: "厨房用品"}}
	if err := app.store.CreateReceipt(old, oldItems); err != nil {
		t.Fatalf("建旧小票失败：%v", err)
	}
	got, _ := app.store.GetReceiptItems("rc1")
	if _, err := app.store.AddReturn(got[0].ID, 2); err != nil {
		t.Fatalf("AddReturn 失败：%v", err)
	}
	newRec := &Receipt{ID: "rc2", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		SubtotalCents: 3000, TotalCents: 3000, ItemCount: 3}
	newItems := []ReceiptItem{{ItemNo: "3000011", Name: "TEST MUG", Qty: 3,
		UnitPriceCents: 1000, NetCents: 3000, Category: "厨房用品"}}
	if err := app.store.ReplaceReceipt("rc1", newRec, newItems); err != nil {
		t.Fatalf("覆盖失败：%v", err)
	}
	m, err := app.store.PendingReturnMap("rc2")
	if err != nil || len(m) != 1 {
		t.Fatalf("覆盖后待退记录应带过来，m=%v err=%v", m, err)
	}
	for _, rt := range m {
		if rt.Qty != 2 || rt.ItemNo != "3000011" {
			t.Errorf("带过来的记录 = %+v，想要货号 3000011 / qty 2", rt)
		}
	}
	// 再覆盖成没有这个货号的小票：记录丢弃
	newRec2 := &Receipt{ID: "rc3", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		SubtotalCents: 1000, TotalCents: 1000, ItemCount: 1}
	newItems2 := []ReceiptItem{{ItemNo: "3000012", Name: "OTHER", Qty: 1,
		UnitPriceCents: 1000, NetCents: 1000, Category: "其他"}}
	if err := app.store.ReplaceReceipt("rc2", newRec2, newItems2); err != nil {
		t.Fatalf("二次覆盖失败：%v", err)
	}
	if list, _ := app.store.ListReturns("pending"); len(list) != 0 {
		t.Errorf("货号没了待退记录应丢弃，实际 %d 条", len(list))
	}
}

// 删除小票时退货记录一起删。
func TestReturnsDeletedWithReceipt(t *testing.T) {
	app := testApp(t)
	rec := &Receipt{ID: "rd1", Store: "FREMONT", StoreNo: "999", PurchasedAt: "2026-09-12 14:25",
		SubtotalCents: 1000, TotalCents: 1000, ItemCount: 1}
	items := []ReceiptItem{{ItemNo: "3000013", Name: "TEST PEN", Qty: 1,
		UnitPriceCents: 1000, NetCents: 1000, Category: "日用品"}}
	if err := app.store.CreateReceipt(rec, items); err != nil {
		t.Fatalf("建小票失败：%v", err)
	}
	got, _ := app.store.GetReceiptItems("rd1")
	if _, err := app.store.AddReturn(got[0].ID, 1); err != nil {
		t.Fatalf("AddReturn 失败：%v", err)
	}
	if err := app.store.DeleteReceipt("rd1"); err != nil {
		t.Fatalf("删小票失败：%v", err)
	}
	if list, _ := app.store.ListReturns("pending"); len(list) != 0 {
		t.Errorf("删小票后退货记录应一起删，实际 %d 条", len(list))
	}
}
