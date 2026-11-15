package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

var errNotFound = errors.New("not found")

// ---------- 用户与会话（与 SimpleBin 同款：首个注册为管理员 + 服务端会话表） ----------

// User 是 users 表的一行。
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	IsAdmin      bool   `json:"is_admin"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    string `json:"created_at"`
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(username, passwordHash string, isAdmin bool) (*User, error) {
	res, err := s.db.Exec(
		`INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, boolToInt(isAdmin), nowStr())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

func scanUser(row *sql.Row) (*User, error) {
	u := &User{}
	var admin, disabled int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &admin, &disabled, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = admin != 0
	u.Disabled = disabled != 0
	return u, nil
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	return scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, is_admin, disabled, created_at FROM users WHERE username = ?`, username))
}

func (s *Store) GetUserByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, is_admin, disabled, created_at FROM users WHERE id = ?`, id))
}

func (s *Store) SetUserPassword(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

func (s *Store) CreateSession(userID int64) (string, error) {
	id := randHex(32)
	expires := time.Now().UTC().Add(sessionDays * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		id, userID, expires, nowStr())
	return id, err
}

// GetSessionUser 校验 session：存在、未过期、用户未被禁用。
func (s *Store) GetSessionUser(sessionID string) (*User, error) {
	u := &User{}
	var admin, disabled int
	err := s.db.QueryRow(
		`SELECT u.id, u.username, u.password_hash, u.is_admin, u.disabled, u.created_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id = ? AND s.expires_at > ?`, sessionID, nowStr()).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &admin, &disabled, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = admin != 0
	u.Disabled = disabled != 0
	if u.Disabled {
		return nil, errNotFound
	}
	return u, nil
}

func (s *Store) DeleteSession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sessionID)
	return err
}

func (s *Store) DeleteExpiredSessions() {
	// 顺手清理，失败也无所谓
	s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, nowStr())
}

// DeleteUserSessions 吊销某用户全部会话（改密码后调用）。
func (s *Store) DeleteUserSessions(userID int64) {
	s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
}

// ---------- 小票与商品 ----------

// Receipt 是 receipts 表的一行。金额均为整数分。
type Receipt struct {
	ID            string `json:"id"`
	Store         string `json:"store"`
	StoreNo       string `json:"store_no"`
	PurchasedAt   string `json:"purchased_at"` // "2006-01-02 15:04"
	SubtotalCents int    `json:"subtotal_cents"`
	TaxCents      int    `json:"tax_cents"`
	TotalCents    int    `json:"total_cents"`
	SavingsCents  int    `json:"savings_cents"`
	ItemCount     int    `json:"item_count"`
	HasPhoto      bool   `json:"has_photo"`
	PhotoIsPDF    bool   `json:"photo_is_pdf"` // 附件是导出的 PDF（不是照片）
	PhotoPath     string `json:"-"`
	RawText       string `json:"-"`
	CreatedAt     string `json:"created_at"`
}

// ReceiptItem 是 receipt_items 表的一行（同一小票内同货号已聚合）。
// NameZh 是商品词典里的中文名（有则展示中文，没有则展示英文 Name）。
type ReceiptItem struct {
	ID             int64  `json:"id,omitempty"`
	ReceiptID      string `json:"receipt_id,omitempty"`
	ItemNo         string `json:"item_no"`
	Name           string `json:"name"`
	NameZh         string `json:"name_zh"`
	Qty            int    `json:"qty"`
	UnitPriceCents int    `json:"unit_price_cents"`
	DiscountCents  int    `json:"discount_cents"` // 每件折扣
	NetCents       int    `json:"net_cents"`      // qty * (unit - discount)
	Taxable        bool   `json:"taxable"`
	FoodExempt     bool   `json:"food_exempt"`
	Category       string `json:"category"`
	PurchasedAt    string `json:"purchased_at,omitempty"` // 购买历史里带上
	Store          string `json:"store,omitempty"`
	StoreNo        string `json:"store_no,omitempty"`
}

const receiptColumns = `id, store, store_no, purchased_at, subtotal_cents, tax_cents,
	total_cents, savings_cents, item_count, photo_path, raw_text, created_at`

