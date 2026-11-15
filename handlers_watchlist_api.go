package main

// 盯价清单 API（v1.8）：给助手用的机器接口，与商品词典 API 共用同一个
// Bearer <redacted> 鉴权（requireAPIToken）。网页端盯价操作走 /api/v1/watchlist
//（会话 Cookie），机器调用走这里，不走浏览器登录。
//
//   GET    /api/watchlist            列出盯价清单（含词典中文名与最近观察）
//   POST   /api/watchlist            批量新增/更新：{"items":[{"item_no":"...","target_cents":1499}]}
//                                    target_cents 可空（只看降价）；已在清单里时
//                                    POST 更新目标价，省略 target_cents 则保持原值
//   DELETE /api/watchlist/{itemNo}   从清单移除

import (
	"errors"
	"net/http"
	"strings"
)

// listWatchlistAPIHandler GET /api/watchlist：返回盯价清单。
func (a *App) listWatchlistAPIHandler(w http.ResponseWriter, r *http.Request) {
	watches, err := a.store.ListWatchlist()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"watchlist": watches})
}

// addWatchAPIHandler POST /api/watchlist：批量新增/更新盯价商品。
func (a *App) addWatchAPIHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []struct {
			ItemNo      string `json:"item_no"`
			TargetCents *int   `json:"target_cents"` // 可空
		} `json:"items"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "items 不能为空")
		return
	}
	if len(req.Items) > 100 {
		writeErr(w, http.StatusBadRequest, "一次最多 100 条")
		return
	}

	type result struct {
		ItemNo string `json:"item_no"`
		Added  bool   `json:"added"` // true=新增，false=已在清单里（更新目标价）
		Error  string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(req.Items))
	added := 0
	for _, it := range req.Items {
		itemNo := strings.TrimSpace(it.ItemNo)
		if itemNo == "" {
			results = append(results, result{ItemNo: itemNo, Error: "货号不能为空"})
			continue
		}
		if it.TargetCents != nil && *it.TargetCents <= 0 {
			results = append(results, result{ItemNo: itemNo, Error: "目标价必须大于 0"})
			continue
		}
		// 品名自动取最近一次购买记录；没买过、清单里也没有就留空
		name := ""
		if p, err := a.store.LatestPurchase(itemNo); err == nil {
			name = p.Name
		}
		existing, _ := a.store.GetWatch(itemNo)
		target := it.TargetCents
		if existing != nil {
			// 已在清单里：更新时省略的字段保持原值，不清掉
			if name == "" {
				name = existing.Name
			}
			if target == nil {
				target = existing.TargetCents
			}
		}
		if err := a.store.AddWatch(itemNo, name, target); err != nil {
			results = append(results, result{ItemNo: itemNo, Error: "保存失败"})
			continue
		}
		a.backfillWatchObservation(itemNo)
		isNew := existing == nil
		if isNew {
			added++
		}
		results = append(results, result{ItemNo: itemNo, Added: isNew})
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "results": results})
}

// removeWatchAPIHandler DELETE /api/watchlist/{itemNo}：从清单移除。
func (a *App) removeWatchAPIHandler(w http.ResponseWriter, r *http.Request) {
	itemNo := r.PathValue("itemNo")
	if err := a.store.RemoveWatch(itemNo); err != nil {
		if errors.Is(err, errNotFound) {
			writeErr(w, http.StatusNotFound, "不在盯价清单里")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
