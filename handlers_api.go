package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---------- JSON 工具 ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20)) // JSON body 上限 1MB
	return dec.Decode(v)
}

// ---------- 状态 / 注册 / 登录 ----------

// statusHandler 供登录页判断是否首次使用（库里还没有任何用户）。
func (a *App) statusHandler(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.CountUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": n == 0})
}

// registerHandler 首个用户注册：仅当库里没有任何用户时可用，注册者自动成为管理员。
func (a *App) registerHandler(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.CountUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	if n > 0 {
		writeErr(w, http.StatusForbidden, "注册已关闭")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	if !validUsername(req.Username) {
		writeErr(w, http.StatusBadRequest, "用户名只允许小写字母、数字、-、_，1–32 位")
		return
	}
	if !validPassword(req.Password) {
		writeErr(w, http.StatusBadRequest, "密码最短 8 位")
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "密码处理失败")
		return
	}
	u, err := a.store.CreateUser(req.Username, hash, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "创建用户失败")
		return
	}
	a.startSession(w, u)
	writeJSON(w, http.StatusCreated, u)
}

// startSession 建会话并写 Cookie（登录与注册共用）。
func (a *App) startSession(w http.ResponseWriter, u *User) {
	a.store.DeleteExpiredSessions() // 顺手清过期会话
	sid, err := a.store.CreateSession(u.ID)
	if err != nil {
		return
	}
	a.setSessionCookie(w, sid)
}

func (a *App) loginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if a.limiter.isLocked(username) {
		writeErr(w, http.StatusTooManyRequests, "登录失败次数过多，请 15 分钟后再试")
		return
	}
	u, err := a.store.GetUserByUsername(username)
	if err != nil || u.Disabled {
		// 用户不存在或已禁用：也跑一次 bcrypt，避免用响应时间猜用户名
		checkPassword(string(dummyHash), req.Password)
		a.limiter.recordFailure(username)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !checkPassword(u.PasswordHash, req.Password) {
		a.limiter.recordFailure(username)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	a.limiter.reset(username)
	a.startSession(w, u)
	writeJSON(w, http.StatusOK, u)
}

func (a *App) logoutHandler(w http.ResponseWriter, r *http.Request, u *User) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.store.DeleteSession(c.Value)
	}
	a.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) meHandler(w http.ResponseWriter, r *http.Request, u *User) {
	writeJSON(w, http.StatusOK, u)
}