func scanReceipt(row interface{ Scan(...any) error }) (*Receipt, error) {
	r := &Receipt{}
	err := row.Scan(&r.ID, &r.Store, &r.StoreNo, &r.PurchasedAt, &r.SubtotalCents,
		&r.TaxCents, &r.TotalCents, &r.SavingsCents, &r.ItemCount, &r.PhotoPath,
		&r.RawText, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	r.HasPhoto = r.PhotoPath != ""
	r.PhotoIsPDF = strings.HasSuffix(strings.ToLower(r.PhotoPath), ".pdf")
	return r, nil
}

func (s *Store) CountReceipts() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM receipts`).Scan(&n)
	return n, err
}

// FindDuplicateReceipt 找同门店、同时间、同总价的小票（防重复导入）。
func (s *Store) FindDuplicateReceipt(storeNo, purchasedAt string, totalCents int) (*Receipt, error) {
	return scanReceipt(s.db.QueryRow(
		`SELECT `+receiptColumns+` FROM receipts
		 WHERE store_no = ? AND purchased_at = ? AND total_cents = ? LIMIT 1`,
		storeNo, purchasedAt, totalCents))
}

// CreateReceipt 写入小票 + 商品行 + 分类记忆（同一事务）。
// 调用方已做对账校验；这里只负责落库。
func (s *Store) CreateReceipt(r *Receipt, items []ReceiptItem) error {
	// 门店名统一大写（最后一道关口：确认页手改、种子数据都走这里）。
	r.Store = strings.ToUpper(strings.TrimSpace(r.Store))
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowStr()
	_, err = tx.Exec(
		`INSERT INTO receipts (id, store, store_no, purchased_at, subtotal_cents, tax_cents,
			total_cents, savings_cents, item_count, photo_path, raw_text, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Store, r.StoreNo, r.PurchasedAt, r.SubtotalCents, r.TaxCents,
		r.TotalCents, r.SavingsCents, r.ItemCount, r.PhotoPath, r.RawText, now, now)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Category == "" || !validCategory(it.Category) {
			it.Category = "其他"
		}
		_, err = tx.Exec(
			`INSERT INTO receipt_items (receipt_id, item_no, name, name_zh, qty, unit_price_cents,
				discount_cents, net_cents, taxable, food_exempt, category)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, it.ItemNo, it.Name, it.NameZh, it.Qty, it.UnitPriceCents, it.DiscountCents,
			it.NetCents, boolToInt(it.Taxable), boolToInt(it.FoodExempt), it.Category)
		if err != nil {
			return err
		}
		// 记住分类：下次同货号自动填
		_, err = tx.Exec(
			`INSERT INTO item_categories (item_no, category, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT(item_no) DO UPDATE SET category = excluded.category, updated_at = excluded.updated_at`,
			it.ItemNo, it.Category, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetReceipt(id string) (*Receipt, error) {
	return scanReceipt(s.db.QueryRow(
		`SELECT `+receiptColumns+` FROM receipts WHERE id = ?`, id))
}

func (s *Store) GetReceiptItems(receiptID string) ([]ReceiptItem, error) {
	rows, err := s.db.Query(
		`SELECT id, receipt_id, item_no, name, name_zh, qty, unit_price_cents, discount_cents,
			net_cents, taxable, food_exempt, category FROM receipt_items
		 WHERE receipt_id = ? ORDER BY id`, receiptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReceiptItems(rows)
}

func scanReceiptItems(rows *sql.Rows) ([]ReceiptItem, error) {
	out := []ReceiptItem{}
	for rows.Next() {
		var it ReceiptItem
		var taxable, exempt int
		if err := rows.Scan(&it.ID, &it.ReceiptID, &it.ItemNo, &it.Name, &it.NameZh, &it.Qty,
			&it.UnitPriceCents, &it.DiscountCents, &it.NetCents, &taxable, &exempt,
			&it.Category); err != nil {
			return nil, err
		}
		it.Taxable = taxable != 0
		it.FoodExempt = exempt != 0
		out = append(out, it)
	}
	return out, rows.Err()
}

// ReceiptFilter 是小票列表的筛选条件。
type ReceiptFilter struct {
	Month    string // "2026-11"，空为全部
	Category string // 空为全部
	StoreNo  string // 空为全部
	Page     int
	PerPage  int
}

func (s *Store) ListReceipts(f ReceiptFilter) ([]*Receipt, int, error) {
	where := `1 = 1`
	args := []any{}
	if f.Month != "" {
		where += ` AND substr(r.purchased_at, 1, 7) = ?`
		args = append(args, f.Month)
	}
	if f.StoreNo != "" {
		where += ` AND r.store_no = ?`
		args = append(args, f.StoreNo)
	}
	join := ``
	if f.Category != "" {
		// 按分类筛：小票里至少有一件该分类商品
		join = ` JOIN receipt_items ri ON ri.receipt_id = r.id AND ri.category = ?`
		args = append([]any{f.Category}, args...)
	}
	var total int
	countArgs := append([]any{}, args...)
	if err := s.db.QueryRow(
		`SELECT COUNT(DISTINCT r.id) FROM receipts r `+join+` WHERE `+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.PerPage <= 0 || f.PerPage > 200 {
		f.PerPage = 50
	}
	if f.Page < 1 {
		f.Page = 1
	}
	offset := (f.Page - 1) * f.PerPage
	query := `SELECT DISTINCT ` + receiptColumnsWithPrefix("r") + ` FROM receipts r ` + join +
		` WHERE ` + where + ` ORDER BY r.purchased_at DESC LIMIT ? OFFSET ?`
	args = append(args, f.PerPage, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Receipt{}
	for rows.Next() {
		r, err := scanReceipt(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func receiptColumnsWithPrefix(p string) string {
	cols := []string{"id", "store", "store_no", "purchased_at", "subtotal_cents", "tax_cents",
		"total_cents", "savings_cents", "item_count", "photo_path", "raw_text", "created_at"}
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p + "." + c)
	}
	return b.String()
}

// ListStores 返回去过的所有门店（筛选下拉用）。
// 按门店号分组（门店号才是门店的唯一身份），店名统一大写显示，
// 旧数据里同一门店号的大小写不同写法只出现一次。
func (s *Store) ListStores() ([]struct {
	Store   string `json:"store"`
	StoreNo string `json:"store_no"`
}, error) {
	rows, err := s.db.Query(
		`SELECT UPPER(store) AS store, store_no FROM receipts GROUP BY store_no ORDER BY store`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []struct {
		Store   string `json:"store"`
		StoreNo string `json:"store_no"`
	}{}
	for rows.Next() {
		var x struct {
			Store   string `json:"store"`
			StoreNo string `json:"store_no"`
		}
		if err := rows.Scan(&x.Store, &x.StoreNo); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ListMonths 返回有小票的月份列表（YYYY-MM，倒序），供小票页筛选器用。
func (s *Store) ListMonths() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT strftime('%Y-%m', purchased_at) AS m FROM receipts ORDER BY m DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteReceipt 删小票：商品行由外键级联删除；相关提醒一起删掉；
// 照片文件由调用方删（数据库不知道文件是否还在）。
func (s *Store) DeleteReceipt(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM alerts WHERE old_receipt_id = ? OR new_receipt_id = ?`, id, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM price_observations WHERE receipt_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM receipts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return tx.Commit()
}

// ---------- 分类记忆 ----------

func (s *Store) GetCategory(itemNo string) string {
	var c string
	if err := s.db.QueryRow(
		`SELECT category FROM item_categories WHERE item_no = ?`, itemNo).Scan(&c); err != nil {
		return ""
	}
	return c
}

// CategoriesFor 批量查一批货号的分类（确认页用）。
func (s *Store) CategoriesFor(itemNos []string) map[string]string {
	out := map[string]string{}
	if len(itemNos) == 0 {
		return out
	}
	q := `SELECT item_no, category FROM item_categories WHERE item_no IN (`
	args := make([]any, 0, len(itemNos))
	for i, n := range itemNos {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, n)
	}
	q += ")"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var no, c string
		if err := rows.Scan(&no, &c); err == nil {
			out[no] = c
		}
	}
	return out
}

