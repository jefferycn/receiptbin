package main

// 价格观察 API（v1.9）测试：鉴权、写入观察、去重、调价与盯价提醒。
// 夹具全是虚构数据（货号 9000001/9000002），不含任何真实购物记录。

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestObservationsAPIAuth(t *testing.T) {
	// token 未配置：503
	app := testAPIApp(t, "")
	rec := doAPI(t, app, "POST", "/api/observations",
		`{"observations":[{"item_no":"9000001","price_cents":1499}]}`, testAPIToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("未配置 token 时应返回 503，实际 %d", rec.Code)
	}

	// token 已配置：缺 token → 401，对 token → 200
	app = testAPIApp(t, testAPIToken)
	rec = doAPI(t, app, "POST", "/api/observations",
		`{"observations":[{"item_no":"9000001","price_cents":1499}]}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("缺 token 应返回 401，实际 %d", rec.Code)
	}
	rec = doAPI(t, app, "POST", "/api/observations",
		`{"observations":[{"item_no":"9000001","price_cents":1499}]}`, testAPIToken)
	if rec.Code != http.StatusOK {
		t.Errorf("对 token 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestObservationsAPIEndToEnd(t *testing.T) {
	app := testAPIApp(t, testAPIToken)
	// 9/10 以 $19.99 买过两件，随后加入盯价、目标价 $14.99
	mustReceipt(t, app, "e1", "2026-09-10 15:00", []ReceiptItem{fakeItem("9000001", "TEST JUICE", 2, 1999)})
	target := 1499
	if err := app.store.AddWatch("9000001", "TEST JUICE", &target); err != nil {
		t.Fatal(err)
	}

	post := func(body string) (int, int, int) {
		t.Helper()
		rec := doAPI(t, app, "POST", "/api/observations", body, testAPIToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Recorded      int `json:"recorded"`
			Skipped       int `json:"skipped"`
			AlertsCreated int `json:"alerts_created"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析响应失败：%v", err)
		}
		return resp.Recorded, resp.Skipped, resp.AlertsCreated
	}

	// 9/12 查到 $14.99：调价提醒（9/10 买贵了）+ 达到目标价，共 2 条提醒
	recorded, skipped, alerts := post(
		`{"observations":[{"item_no":"9000001","price_cents":1499,"store":"DANVILLE","observed_on":"2026-09-12"}]}`)
	if recorded != 1 || skipped != 0 || alerts != 2 {
		t.Errorf("首次写入应记 1 条、提醒 2 条，实际 recorded=%d skipped=%d alerts=%d",
			recorded, skipped, alerts)
	}
	latest, err := app.store.LatestObservation("9000001")
	if err != nil || latest.Source != "watch" || latest.PriceCents != 1499 {
		t.Errorf("观察 source 应为 watch、价格 1499，实际 %+v err=%v", latest, err)
	}

	// 同价同店再写一次：跳过，不重跑提醒
	recorded, skipped, alerts = post(
		`{"observations":[{"item_no":"9000001","price_cents":1499,"store":"DANVILLE","observed_on":"2026-09-13"}]}`)
	if recorded != 0 || skipped != 1 || alerts != 0 {
		t.Errorf("重复写入应跳过，实际 recorded=%d skipped=%d alerts=%d",
			recorded, skipped, alerts)
	}

	// 9/14 再降到 $13.99：调价 + 盯价降价 + 达到目标价，共 3 条提醒
	recorded, skipped, alerts = post(
		`{"observations":[{"item_no":"9000001","price_cents":1399,"store":"DANVILLE","observed_on":"2026-09-14"}]}`)
	if recorded != 1 || skipped != 0 || alerts != 3 {
		t.Errorf("降价写入应记 1 条、提醒 3 条，实际 recorded=%d skipped=%d alerts=%d",
			recorded, skipped, alerts)
	}

	// 非盯价商品：也记观察（出现在价格线上），但不产生盯价提醒
	recorded, skipped, alerts = post(
		`{"observations":[{"item_no":"9000002","price_cents":999,"store":"CONCORD","observed_on":"2026-09-14"}]}`)
	if recorded != 1 || alerts != 0 {
		t.Errorf("非盯价商品应记 1 条、无提醒，实际 recorded=%d alerts=%d", recorded, alerts)
	}
	obs, err := app.store.ObservationsFor("9000002")
	if err != nil || len(obs) != 1 || obs[0].Source != "watch" {
		t.Errorf("9000002 应有 1 条 watch 观察，实际 %v err=%v", obs, err)
	}

	// 非法价格：报错条目，不记
	rec := doAPI(t, app, "POST", "/api/observations",
		`{"observations":[{"item_no":"9000001","price_cents":0}]}`, testAPIToken)
	var resp struct {
		Recorded int `json:"recorded"`
		Results  []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if resp.Recorded != 0 || len(resp.Results) != 1 || resp.Results[0].Error == "" {
		t.Errorf("价格 0 应报错且不记，实际 %+v", resp)
	}

	// 提醒总数：2 + 3 = 5 条未处理
	open, err := app.store.ListAlerts("open")
	if err != nil || len(open) != 5 {
		t.Errorf("应有 5 条未处理提醒，实际 %d err=%v", len(open), err)
	}
}