func (a *App) changePasswordHandler(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !checkPassword(u.PasswordHash, req.Current) {
		writeErr(w, http.StatusUnauthorized, "当前密码错误")
		return
	}
	if !validPassword(req.New) {
		writeErr(w, http.StatusBadRequest, "新密码最短 8 位")
		return
	}
	hash, err := hashPassword(req.New)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "密码处理失败")
		return
	}
	if err := a.store.SetUserPassword(u.ID, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	// 改密码后吊销全部会话（含当前），需要重新登录
	a.store.DeleteUserSessions(u.ID)
	a.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- 统计 ----------

func (a *App) statsHandler(w http.ResponseWriter, r *http.Request, u *User) {
	period := r.URL.Query().Get("period") // all / YYYY / YYYY-MM，默认今年
	if period == "" {
		period = time.Now().Format("2006")
	}
	all, year, month, err := a.store.SumTotals()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	byMonth, err := a.store.TotalsByMonth()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	byCategory, err := a.store.TotalsByCategory(period)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	openAlerts, _ := a.store.CountOpenAlerts()
	receipts, _, _ := a.store.ListReceipts(ReceiptFilter{PerPage: 5})
	writeJSON(w, http.StatusOK, map[string]any{
		"total_cents": all, "year_cents": year, "month_cents": month,
		"by_month": byMonth, "by_category": byCategory,
		"period": period, "open_alerts": openAlerts,
		"recent_receipts": receipts, "categories": Categories,
	})
}

// ---------- 小票上传与保存 ----------

// uploadHandler 收小票照片或官网导出的 PDF，识别 + 解析，返回解析结果与分类建议。
// 文件存在 data/pending/<uploadID>，确认保存时才挪到 receipts/。
// 照片走 OCR + 纸质解析；PDF 走 pdftotext 提文本 + 导出格式解析（自带文本层，不用 OCR）。
// 注意：OCR 可能跑几十秒，前端要显示进度；这里不设额外超时，
// 依赖 http.Server 的整体超时与 ocr.go 里的 120 秒上限。
func (a *App) uploadHandler(w http.ResponseWriter, r *http.Request, u *User) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeErr(w, http.StatusBadRequest, "文件太大或格式不对（上限 20MB）")
		return
	}
	file, header, err := r.FormFile("photo")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请上传小票照片或 PDF")
		return
	}
	defer file.Close()
	isPDF := false
	ext := ""
	if header.Header.Get("Content-Type") == "application/pdf" {
		ext, isPDF = ".pdf", true
	}
	if ext == "" {
		var ok bool
		ext, ok = extForContentType(header.Header.Get("Content-Type"))
		if !ok {
			// 有些浏览器不填 MIME，退化为按文件名后缀判断
			switch strings.ToLower(filepath.Ext(header.Filename)) {
			case ".jpg", ".jpeg":
				ext = ".jpg"
			case ".png":
				ext = ".png"
			case ".webp":
				ext = ".webp"
			case ".heic", ".heif":
				ext = ".heic"
			case ".pdf":
				ext, isPDF = ".pdf", true
			default:
				writeErr(w, http.StatusBadRequest, "只支持 JPG / PNG / WebP / HEIC 照片或 PDF")
				return
			}
		}
	}
	uploadID := randBase62(12)
	pendingPath := filepath.Join(a.store.dataDir, "pending", uploadID+ext)
	dst, err := os.Create(pendingPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存上传文件失败")
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		os.Remove(pendingPath)
		writeErr(w, http.StatusBadRequest, "文件太大或上传中断（上限 20MB）")
		return
	}
	dst.Close()
	// 按文件头校验真实类型，避免扩展名欺骗
	if isPDF {
		if !isPDFFile(pendingPath) {
			os.Remove(pendingPath)
			writeErr(w, http.StatusBadRequest, "上传的文件不是 PDF")
			return
		}
	} else if !isImageFile(pendingPath) {
		os.Remove(pendingPath)
		writeErr(w, http.StatusBadRequest, "上传的文件不是图片")
		return
	}
	var text string
	var parsed *ParsedReceipt
	if isPDF {
		// PDF：提文本（会员号在提文本时已去掉）+ 导出格式解析；
		// 文本层是干净的，不用跑货号纠错。
		text, err = ExtractPDFText(pendingPath)
		if err != nil {
			os.Remove(pendingPath)
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		parsed, err = ParseExportReceiptText(text)
		if err != nil {
			os.Remove(pendingPath)
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		text, err = OCRReceipt(pendingPath)
		if err != nil {
			os.Remove(pendingPath)
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		parsed, err = ParseReceiptText(text)
		if err != nil {
			os.Remove(pendingPath)
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// 货号纠错：同名同价、只差一位数字的货号合并（OCR 常把 5 读成 8），
		// 库里已有的货号优先。纠错在对账与分类填充之前做。
		if known, err := a.store.KnownItemNumbers(); err == nil {
			snapItemNumbers(parsed, known)
		}
	}
	// 分类与中文名：先查商品词典（中文名 + 分类），词典没有的退回分类记忆或“其他”。
	// 确认页不再要求手动选分类：词典没覆盖的商品默认“其他”，事后用词典 API 批量改。
	itemNos := make([]string, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		itemNos = append(itemNos, it.ItemNo)
	}
	cats := a.store.CategoriesFor(itemNos)
	prods := a.store.ProductsFor(itemNos)
	applyProducts(parsed.Items, cats, prods)
	problems, warnings := Reconcile(parsed)
	parsed.Warnings = append(parsed.Warnings, warnings...)
	writeJSON(w, http.StatusOK, map[string]any{
		"upload_id": uploadID,
		"receipt":   parsed,
		"problems":  problems,
		"raw_text":  text,
		"is_pdf":    isPDF,
	})
}

// createReceiptHandler 保存确认后的小票（前端传完整编辑结果）。
// 服务端再做一次对账校验，不通过拒绝入库；重复小票（同店同时间同总价）
// 返回 409，前端确认后带 force=true 重试。
func (a *App) createReceiptHandler(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		UploadID             string       `json:"upload_id"`
		Store                string       `json:"store"`
		StoreNo              string       `json:"store_no"`
		PurchasedAt          string       `json:"purchased_at"`
		SubtotalCents        int          `json:"subtotal_cents"`
		TaxCents             int          `json:"tax_cents"`
		TotalCents           int          `json:"total_cents"`
		SavingsCents         int          `json:"savings_cents"`
		ReceiptDiscountCents int          `json:"receipt_discount_cents"` // 整单立减（PDF 导出）
		ItemCount            int          `json:"item_count"`
		RawText              string       `json:"raw_text"`
		Items                []ParsedItem `json:"items"`
		Force                bool         `json:"force"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Store = strings.TrimSpace(req.Store)
	req.StoreNo = strings.TrimSpace(req.StoreNo)
	req.PurchasedAt = strings.TrimSpace(req.PurchasedAt)
	if req.Store == "" || req.PurchasedAt == "" {
		writeErr(w, http.StatusBadRequest, "门店和购买时间不能为空")
		return
	}
	if _, err := time.Parse(receiptTimeLayout, req.PurchasedAt); err != nil {
		// 允许只填日期，补 12:00
		if t, err2 := time.Parse("2006-01-02", req.PurchasedAt); err2 == nil {
			req.PurchasedAt = t.Format("2006-01-02") + " 12:00"
		} else {
			writeErr(w, http.StatusBadRequest, "购买时间格式不对（YYYY-MM-DD HH:MM）")
			return
		}
	}
	if len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "至少要有一件商品")
		return
	}
	// 归一化商品行并重算合计（不信任前端算好的数）。
	// 入库前再套一次词典：用户在确认页可能改了货号，或上传后词典刚更新过，
	// 以入库时刻的词典为准。先去空白再查词典，避免带空格的货号查不到。
	for i := range req.Items {
		req.Items[i].ItemNo = strings.TrimSpace(req.Items[i].ItemNo)
		req.Items[i].Name = strings.TrimSpace(req.Items[i].Name)
	}
	saveNos := make([]string, 0, len(req.Items))
	for _, it := range req.Items {
		saveNos = append(saveNos, it.ItemNo)
	}
	saveCats := a.store.CategoriesFor(saveNos)
	saveProds := a.store.ProductsFor(saveNos)
	applyProducts(req.Items, saveCats, saveProds)
	items := make([]ReceiptItem, 0, len(req.Items))
	gross, discounts, qtyTotal := 0, 0, 0
	for _, it := range req.Items {
		if it.ItemNo == "" || it.Name == "" || it.Qty < 1 || it.UnitPriceCents < 0 || it.DiscountCents < 0 {
			writeErr(w, http.StatusBadRequest, "商品行数据不对：货号、品名、数量、价格不能为空或为负")
			return
		}
		if it.DiscountCents > it.UnitPriceCents {
			writeErr(w, http.StatusBadRequest, "货号 "+it.ItemNo+" 的折扣大于单价")
			return
		}
		if !validCategory(it.Category) {
			it.Category = "其他"
		}
		net := it.Qty * (it.UnitPriceCents - it.DiscountCents)
		items = append(items, ReceiptItem{
			ItemNo: it.ItemNo, Name: it.Name, NameZh: it.NameZh, Qty: it.Qty,
			UnitPriceCents: it.UnitPriceCents, DiscountCents: it.DiscountCents,
			NetCents: net, Taxable: it.Taxable, FoodExempt: it.FoodExempt,
			Category: it.Category,
		})
		gross += it.Qty * it.UnitPriceCents
		discounts += it.Qty * it.DiscountCents
		qtyTotal += it.Qty
	}
	if req.ReceiptDiscountCents < 0 {
		writeErr(w, http.StatusBadRequest, "整单立减不能为负")
		return
	}
	// 立减合计 = 单件折扣 + 整单立减（服务端重算，不信任前端传来的数）；
	// 整单立减不单独存库，并入 savings_cents。
	discountTotal := discounts + req.ReceiptDiscountCents
	parsed := &ParsedReceipt{
		Items: itemsAsParsed(items), GrossCents: gross, DiscountCents: discountTotal,
		ReceiptDiscountCents: req.ReceiptDiscountCents,
		SubtotalCents:        req.SubtotalCents, TaxCents: req.TaxCents, TotalCents: req.TotalCents,
		SavingsCents: discountTotal, ItemCount: req.ItemCount,
	}
	if problems, _ := Reconcile(parsed); len(problems) > 0 {
		writeErr(w, http.StatusBadRequest, "对账不通过："+strings.Join(problems, "；"))
		return
	}
	if req.ItemCount == 0 {
		req.ItemCount = qtyTotal
	}
	if !req.Force {
		if dup, err := a.store.FindDuplicateReceipt(req.StoreNo, req.PurchasedAt, req.TotalCents); err == nil {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":        "已有一张相同的小票（" + dup.Store + " " + dup.PurchasedAt + "，" + formatCents(dup.TotalCents) + "），可能重复导入",
				"duplicate_id": dup.ID,
			})
			return
		}
	}
	id := randBase62(12)
	photoPath := ""
	if req.UploadID != "" {
		// 把 pending 照片挪到 receipts/<id>.<ext>
		if moved, err := a.adoptPendingPhoto(req.UploadID, id); err == nil {
			photoPath = moved
		}
		// 照片挪失败不阻塞入库（照片只是附件）
	}
	rec := &Receipt{
		ID: id, Store: req.Store, StoreNo: req.StoreNo, PurchasedAt: req.PurchasedAt,
		SubtotalCents: req.SubtotalCents, TaxCents: req.TaxCents, TotalCents: req.TotalCents,
		SavingsCents: discountTotal, ItemCount: req.ItemCount,
		// 入库前再剥一次会员号（纵深防御：raw_text 是前端回传的，不信任它）
		PhotoPath: photoPath, RawText: stripMemberNumbers(req.RawText),
	}
	if err := a.store.CreateReceipt(rec, items); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存小票失败")
		return
	}
	alertsCreated := a.checkReceiptAlerts(rec, items)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "alerts_created": alertsCreated,
	})
}

func itemsAsParsed(items []ReceiptItem) []ParsedItem {
	out := make([]ParsedItem, 0, len(items))
	for _, it := range items {
		out = append(out, ParsedItem{
			ItemNo: it.ItemNo, Name: it.Name, NameZh: it.NameZh, Qty: it.Qty,
			UnitPriceCents: it.UnitPriceCents, DiscountCents: it.DiscountCents,
			NetCents: it.NetCents, Taxable: it.Taxable, FoodExempt: it.FoodExempt,
			Category: it.Category,
		})
	}
	return out
}

// adoptPendingPhoto 把 pending/<uploadID>.* 挪到 receipts/<receiptID>.*，返回相对路径。
func (a *App) adoptPendingPhoto(uploadID, receiptID string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(a.store.dataDir, "pending", uploadID+".*"))
	if err != nil || len(matches) == 0 {
		return "", errors.New("pending photo not found")
	}
	src := matches[0]
	ext := filepath.Ext(src)
	rel := "receipts/" + receiptID + ext
	dst := filepath.Join(a.store.dataDir, rel)
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	return rel, nil
}

// ---------- 小票列表与详情 ----------

func (a *App) listReceiptsHandler(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	f := ReceiptFilter{
		Month:    strings.TrimSpace(q.Get("month")),
		Category: strings.TrimSpace(q.Get("category")),
		StoreNo:  strings.TrimSpace(q.Get("store_no")),
		Page:     page,
		PerPage:  perPage,
	}
	receipts, total, err := a.store.ListReceipts(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	stores, _ := a.store.ListStores()
	writeJSON(w, http.StatusOK, map[string]any{
		"receipts": receipts, "total": total,
		"page": f.Page, "per_page": f.PerPage,
		"stores": stores, "categories": Categories,
	})
}

func (a *App) getReceiptHandler(w http.ResponseWriter, r *http.Request, u *User) {
	id := r.PathValue("id")
	rec, err := a.store.GetReceipt(id)
	if errors.Is(err, errNotFound) {
		writeErr(w, http.StatusNotFound, "小票不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	items, err := a.store.GetReceiptItems(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": rec, "items": items})
}

func (a *App) deleteReceiptHandler(w http.ResponseWriter, r *http.Request, u *User) {
	id := r.PathValue("id")
	rec, err := a.store.GetReceipt(id)
	if errors.Is(err, errNotFound) {
		writeErr(w, http.StatusNotFound, "小票不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	if err := a.store.DeleteReceipt(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败")
		return
	}
	if rec.PhotoPath != "" {
		os.Remove(filepath.Join(a.store.dataDir, filepath.FromSlash(rec.PhotoPath)))
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// photoHandler 返回小票照片原图（需登录）。
func (a *App) photoHandler(w http.ResponseWriter, r *http.Request, u *User) {
	id := r.PathValue("id")
	rec, err := a.store.GetReceipt(id)
	if err != nil || rec.PhotoPath == "" {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(a.store.dataDir, filepath.FromSlash(rec.PhotoPath))
	// 防目录穿越：PhotoPath 只由服务端生成，仍校验一次
	if !strings.HasPrefix(filepath.Clean(p), filepath.Join(a.store.dataDir, "receipts")) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, p)
}

// pendingPhotoHandler 返回待确认的上传照片（确认页展示用，需登录）。
func (a *App) pendingPhotoHandler(w http.ResponseWriter, r *http.Request, u *User) {
	uploadID := r.PathValue("uploadID")
	if uploadID == "" || strings.ContainsAny(uploadID, `/\`) {
		http.NotFound(w, r)
		return
	}
	matches, err := filepath.Glob(filepath.Join(a.store.dataDir, "pending", uploadID+".*"))
	if err != nil || len(matches) == 0 {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, matches[0])
}

// ---------- 搜索与商品详情 ----------

func (a *App) searchHandler(w http.ResponseWriter, r *http.Request, u *User) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	items, err := a.store.SearchItems(q, 30)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// itemDetailHandler 返回某货号的购买历史 + 价格观察 + 盯价状态。
func (a *App) itemDetailHandler(w http.ResponseWriter, r *http.Request, u *User) {
	itemNo := r.PathValue("itemNo")
	purchases, err := a.store.GetItemPurchases(itemNo)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	if len(purchases) == 0 {
		writeErr(w, http.StatusNotFound, "没有买过货号 "+itemNo+" 的商品")
		return
	}
	obs, _ := a.store.ObservationsFor(itemNo)
	watch, _ := a.store.GetWatch(itemNo)
	// 中文名：词典优先，否则取最近一次购买行的中文名
	nameZh := purchases[len(purchases)-1].NameZh
	if p, err := a.store.GetProduct(itemNo); err == nil && p.NameZh != "" {
		nameZh = p.NameZh
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item_no": itemNo, "name": purchases[0].Name, "name_zh": nameZh,
		"category":  purchases[0].Category,
		"purchases": purchases, "observations": obs, "watch": watch,
	})
}

// ---------- 手动记价 ----------

func (a *App) createObservationHandler(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		ItemNo     string `json:"item_no"`
		PriceCents int    `json:"price_cents"`
		Store      string `json:"store"`
		ObservedOn string `json:"observed_on"` // "2006-01-02"，空为今天
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.ItemNo = strings.TrimSpace(req.ItemNo)
	req.Store = strings.TrimSpace(req.Store)
	if req.ItemNo == "" || req.PriceCents <= 0 {
		writeErr(w, http.StatusBadRequest, "货号和价格不能为空")
		return
	}
	if req.ObservedOn == "" {
		req.ObservedOn = todayLocal()
	}
	if _, err := time.Parse("2006-01-02", req.ObservedOn); err != nil {
		writeErr(w, http.StatusBadRequest, "日期格式不对（YYYY-MM-DD）")
		return
	}
	// 品名：优先取购买记录里的，否则取盯价清单里的，否则用货号
	name := req.ItemNo
	if p, err := a.store.LatestPurchase(req.ItemNo); err == nil {
		name = p.Name
	} else if w, err := a.store.GetWatch(req.ItemNo); err == nil && w.Name != "" {
		name = w.Name
	}
	n := a.checkManualPrice(req.ItemNo, name, req.PriceCents, req.Store, req.ObservedOn)
	// 非盯价商品没有观察记录也照常记：手动记价本身就是一次观察，
	// 即使没盯价也应出现在商品价格线上（observePrice 只给盯价商品记，
	// 这里补记一次——盯价商品在 observePrice 里已记过，不重复）。
	if _, err := a.store.GetWatch(req.ItemNo); err != nil {
		a.store.AddObservation(&Observation{
			ItemNo: req.ItemNo, PriceCents: req.PriceCents, Store: req.Store,
			ObservedOn: req.ObservedOn, Source: "manual",
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"alerts_created": n})
}

// ---------- 提醒 ----------

func (a *App) listAlertsHandler(w http.ResponseWriter, r *http.Request, u *User) {
	status := r.URL.Query().Get("status")
	if status != "open" && status != "handled" && status != "dismissed" {
		status = "open"
	}
	alerts, err := a.store.ListAlerts(status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	open, _ := a.store.CountOpenAlerts()
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts, "open_count": open})
}

func (a *App) setAlertStatusHandler(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "提醒 id 不对")
		return
	}
	var req struct {
		Status string `json:"status"` // handled / dismissed
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Status != "handled" && req.Status != "dismissed" {
		writeErr(w, http.StatusBadRequest, "状态只能是 handled 或 dismissed")
		return
	}
	if err := a.store.SetAlertStatus(id, req.Status); err != nil {
		if errors.Is(err, errNotFound) {
			writeErr(w, http.StatusNotFound, "提醒不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- 盯价清单 ----------

func (a *App) listWatchlistHandler(w http.ResponseWriter, r *http.Request, u *User) {
	watches, err := a.store.ListWatchlist()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"watchlist": watches})
}

func (a *App) addWatchHandler(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		ItemNo      string `json:"item_no"`
		TargetCents *int   `json:"target_cents"` // 可空
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.ItemNo = strings.TrimSpace(req.ItemNo)
	if req.ItemNo == "" {
		writeErr(w, http.StatusBadRequest, "货号不能为空")
		return
	}
	if req.TargetCents != nil && *req.TargetCents <= 0 {
		writeErr(w, http.StatusBadRequest, "目标价必须大于 0")
		return
	}
	// 品名自动取最近一次购买记录；没买过就留空，以后小票进来再补
	name := ""
	if p, err := a.store.LatestPurchase(req.ItemNo); err == nil {
		name = p.Name
	}
	if err := a.store.AddWatch(req.ItemNo, name, req.TargetCents); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	a.backfillWatchObservation(req.ItemNo)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (a *App) updateWatchHandler(w http.ResponseWriter, r *http.Request, u *User) {
	itemNo := r.PathValue("itemNo")
	var req struct {
		TargetCents *int `json:"target_cents"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.TargetCents != nil && *req.TargetCents <= 0 {
		writeErr(w, http.StatusBadRequest, "目标价必须大于 0")
		return
	}
	watch, err := a.store.GetWatch(itemNo)
	if errors.Is(err, errNotFound) {
		writeErr(w, http.StatusNotFound, "不在盯价清单里")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "数据库错误")
		return
	}
	if err := a.store.AddWatch(itemNo, watch.Name, req.TargetCents); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) removeWatchHandler(w http.ResponseWriter, r *http.Request, u *User) {
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