// KnownItemNumbers 返回库里出现过的全部货号（OCR 货号纠错用）。
func (s *Store) KnownItemNumbers() (map[string]bool, error) {
	rows, err := s.db.Query(
		`SELECT item_no FROM item_categories UNION SELECT item_no FROM receipt_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			return nil, err
		}
		out[no] = true
	}
	return out, rows.Err()
}

// ---------- 搜索与购买历史 ----------

// ItemSummary 是搜索结果里的一件商品（按货号聚合）。
// NameZh 有值时前端展示中文名，否则展示英文名。
type ItemSummary struct {
	ItemNo        string `json:"item_no"`
	Name          string `json:"name"`
	NameZh        string `json:"name_zh"`
	Category      string `json:"category"`
	PurchaseCount int    `json:"purchase_count"`
	LastPrice     int    `json:"last_price_cents"`
	LastDate      string `json:"last_date"`
	LastStore     string `json:"last_store"`
}

func likeEscape(q string) string {
	// LIKE 通配符转义，避免用户输入的 % _ 改变语义
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// SearchItems 按货号或品名搜商品（货号精确优先，品名模糊）。
// 品名同时匹配英文名与中文名（中文名用普通 LIKE，不转大写）。
func (s *Store) SearchItems(q string, limit int) ([]ItemSummary, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []ItemSummary{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.Query(
		`SELECT ri.item_no, MAX(ri.name), MAX(ri.name_zh), MAX(ri.category), COUNT(*),
			(SELECT ri2.unit_price_cents - ri2.discount_cents FROM receipt_items ri2
			 JOIN receipts r2 ON r2.id = ri2.receipt_id
			 WHERE ri2.item_no = ri.item_no ORDER BY r2.purchased_at DESC LIMIT 1),
			MAX(r.purchased_at),
			(SELECT r3.store FROM receipt_items ri3
			 JOIN receipts r3 ON r3.id = ri3.receipt_id
			 WHERE ri3.item_no = ri.item_no ORDER BY r3.purchased_at DESC LIMIT 1)
		 FROM receipt_items ri JOIN receipts r ON r.id = ri.receipt_id
		 WHERE ri.item_no LIKE ? ESCAPE '\' OR UPPER(ri.name) LIKE UPPER(?) ESCAPE '\'
		 OR ri.name_zh LIKE ? ESCAPE '\'
		 GROUP BY ri.item_no
		 ORDER BY CASE WHEN ri.item_no = ? THEN 0 ELSE 1 END, MAX(r.purchased_at) DESC
		 LIMIT ?`,
		likeEscape(q), likeEscape(q), likeEscape(q), q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ItemSummary{}
	for rows.Next() {
		var x ItemSummary
		if err := rows.Scan(&x.ItemNo, &x.Name, &x.NameZh, &x.Category, &x.PurchaseCount,
			&x.LastPrice, &x.LastDate, &x.LastStore); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// GetItemPurchases 返回某货号的全部购买记录（时间升序，画价格线用）。
func (s *Store) GetItemPurchases(itemNo string) ([]ReceiptItem, error) {
	rows, err := s.db.Query(
		`SELECT ri.id, ri.receipt_id, ri.item_no, ri.name, ri.name_zh, ri.qty, ri.unit_price_cents,
			ri.discount_cents, ri.net_cents, ri.taxable, ri.food_exempt, ri.category,
			r.purchased_at, r.store, r.store_no
		 FROM receipt_items ri JOIN receipts r ON r.id = ri.receipt_id
		 WHERE ri.item_no = ? ORDER BY r.purchased_at ASC`, itemNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReceiptItem{}
	for rows.Next() {
		var it ReceiptItem
		var taxable, exempt int
		if err := rows.Scan(&it.ID, &it.ReceiptID, &it.ItemNo, &it.Name, &it.NameZh, &it.Qty,
			&it.UnitPriceCents, &it.DiscountCents, &it.NetCents, &taxable, &exempt,
			&it.Category, &it.PurchasedAt, &it.Store, &it.StoreNo); err != nil {
			return nil, err
		}
		it.Taxable = taxable != 0
		it.FoodExempt = exempt != 0
		out = append(out, it)
	}
	return out, rows.Err()
}

// LatestPurchase 返回某货号最近一次购买（盯价回填用）。
func (s *Store) LatestPurchase(itemNo string) (*ReceiptItem, error) {
	row := s.db.QueryRow(
		`SELECT ri.id, ri.receipt_id, ri.item_no, ri.name, ri.name_zh, ri.qty, ri.unit_price_cents,
			ri.discount_cents, ri.net_cents, ri.taxable, ri.food_exempt, ri.category,
			r.purchased_at, r.store, r.store_no
		 FROM receipt_items ri JOIN receipts r ON r.id = ri.receipt_id
		 WHERE ri.item_no = ? ORDER BY r.purchased_at DESC LIMIT 1`, itemNo)
	var it ReceiptItem
	var taxable, exempt int
	err := row.Scan(&it.ID, &it.ReceiptID, &it.ItemNo, &it.Name, &it.NameZh, &it.Qty,
		&it.UnitPriceCents, &it.DiscountCents, &it.NetCents, &taxable, &exempt,
		&it.Category, &it.PurchasedAt, &it.Store, &it.StoreNo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	it.Taxable = taxable != 0
	it.FoodExempt = exempt != 0
	return &it, nil
}

// EarlierPurchases 返回某货号在 [from, before) 时间段内的购买记录
// （调价比对用：比新小票早、且在 30 天窗口内的购买）。
func (s *Store) EarlierPurchases(itemNo, from, before string) ([]ReceiptItem, error) {
	rows, err := s.db.Query(
		`SELECT ri.id, ri.receipt_id, ri.item_no, ri.name, ri.name_zh, ri.qty, ri.unit_price_cents,
			ri.discount_cents, ri.net_cents, ri.taxable, ri.food_exempt, ri.category,
			r.purchased_at, r.store, r.store_no
		 FROM receipt_items ri JOIN receipts r ON r.id = ri.receipt_id
		 WHERE ri.item_no = ? AND r.purchased_at >= ? AND r.purchased_at < ?
		 ORDER BY r.purchased_at DESC`, itemNo, from, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReceiptItem{}
	for rows.Next() {
		var it ReceiptItem
		var taxable, exempt int
		if err := rows.Scan(&it.ID, &it.ReceiptID, &it.ItemNo, &it.Name, &it.NameZh, &it.Qty,
			&it.UnitPriceCents, &it.DiscountCents, &it.NetCents, &taxable, &exempt,
			&it.Category, &it.PurchasedAt, &it.Store, &it.StoreNo); err != nil {
			return nil, err
		}
		it.Taxable = taxable != 0
		it.FoodExempt = exempt != 0
		out = append(out, it)
	}
	return out, rows.Err()
}

// ---------- 统计 ----------

// MonthTotal 是一月的花费（含税总价）。
type MonthTotal struct {
	Month        string `json:"month"` // "2026-11"
	TotalCents   int    `json:"total_cents"`
	Receipts     int    `json:"receipts"`
	SavingsCents int    `json:"savings_cents"`
}

// CategoryTotal 是一分类的花费（商品实付，不含税；税无法按分类分摊）。
type CategoryTotal struct {
	Category   string `json:"category"`
	TotalCents int    `json:"total_cents"`
	Items      int    `json:"items"`
}

// TotalsByMonth 返回最近 12 个月的花费（时间升序，画柱状图用）。
func (s *Store) TotalsByMonth() ([]MonthTotal, error) {
	rows, err := s.db.Query(
		`SELECT substr(purchased_at, 1, 7) AS m, SUM(total_cents), COUNT(*), SUM(savings_cents)
		 FROM receipts GROUP BY m ORDER BY m DESC LIMIT 12`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonthTotal{}
	for rows.Next() {
		var x MonthTotal
		if err := rows.Scan(&x.Month, &x.TotalCents, &x.Receipts, &x.SavingsCents); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	// 反转为升序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// TotalsByCategory 返回某时段的分类花费。period: "all" / "YYYY" / "YYYY-MM"。
func (s *Store) TotalsByCategory(period string) ([]CategoryTotal, error) {
	where := ``
	args := []any{}
	switch len(period) {
	case 4: // 年
		where = `WHERE substr(r.purchased_at, 1, 4) = ?`
		args = append(args, period)
	case 7: // 月
		where = `WHERE substr(r.purchased_at, 1, 7) = ?`
		args = append(args, period)
	}
	rows, err := s.db.Query(
		`SELECT ri.category, SUM(ri.net_cents), SUM(ri.qty)
		 FROM receipt_items ri JOIN receipts r ON r.id = ri.receipt_id `+where+`
		 GROUP BY ri.category ORDER BY SUM(ri.net_cents) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CategoryTotal{}
	for rows.Next() {
		var x CategoryTotal
		if err := rows.Scan(&x.Category, &x.TotalCents, &x.Items); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SumTotals 返回总花费、今年花费、本月花费（含税）。
func (s *Store) SumTotals() (all, year, month int, err error) {
	now := time.Now()
	y := now.Format("2006")
	m := now.Format("2006-01")
	err = s.db.QueryRow(
		`SELECT COALESCE(SUM(total_cents), 0),
			COALESCE(SUM(CASE WHEN substr(purchased_at, 1, 4) = ? THEN total_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN substr(purchased_at, 1, 7) = ? THEN total_cents ELSE 0 END), 0)
		 FROM receipts`, y, m).Scan(&all, &year, &month)
	return all, year, month, err
}

// ---------- 提醒 ----------

// Alert 是 alerts 表的一行。
type Alert struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"` // price_drop / watch_drop / watch_target
	ItemNo        string `json:"item_no"`
	Name          string `json:"name"`
	OldPriceCents int    `json:"old_price_cents"`
	NewPriceCents int    `json:"new_price_cents"`
	OldReceiptID  string `json:"old_receipt_id"`
	NewReceiptID  string `json:"new_receipt_id"`
	PurchaseDate  string `json:"purchase_date"`
	Deadline      string `json:"deadline"`
	DiffCents     int    `json:"diff_cents"`
	Qty           int    `json:"qty"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	DaysLeft      int    `json:"days_left"` // 仅 price_drop 有意义
}

func (s *Store) CreateAlert(a *Alert) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO alerts (kind, item_no, name, old_price_cents, new_price_cents,
			old_receipt_id, new_receipt_id, purchase_date, deadline, diff_cents, qty, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
		a.Kind, a.ItemNo, a.Name, a.OldPriceCents, a.NewPriceCents,
		a.OldReceiptID, a.NewReceiptID, a.PurchaseDate, a.Deadline, a.DiffCents, a.Qty, nowStr())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// HasOpenPriceDropAlert 查某张旧小票是否已有同价的未处理调价提醒（去重用）。
// 新小票是哪张不参与比较：提醒的内容是“这张旧小票能退差价”，
// 之后再来同价小票不应重复提醒。
func (s *Store) HasOpenPriceDropAlert(itemNo, oldReceiptID string, newPrice int) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM alerts
		 WHERE status = 'open' AND kind = 'price_drop' AND item_no = ?
		 AND old_receipt_id = ? AND new_price_cents = ?`,
		itemNo, oldReceiptID, newPrice).Scan(&n)
	return n > 0, err
}

// HasOpenWatchAlert 查某盯价商品是否已有同类未处理提醒（同价去重）。
func (s *Store) HasOpenWatchAlert(kind, itemNo string, newPrice int) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM alerts
		 WHERE status = 'open' AND kind = ? AND item_no = ? AND new_price_cents = ?`,
		kind, itemNo, newPrice).Scan(&n)
	return n > 0, err
}

func (s *Store) ListAlerts(status string) ([]Alert, error) {
	rows, err := s.db.Query(
		`SELECT id, kind, item_no, name, old_price_cents, new_price_cents,
			old_receipt_id, new_receipt_id, purchase_date, deadline, diff_cents, qty, status, created_at
		 FROM alerts WHERE status = ?
		 ORDER BY CASE WHEN deadline = '' THEN 1 ELSE 0 END, deadline ASC, created_at DESC`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.Kind, &a.ItemNo, &a.Name, &a.OldPriceCents,
			&a.NewPriceCents, &a.OldReceiptID, &a.NewReceiptID, &a.PurchaseDate,
			&a.Deadline, &a.DiffCents, &a.Qty, &a.Status, &a.CreatedAt); err != nil {
			return nil, err
		}
		if a.Deadline != "" {
			a.DaysLeft = daysUntil(a.Deadline)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CountOpenAlerts() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status = 'open'`).Scan(&n)
	return n, err
}

