package main

// 价格观察 API（v1.9）：给助手用的机器接口，与商品词典 API 共用同一个
// Bearer <redacted> 鉴权（requireAPIToken）。盯价检查在应用外做
// （costco.com / Same-Day / warehouserunner），查到的价格经这里写入，
// 观察的 source 记为 "watch"，与小票（receipt）、手动记价（manual）区分。
//
//   POST /api/observations   批量写入价格观察：
//     {"observations":[{"item_no":"...","price_cents":1499,
//                        "store":"DANVILLE","observed_on":"2026-11-15"}]}
//     observed_on 可空（默认今天）；store 可空。
//     与最近一次观察同价同店时跳过（不重复记，也不重跑提醒）；
//     否则记观察并跑调价比对（30 天内高价购买 → price_drop）与盯价检查
//     （低于上次观察 → watch_drop；达到目标价 → watch_target）。
//     非盯价商品也记观察（与手动记价一致，出现在商品价格线上），
//     但不产生盯价提醒。

import (
	"net/http"
	"strings"
	"time"
)

// addObservationsAPIHandler POST /api/observations：批量写入价格观察。
func (a *App) addObservationsAPIHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Observations []struct {
			ItemNo     string `json:"item_no"`
			PriceCents int    `json:"price_cents"`
			Store      string `json:"store"`
			ObservedOn string `json:"observed_on"` // "2006-01-02"，空为今天
		} `json:"observations"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(req.Observations) == 0 {
		writeErr(w, http.StatusBadRequest, "observations 不能为空")
		return
	}
	if len(req.Observations) > 100 {
		writeErr(w, http.StatusBadRequest, "一次最多 100 条")
		return
	}

	type result struct {
		ItemNo        string `json:"item_no"`
		Recorded      bool   `json:"recorded"`
		Skipped       bool   `json:"skipped,omitempty"`
		AlertsCreated int    `json:"alerts_created"`
		Error         string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(req.Observations))
	recorded, skipped, alerts := 0, 0, 0
	for _, o := range req.Observations {
		itemNo := strings.TrimSpace(o.ItemNo)
		store := strings.ToUpper(strings.TrimSpace(o.Store))
		observedOn := o.ObservedOn
		if itemNo == "" || o.PriceCents <= 0 {
			results = append(results, result{ItemNo: itemNo, Error: "货号和价格不能为空"})
			continue
		}
		if observedOn == "" {
			observedOn = todayLocal()
		}
		if _, err := time.Parse("2006-01-02", observedOn); err != nil {
			results = append(results, result{ItemNo: itemNo, Error: "日期格式不对（YYYY-MM-DD）"})
			continue
		}
		// 去重：与最近一次观察同价同店，跳过（不重跑提醒，避免已处理的
		// 提醒被反复重建）
		if latest, err := a.store.LatestObservation(itemNo); err == nil &&
			latest.PriceCents == o.PriceCents && latest.Store == store {
			skipped++
			results = append(results, result{ItemNo: itemNo, Skipped: true})
			continue
		}
		// 品名：优先取购买记录里的，否则取盯价清单里的，否则用货号
		name := itemNo
		if p, err := a.store.LatestPurchase(itemNo); err == nil {
			name = p.Name
		} else if w, err := a.store.GetWatch(itemNo); err == nil && w.Name != "" {
			name = w.Name
		}
		n := a.checkWatchPrice(itemNo, name, o.PriceCents, store, observedOn)
		// 非盯价商品：observePrice 不记，这里补记（与手动记价一致）
		if _, err := a.store.GetWatch(itemNo); err != nil {
			a.store.AddObservation(&Observation{
				ItemNo: itemNo, PriceCents: o.PriceCents, Store: store,
				ObservedOn: observedOn, Source: "watch",
			})
		}
		recorded++
		alerts += n
		results = append(results, result{ItemNo: itemNo, Recorded: true, AlertsCreated: n})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recorded": recorded, "skipped": skipped,
		"alerts_created": alerts, "results": results,
	})
}
