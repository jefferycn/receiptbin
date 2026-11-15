package main

// 商品词典 API（v1.2）：给助手用的机器接口，批量更新中文名与分类。
//
// 认证：环境变量 RECEIPTBIN_API_TOKEN 里的 Bearer token，不接受网页会话
// Cookie（机器调用不走浏览器登录）。token 未配置时返回 503；
// token 不对或缺失时返回 401。token 永不写日志。

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireAPIToken 是 /api/products 的鉴权中间件。
func (a *App) requireAPIToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.apiToken == "" {
			writeErr(w, http.StatusServiceUnavailable,
				"API token 未配置：请在 .env 里设置 RECEIPTBIN_API_TOKEN 后重启服务")
			return
		}
		// 只认 Authorization: Bearer <token>，不认会话 Cookie
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeErr(w, http.StatusUnauthorized, "需要 Bearer token")
			return
		}
		given := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(given), []byte(a.apiToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "token 不正确")
			return
		}
		next(w, r)
	}
}

// listProductsHandler GET /api/products：返回全部词条（按货号排序）。
func (a *App) listProductsHandler(w http.ResponseWriter, r *http.Request) {
	prods, err := a.store.ListProducts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": prods})
}

// upsertProductsHandler POST /api/products：批量新增/更新词条。
// 请求体：{"items":[{"item_number":"9000001","name_zh":"中文名","category":"食品"}]}
// name_zh 与 category 各自可选，但每条至少提供一个；category 必须是已知分类。
// 更新会追溯改掉该货号的历史商品行，响应里按条报告改动行数。
func (a *App) upsertProductsHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []struct {
			ItemNumber string  `json:"item_number"`
			NameZh     *string `json:"name_zh"`
			Category   *string `json:"category"`
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
	if len(req.Items) > 500 {
		writeErr(w, http.StatusBadRequest, "一次最多更新 500 条")
		return
	}
	entries := make([]ProductUpdate, 0, len(req.Items))
	seen := map[string]bool{}
	for _, it := range req.Items {
		no := strings.TrimSpace(it.ItemNumber)
		if no == "" {
			writeErr(w, http.StatusBadRequest, "item_number 不能为空")
			return
		}
		if seen[no] {
			writeErr(w, http.StatusBadRequest, "货号 "+no+" 在同一次请求里重复出现")
			return
		}
		seen[no] = true
		hasZh := it.NameZh != nil && strings.TrimSpace(*it.NameZh) != ""
		hasCat := it.Category != nil && strings.TrimSpace(*it.Category) != ""
		if !hasZh && !hasCat {
			writeErr(w, http.StatusBadRequest, "货号 "+no+" 至少要提供 name_zh 或 category 之一")
			return
		}
		if hasCat && !validCategory(strings.TrimSpace(*it.Category)) {
			writeErr(w, http.StatusBadRequest, "货号 "+no+" 的分类 "+strings.TrimSpace(*it.Category)+" 不在分类列表里")
			return
		}
		e := ProductUpdate{ItemNumber: no}
		if hasZh {
			e.NameZh = it.NameZh
		}
		if hasCat {
			e.Category = it.Category
		}
		entries = append(entries, e)
	}
	results, err := a.store.UpsertProducts(entries)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	created, updated, rowsTotal := 0, 0, 0
	for _, res := range results {
		if res.Created {
			created++
		} else {
			updated++
		}
		rowsTotal += res.RowsUpdated
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"created": created, "updated": updated,
		"items_updated": rowsTotal,
	})
}

// deleteProductHandler DELETE /api/products/{item_number}：删一条词条。
// 历史商品行已写入的中文名/分类保留不动。
func (a *App) deleteProductHandler(w http.ResponseWriter, r *http.Request) {
	itemNo := r.PathValue("item_number")
	deleted, err := a.store.DeleteProduct(itemNo)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "商品词典里没有货号 "+itemNo)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// listUnmappedHandler GET /api/products/unmapped：返回买过、但还没进词典的商品。
func (a *App) listUnmappedHandler(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListUnmapped()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