func (s *Store) SetAlertStatus(id int64, status string) error {
	res, err := s.db.Exec(`UPDATE alerts SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// ---------- 盯价清单与价格观察 ----------

// Watch 是 watchlist 表的一行；TargetCents 为 nil 表示只看降价、不限目标价。
// NameZh 不进 watchlist 表，取商品词典（有中文名时前端展示中文）。
type Watch struct {
	ItemNo      string `json:"item_no"`
	Name        string `json:"name"`
	NameZh      string `json:"name_zh"`
	TargetCents *int   `json:"target_cents"`
	CreatedAt   string `json:"created_at"`
	// LastObs 是最近一次观察（列表展示用，不进 watchlist 表）
	LastObs *Observation `json:"last_obs,omitempty"`
}

// Observation 是 price_observations 表的一行。
type Observation struct {
	ID         int64  `json:"id,omitempty"`
	ItemNo     string `json:"item_no"`
	PriceCents int    `json:"price_cents"`
	Store      string `json:"store"`
	ObservedOn string `json:"observed_on"` // "2006-01-02"
	Source     string `json:"source"`      // receipt / manual / watch
	ReceiptID  string `json:"receipt_id,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

func (s *Store) AddWatch(itemNo, name string, target *int) error {
	_, err := s.db.Exec(
		`INSERT INTO watchlist (item_no, name, target_cents, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(item_no) DO UPDATE SET name = excluded.name, target_cents = excluded.target_cents`,
		itemNo, name, target, nowStr())
	return err
}

func (s *Store) RemoveWatch(itemNo string) error {
	res, err := s.db.Exec(`DELETE FROM watchlist WHERE item_no = ?`, itemNo)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) GetWatch(itemNo string) (*Watch, error) {
	w := &Watch{}
	var target sql.NullInt64
	err := s.db.QueryRow(
		`SELECT item_no, name, target_cents, created_at FROM watchlist WHERE item_no = ?`,
		itemNo).Scan(&w.ItemNo, &w.Name, &target, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if target.Valid {
		v := int(target.Int64)
		w.TargetCents = &v
	}
	if p, err := s.GetProduct(itemNo); err == nil {
		w.NameZh = p.NameZh
	}
	return w, nil
}

func (s *Store) ListWatchlist() ([]Watch, error) {
	rows, err := s.db.Query(
		`SELECT item_no, name, target_cents, created_at FROM watchlist ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Watch{}
	for rows.Next() {
		var w Watch
		var target sql.NullInt64
		if err := rows.Scan(&w.ItemNo, &w.Name, &target, &w.CreatedAt); err != nil {
			return nil, err
		}
		if target.Valid {
			v := int(target.Int64)
			w.TargetCents = &v
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 中文名：批量查商品词典补上
	nos := make([]string, 0, len(out))
	for i := range out {
		nos = append(nos, out[i].ItemNo)
	}
	for no, p := range s.ProductsFor(nos) {
		for i := range out {
			if out[i].ItemNo == no {
				out[i].NameZh = p.NameZh
			}
		}
	}
	for i := range out {
		if obs, err := s.LatestObservation(out[i].ItemNo); err == nil {
			out[i].LastObs = obs
		}
	}
	return out, nil
}

func (s *Store) AddObservation(o *Observation) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO price_observations (item_no, price_cents, store, observed_on, source, receipt_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.ItemNo, o.PriceCents, o.Store, o.ObservedOn, o.Source, o.ReceiptID, nowStr())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LatestObservation 返回某货号最近一次观察（日期最新，同日取最新写入）。
func (s *Store) LatestObservation(itemNo string) (*Observation, error) {
	o := &Observation{}
	err := s.db.QueryRow(
		`SELECT id, item_no, price_cents, store, observed_on, source, receipt_id, created_at
		 FROM price_observations WHERE item_no = ?
		 ORDER BY observed_on DESC, id DESC LIMIT 1`, itemNo).
		Scan(&o.ID, &o.ItemNo, &o.PriceCents, &o.Store, &o.ObservedOn, &o.Source, &o.ReceiptID, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// ObservationsFor 返回某货号的全部观察（日期升序，画价格线用）。
func (s *Store) ObservationsFor(itemNo string) ([]Observation, error) {
	rows, err := s.db.Query(
		`SELECT id, item_no, price_cents, store, observed_on, source, receipt_id, created_at
		 FROM price_observations WHERE item_no = ? ORDER BY observed_on ASC, id ASC`, itemNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Observation{}
	for rows.Next() {
		var o Observation
		if err := rows.Scan(&o.ID, &o.ItemNo, &o.PriceCents, &o.Store, &o.ObservedOn,
			&o.Source, &o.ReceiptID, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
