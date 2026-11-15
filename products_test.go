package main

// 商品词典（v1.2）测试：词典增删改查、追溯更新、未命名清单、
// 中文搜索、导入套用、API 鉴权。夹具全是虚构数据
// （货号 9000001/9000002，测试商品名），不含任何真实购物记录。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAPIToken = "test-token-for-unit-tests-only"

func strptr(s string) *string { return &s }

// fakeItem 造一件虚构商品行（测试用）。
func fakeItem(itemNo, name string, qty, unit int) ReceiptItem {
	return ReceiptItem{
		ItemNo: itemNo, Name: name, Qty: qty,
		UnitPriceCents: unit, DiscountCents: 0, NetCents: qty * unit,
		Taxable: true, Category: "其他",
	}
}

func TestProductUpsertListDelete(t *testing.T) {
	app := testApp(t)
	store := app.store

	// 初始为空
	prods, err := store.ListProducts()
	if err != nil || len(prods) != 0 {
		t.Fatalf("初始词典应为空，实际 %v, err=%v", prods, err)
	}

	// 批量新建：一条只给中文名，一条只给分类，一条两个都给
	res, err := store.UpsertProducts([]ProductUpdate{
		{ItemNumber: "9000001", NameZh: strptr("测试果汁")},
		{ItemNumber: "9000002", Category: strptr("日用品")},
		{ItemNumber: "9000003", NameZh: strptr("测试纸巾"), Category: strptr("日用品")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("upsert 结果 = %d 条，想要 3", len(res))
	}
	for _, r := range res {
		if !r.Created {
			t.Errorf("货号 %s 应为新建，实际 Created=false", r.ItemNumber)
		}
		if r.RowsUpdated != 0 {
			t.Errorf("货号 %s 还没买过，追溯行数应为 0，实际 %d", r.ItemNumber, r.RowsUpdated)
		}
	}

	prods, _ = store.ListProducts()
	if len(prods) != 3 {
		t.Fatalf("词典应有 3 条，实际 %d", len(prods))
	}
	// 按货号排序
	if prods[0].ItemNumber != "9000001" || prods[2].ItemNumber != "9000003" {
		t.Errorf("词典未按货号排序：%v", prods)
	}
	if prods[0].NameZh != "测试果汁" || prods[0].Category != "" {
		t.Errorf("9000001 应只有中文名，实际 %+v", prods[0])
	}
	if prods[1].NameZh != "" || prods[1].Category != "日用品" {
		t.Errorf("9000002 应只有分类，实际 %+v", prods[1])
	}

	// 部分更新：只改分类，中文名保持不动；Created 应为 false
	res, err = store.UpsertProducts([]ProductUpdate{
		{ItemNumber: "9000001", Category: strptr("乳制品/饮品")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Created {
		t.Errorf("更新已有词条时 Created 应为 false")
	}
	p, _ := store.GetProduct("9000001")
	if p.NameZh != "测试果汁" || p.Category != "乳制品/饮品" {
		t.Errorf("部分更新后 9000001 = %+v，想要中文名保留、分类更新", p)
	}

	// 删除：存在的删掉，不存在的返回 false
	ok, err := store.DeleteProduct("9000002")
	if err != nil || !ok {
		t.Errorf("删除 9000002 应成功，ok=%v err=%v", ok, err)
	}
	ok, _ = store.DeleteProduct("9000002")
	if ok {
		t.Errorf("重复删除应返回 false")
	}
	if _, err := store.GetProduct("9000002"); err == nil {
		t.Errorf("删除后 GetProduct 应返回错误")
	}
	prods, _ = store.ListProducts()
	if len(prods) != 2 {
		t.Errorf("删除后词典应剩 2 条，实际 %d", len(prods))
	}
}

func TestProductRetroactiveUpdate(t *testing.T) {
	app := testApp(t)
	store := app.store

	// 两张小票都买了 9000001（英文名 TEST JUICE，分类“其他”）
	mustReceipt(t, app, "p1", "2026-09-10 15:00", []ReceiptItem{fakeItem("9000001", "TEST JUICE", 2, 999)})
	mustReceipt(t, app, "p2", "2026-09-17 15:00", []ReceiptItem{fakeItem("9000001", "TEST JUICE", 1, 999)})

	// 词典更新中文名 + 分类：应追溯改掉 2 行历史商品
	res, err := store.UpsertProducts([]ProductUpdate{
		{ItemNumber: "9000001", NameZh: strptr("测试果汁"), Category: strptr("乳制品/饮品")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].RowsUpdated != 2 {
		t.Errorf("追溯行数 = %d，想要 2（两张小票各 1 行）", res[0].RowsUpdated)
	}
	// 词条的 name_en 应从购买记录取到英文名
	p, _ := store.GetProduct("9000001")
	if p.NameEn != "TEST JUICE" {
		t.Errorf("name_en = %q，想要 TEST JUICE", p.NameEn)
	}
	// 历史商品行：中文名与分类都改了，英文名与金额不动
	items, _ := store.GetReceiptItems("p1")
	if len(items) != 1 {
		t.Fatalf("p1 应有 1 件商品，实际 %d", len(items))
	}
	it := items[0]
	if it.NameZh != "测试果汁" || it.Category != "乳制品/饮品" {
		t.Errorf("追溯后商品行 = 中文名 %q 分类 %q，想要 测试果汁/乳制品/饮品", it.NameZh, it.Category)
	}
	if it.Name != "TEST JUICE" || it.NetCents != 1998 {
		t.Errorf("追溯不应动英文名与金额：%+v", it)
	}
	// 分类记忆同步
	if c := store.GetCategory("9000001"); c != "乳制品/饮品" {
		t.Errorf("分类记忆 = %q，想要 乳制品/饮品", c)
	}

	// 只改中文名：分类保持不动
	_, err = store.UpsertProducts([]ProductUpdate{
		{ItemNumber: "9000001", NameZh: strptr("测试果汁改名")},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, _ = store.GetReceiptItems("p2")
	if items[0].NameZh != "测试果汁改名" || items[0].Category != "乳制品/饮品" {
		t.Errorf("只改中文名后商品行 = %+v，分类应保持乳制品/饮品", items[0])
	}

	// 删词条：历史商品行保留已写入的中文名与分类
	store.DeleteProduct("9000001")
	items, _ = store.GetReceiptItems("p1")
	if items[0].NameZh != "测试果汁改名" || items[0].Category != "乳制品/饮品" {
		t.Errorf("删词条后历史行不应被清空：%+v", items[0])
	}
}

func TestProductUnmapped(t *testing.T) {
	app := testApp(t)
	store := app.store

	mustReceipt(t, app, "u1", "2026-09-10 15:00", []ReceiptItem{
		fakeItem("9000001", "TEST JUICE", 1, 999),
		fakeItem("9000002", "TEST WIDGET", 3, 499),
	})
	mustReceipt(t, app, "u2", "2026-09-17 15:00", []ReceiptItem{fakeItem("9000001", "TEST JUICE", 1, 999)})

	// 9000001 进词典，9000002 留着未命名
	store.UpsertProducts([]ProductUpdate{{ItemNumber: "9000001", NameZh: strptr("测试果汁")}})

	unmapped, err := store.ListUnmapped()
	if err != nil {
		t.Fatal(err)
	}
	byNo := map[string]UnmappedItem{}
	for _, x := range unmapped {
		byNo[x.ItemNo] = x
	}
	if _, ok := byNo["9000001"]; ok {
		t.Errorf("9000001 已进词典，不应出现在未命名清单里")
	}
	w, ok := byNo["9000002"]
	if !ok {
		t.Fatalf("9000002 应在未命名清单里，实际清单 = %v", unmapped)
	}
	if w.Name != "TEST WIDGET" || w.PurchaseCount != 1 || w.LastDate != "2026-09-10 15:00" {
		t.Errorf("9000002 未命名信息 = %+v，想要 TEST WIDGET/1 次/2026-09-10", w)
	}
	// 种子小票的商品（1000008 足球等）也没进词典，应在清单里
	if _, ok := byNo["1000008"]; !ok {
		t.Errorf("种子商品 1000008 未进词典，应在未命名清单里")
	}
}

func TestSearchMatchesChineseName(t *testing.T) {
	app := testApp(t)
	store := app.store

	mustReceipt(t, app, "s1", "2026-09-10 15:00", []ReceiptItem{fakeItem("9000001", "TEST JUICE", 1, 999)})
	store.UpsertProducts([]ProductUpdate{
		{ItemNumber: "9000001", NameZh: strptr("测试果汁"), Category: strptr("乳制品/饮品")},
	})

	// 中文搜得到
	items, err := store.SearchItems("果汁", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemNo != "9000001" {
		t.Fatalf("中文搜索“果汁”应命中 9000001，实际 %v", items)
	}
	if items[0].NameZh != "测试果汁" || items[0].Name != "TEST JUICE" {
		t.Errorf("搜索结果中文名/英文名 = %q/%q，想要 测试果汁/TEST JUICE", items[0].NameZh, items[0].Name)
	}
	// 英文还搜得到（种子小票里也有 ORANGE JUICE，搜全名才唯一）
	items, _ = store.SearchItems("TEST JUICE", 30)
	if len(items) != 1 || items[0].ItemNo != "9000001" {
		t.Errorf("英文搜索 TEST JUICE 应命中 9000001，实际 %v", items)
	}
	// 货号还搜得到
	items, _ = store.SearchItems("9000001", 30)
	if len(items) != 1 {
		t.Errorf("货号搜索应命中 1 条，实际 %v", items)
	}
}

func TestApplyProducts(t *testing.T) {
	// 词典优先：中文名 + 分类都用词典的
	items := []ParsedItem{
		{ItemNo: "9000001", Name: "TEST JUICE"},
		{ItemNo: "9000002", Name: "TEST WIDGET"},
		{ItemNo: "9000003", Name: "TEST SNACK"},
	}
	cats := map[string]string{"9000002": "日用品"}
	prods := map[string]Product{
		"9000001": {ItemNumber: "9000001", NameZh: "测试果汁", Category: "乳制品/饮品"},
		// 9000003 词典只有中文名、没有分类：分类退回记忆/其他
		"9000003": {ItemNumber: "9000003", NameZh: "测试零食"},
	}
	applyProducts(items, cats, prods)
	if items[0].NameZh != "测试果汁" || items[0].Category != "乳制品/饮品" {
		t.Errorf("9000001 应全用词典：%+v", items[0])
	}
	if items[1].NameZh != "" || items[1].Category != "日用品" {
		t.Errorf("9000002 应退回分类记忆：%+v", items[1])
	}
	if items[2].NameZh != "测试零食" || items[2].Category != "其他" {
		t.Errorf("9000003 应中文名用词典、分类默认其他：%+v", items[2])
	}
}

func TestProductMigrationAddsNameZh(t *testing.T) {
	// 模拟 v1.1 的旧库：receipt_items 没有 name_zh 列。
	// openStore 应自动补列，之后正常读写。
	dir := t.TempDir()
	dbPath := dir + "/receiptbin.db"
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE receipt_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT, receipt_id TEXT NOT NULL,
		item_no TEXT NOT NULL, name TEXT NOT NULL, qty INTEGER NOT NULL DEFAULT 1,
		unit_price_cents INTEGER NOT NULL, discount_cents INTEGER NOT NULL DEFAULT 0,
		net_cents INTEGER NOT NULL, taxable INTEGER NOT NULL DEFAULT 0,
		food_exempt INTEGER NOT NULL DEFAULT 0, category TEXT NOT NULL DEFAULT '其他')`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()

	store, err := openStore(dir)
	if err != nil {
		t.Fatalf("旧库 openStore 失败：%v", err)
	}
	defer store.Close()
	// 迁移后 name_zh 列应存在，且能写入
	var n int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('receipt_items') WHERE name = 'name_zh'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("迁移后应有 name_zh 列，count=%d err=%v", n, err)
	}
	// 旧库也能正常建 products 表
	if _, err := store.ListProducts(); err != nil {
		t.Fatalf("旧库迁移后 ListProducts 应可用：%v", err)
	}
}

// ---------- HTTP API 测试 ----------

func testAPIApp(t *testing.T, token string) *App {
	t.Helper()
	app := testApp(t)
	app.apiToken = token
	return app
}

func doAPI(t *testing.T, app *App, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, req)
	return rec
}

func TestProductsAPIAuth(t *testing.T) {
	// token 未配置：503
	app := testAPIApp(t, "")
	rec := doAPI(t, app, "GET", "/api/products", "", testAPIToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("未配置 token 时应返回 503，实际 %d", rec.Code)
	}

	// token 已配置：缺 token → 401，错 token → 401，对 token → 200
	app = testAPIApp(t, testAPIToken)
	rec = doAPI(t, app, "GET", "/api/products", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("缺 token 应返回 401，实际 %d", rec.Code)
	}
	rec = doAPI(t, app, "GET", "/api/products", "", "wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错 token 应返回 401，实际 %d", rec.Code)
	}
	rec = doAPI(t, app, "GET", "/api/products", "", testAPIToken)
	if rec.Code != http.StatusOK {
		t.Errorf("对 token 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 带错的 Authorization 前缀也不行
	req := httptest.NewRequest("GET", "/api/products", nil)
	req.Header.Set("Authorization", "Token "+testAPIToken)
	rec2 := httptest.NewRecorder()
	app.routes().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("非 Bearer 前缀应返回 401，实际 %d", rec2.Code)
	}
}

func TestProductsAPIUpsertEndToEnd(t *testing.T) {
	app := testAPIApp(t, testAPIToken)
	mustReceipt(t, app, "e1", "2026-09-10 15:00", []ReceiptItem{fakeItem("9000001", "TEST JUICE", 2, 999)})

	// POST 批量更新
	body := `{"items":[
		{"item_number":"9000001","name_zh":"测试果汁","category":"乳制品/饮品"},
		{"item_number":"9000002","name_zh":"测试小物"}
	]}`
	rec := doAPI(t, app, "POST", "/api/products", body, testAPIToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []UpsertResult `json:"results"`
		Created int            `json:"created"`
		Updated int            `json:"updated"`
		Items   int            `json:"items_updated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Created != 2 || resp.Updated != 0 {
		t.Errorf("created/updated = %d/%d，想要 2/0", resp.Created, resp.Updated)
	}
	if resp.Items != 1 {
		t.Errorf("items_updated = %d，想要 1（9000001 有 1 行历史）", resp.Items)
	}

	// GET 列表：两条都在
	rec = doAPI(t, app, "GET", "/api/products", "", testAPIToken)
	var list struct {
		Products []Product `json:"products"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Products) != 2 || list.Products[0].NameZh != "测试果汁" {
		t.Errorf("GET 列表不对：%+v", list.Products)
	}

	// GET 未命名：9000001 已进词典，不应出现；种子商品应在
	rec = doAPI(t, app, "GET", "/api/products/unmapped", "", testAPIToken)
	var unm struct {
		Items []UnmappedItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &unm); err != nil {
		t.Fatal(err)
	}
	for _, x := range unm.Items {
		if x.ItemNo == "9000001" {
			t.Errorf("9000001 已进词典，不应在未命名清单里")
		}
	}

	// DELETE：删掉 9000002，再删一次应 404
	rec = doAPI(t, app, "DELETE", "/api/products/9000002", "", testAPIToken)
	if rec.Code != http.StatusOK {
		t.Errorf("DELETE 应返回 200，实际 %d", rec.Code)
	}
	rec = doAPI(t, app, "DELETE", "/api/products/9000002", "", testAPIToken)
	if rec.Code != http.StatusNotFound {
		t.Errorf("重复 DELETE 应返回 404，实际 %d", rec.Code)
	}
}

func TestProductsAPIValidation(t *testing.T) {
	app := testAPIApp(t, testAPIToken)
	cases := []struct {
		name string
		body string
	}{
		{"空 items", `{"items":[]}`},
		{"缺 item_number", `{"items":[{"name_zh":"测试"}]}`},
		{"两个字段都没给", `{"items":[{"item_number":"9000001"}]}`},
		{"分类不在列表里", `{"items":[{"item_number":"9000001","category":"不存在的分类"}]}`},
		{"同批重复货号", `{"items":[{"item_number":"9000001","name_zh":"甲"},{"item_number":"9000001","name_zh":"乙"}]}`},
	}
	for _, tc := range cases {
		rec := doAPI(t, app, "POST", "/api/products", tc.body, testAPIToken)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s：应返回 400，实际 %d：%s", tc.name, rec.Code, rec.Body.String())
		}
	}
	// 只给中文名是合法的
	rec := doAPI(t, app, "POST", "/api/products", `{"items":[{"item_number":"9000001","name_zh":"测试"}]}`, testAPIToken)
	if rec.Code != http.StatusOK {
		t.Errorf("只给中文名应返回 200，实际 %d", rec.Code)
	}
	// 只给分类也是合法的
	rec = doAPI(t, app, "POST", "/api/products", `{"items":[{"item_number":"9000002","category":"运动"}]}`, testAPIToken)
	if rec.Code != http.StatusOK {
		t.Errorf("只给分类应返回 200，实际 %d", rec.Code)
	}
}
