package main

// 退货清单接口（网页会话鉴权）：从小票详情加入、改数量、标记已处理、
// 全部已处理、移除。提醒页同款的状态流转：pending → done，可恢复。

import (
	"errors"
	"net/http"
	"strconv"
)

func (a *App) listReturnsHandler(w http.ResponseWriter, r *http.Request, u *User) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	if status != "pending" && status != "done" {
		writeErr(w, http.StatusBadRequest, "状态只能是 pending 或 done")
		return
	}
	list, err := a.store.ListReturns(status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	pending, _ := a.store.CountPendingReturns()
	writeJSON(w, http.StatusOK, map[string]any{"returns": list, "pending_count": pending})
}

func (a *App) addReturnHandler(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		ReceiptItemID int64 `json:"receipt_item_id"`
		Qty           int   `json:"qty"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ReceiptItemID <= 0 {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	it, err := a.store.AddReturn(req.ReceiptItemID, req.Qty)
	if errors.Is(err, errNotFound) {
		writeErr(w, http.StatusNotFound, "商品行不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"return": it})
}

func parseReturnID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "退货记录 id 不对")
		return 0, false
	}
	return id, true
}

func (a *App) removeReturnHandler(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := parseReturnID(w, r)
	if !ok {
		return
	}
	if err := a.store.RemoveReturn(id); err != nil {
		if errors.Is(err, errNotFound) {
			writeErr(w, http.StatusNotFound, "退货记录不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) setReturnQtyHandler(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := parseReturnID(w, r)
	if !ok {
		return
	}
	var req struct {
		Qty int `json:"qty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	it, err := a.store.SetReturnQty(id, req.Qty)
	if errors.Is(err, errNotFound) {
		writeErr(w, http.StatusNotFound, "退货记录不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"return": it})
}

func (a *App) setReturnStatusHandler(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := parseReturnID(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"` // done / pending
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Status != "done" && req.Status != "pending" {
		writeErr(w, http.StatusBadRequest, "状态只能是 done 或 pending")
		return
	}
	if err := a.store.SetReturnStatus(id, req.Status); err != nil {
		if errors.Is(err, errNotFound) {
			writeErr(w, http.StatusNotFound, "退货记录不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) doneAllReturnsHandler(w http.ResponseWriter, r *http.Request, u *User) {
	n, err := a.store.MarkAllReturnsDone()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "done": n})
}
