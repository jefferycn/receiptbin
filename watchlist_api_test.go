package main

// 盯价清单 API（v1.8）测试：鉴权、批量新增/更新、删除。
// 夹具全是虚构数据（货号 9000001），不含任何真实购物记录。

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestWatchlistAPIAuth(t *testing.T) {
	// token 未配置：503
	app := testAPIApp(t, "")
	rec := doAPI(t, app, "GET", "/api/watchlist", "", testAPIToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("未配置 token 时应返回 503，实际 %d", rec.Code)
	}

	// token 已配置：缺 token → 401，错 token → 401，对 token → 200
	app = testAPIApp(t, testAPIToken)
	rec = doAPI(t, app, "GET", "/api/watchlist", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("缺 token 应返回 401，实际 %d", rec.Code)
	}
	rec = doAPI(t, app, "GET", "/api/watchlist", "", "wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错 token 应返回 401，实际 %d", rec.Code)
	}
	rec = doAPI(t, app, "GET", "/api/watchlist", "", testAPIToken)
	if rec.Code != http.StatusOK {
		t.Errorf("对 token 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestWatchlistAPIEndToEnd(t *testing.T) {
	app := testAPIApp(t, testAPIToken)
	mustReceipt(t, app, "e1", "2026-09-10 15:00", []ReceiptItem{fakeItem("9000001", "TEST JUICE", 2, 999)})

	// 批量新增：一条带目标价，一条不带（只看降价），一条目标价非法
	rec := doAPI(t, app, "POST", "/api/watchlist",
		`{"items":[{"item_no":"9000001","target_cents":899},{"item_no":"9000002"},{"item_no":"9000003","target_cents":0}]}`,
		testAPIToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var postResp struct {
		Added   int `json:"added"`
		Results []struct {
			ItemNo string `json:"item_no"`
			Added  bool   `json:"added"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("解析 POST 响应失败：%v", err)
	}
	if postResp.Added != 2 || len(postResp.Results) != 3 {
		t.Fatalf("应新增 2 条、结果 3 条，实际 added=%d results=%d", postResp.Added, len(postResp.Results))
	}
	if postResp.Results[2].Error == "" {
		t.Errorf("目标价 0 应报错，实际无错误")
	}

	// GET 列表：品名从购买记录回填，目标价正确
	rec = doAPI(t, app, "GET", "/api/watchlist", "", testAPIToken)
	var listResp struct {
		Watchlist []Watch `json:"watchlist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("解析 GET 响应失败：%v", err)
	}
	if len(listResp.Watchlist) != 2 {
		t.Fatalf("清单应有 2 条，实际 %d", len(listResp.Watchlist))
	}
	var w1 *Watch
	for i := range listResp.Watchlist {
		if listResp.Watchlist[i].ItemNo == "9000001" {
			w1 = &listResp.Watchlist[i]
		}
	}
	if w1 == nil {
		t.Fatalf("9000001 应在清单里")
	}
	if w1.Name != "TEST JUICE" {
		t.Errorf("品名应从购买记录回填为 TEST JUICE，实际 %q", w1.Name)
	}
	if w1.TargetCents == nil || *w1.TargetCents != 899 {
		t.Errorf("目标价应为 899，实际 %+v", w1.TargetCents)
	}
	if w1.LastObs == nil || w1.LastObs.PriceCents != 999 {
		t.Errorf("应回填最近观察价 999，实际 %+v", w1.LastObs)
	}

	// 再 POST 同一货号、不给目标价：应保持原目标价，不清掉
	rec = doAPI(t, app, "POST", "/api/watchlist",
		`{"items":[{"item_no":"9000001"}]}`, testAPIToken)
	if err := json.Unmarshal(rec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("解析 POST 响应失败：%v", err)
	}
	if postResp.Added != 0 || postResp.Results[0].Added {
		t.Errorf("重复 POST 应为更新而非新增，实际 %+v", postResp)
	}
	got, _ := app.store.GetWatch("9000001")
	if got.TargetCents == nil || *got.TargetCents != 899 {
		t.Errorf("省略目标价时应保持 899，实际 %+v", got.TargetCents)
	}

	// POST 新目标价：更新
	rec = doAPI(t, app, "POST", "/api/watchlist",
		`{"items":[{"item_no":"9000001","target_cents":799}]}`, testAPIToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新目标价应返回 200，实际 %d", rec.Code)
	}
	got, _ = app.store.GetWatch("9000001")
	if got.TargetCents == nil || *got.TargetCents != 799 {
		t.Errorf("目标价应更新为 799，实际 %+v", got.TargetCents)
	}

	// DELETE：删掉一条，再删返回 404
	rec = doAPI(t, app, "DELETE", "/api/watchlist/9000002", "", testAPIToken)
	if rec.Code != http.StatusOK {
		t.Errorf("DELETE 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, app, "DELETE", "/api/watchlist/9000002", "", testAPIToken)
	if rec.Code != http.StatusNotFound {
		t.Errorf("重复 DELETE 应返回 404，实际 %d", rec.Code)
	}
}
