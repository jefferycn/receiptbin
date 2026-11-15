package main

// 门店名大小写归一（v1.6）测试：筛选下拉按门店号分组、旧数据迁移。
// 夹具全是虚构的示例门店名，不含任何真实购物记录。

import (
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

func TestValidCategoryGiftCard(t *testing.T) {
	// v1.7 新增分类：礼品卡（用户要求）；"其他" 仍为默认分类，留在最后一位
	if !validCategory("礼品卡") {
		t.Errorf("礼品卡 应为合法分类")
	}
	if Categories[len(Categories)-1] != "其他" {
		t.Errorf("最后一个分类应为 其他，实际 %q", Categories[len(Categories)-1])
	}
}
