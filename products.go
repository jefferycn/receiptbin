package main

import (
	"database/sql"
	"errors"
	"strings"
)

// 商品词典（v1.2）：按 Costco 货号存中文名与分类。
//
// 货号是唯一可靠的键：同一商品在每张小票上的英文缩写可能略有不同
// （OCR 噪声、Costco 自己改缩写），但货号稳定。助手通过 /api/products
// 批量更新词典；新小票入库时自动套用；更新会追溯改掉历史商品行的
// 中文名与分类（小票本身的英文名、金额不动）。

// Product 是 products 表的一行。
type Product struct {
	ItemNumber string `json:"item_number"`
	NameZh     string `json:"name_zh"`
	Category   string `json:"category"`
	NameEn     string `json:"name_en"` // 最后一次见到的英文名，仅供对照
	UpdatedAt  string `json:"updated_at"`
}

// ProductUpdate 是一次词典更新里的单条：NameZh 与 Category 各自可选
// （nil = 不动该字段），但至少要提供一个。
type ProductUpdate struct {
	ItemNumber string
	NameZh     *string
	Category   *string
}

// UpsertResult 是单条更新的结果。
type UpsertResult struct {
	ItemNumber  string `json:"item_number"`
	Created     bool   `json:"created"`      // true = 新建词条，false = 更新已有词条
	RowsUpdated int    `json:"rows_updated"` // 追溯改掉的历史商品行数
}

// UnmappedItem 是还没进词典的已购商品（供助手发现哪些需要命名）。
type UnmappedItem struct {
	ItemNo        string `json:"item_no"`
	Name          string `json:"name"` // 最后一次见到的英文名
	PurchaseCount int    `json:"purchase_count"`
	LastDate      string `json:"last_date"`
}

// ListProducts 返回全部词条（按货号排序）。
func (s *Store) ListProducts() ([]Product, error) {
	rows, err := s.db.Query(
		`SELECT item_number, name_zh, category, name_en, updated_at FROM products ORDER BY item_number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ItemNumber, &p.NameZh, &p.Category, &p.NameEn, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProduct 查单个词条，不存在返回 errNotFound。
func (s *Store) GetProduct(itemNo string) (*Product, error) {
	p := &Product{}
	err := s.db.QueryRow(
		`SELECT item_number, name_zh, category, name_en, updated_at FROM products WHERE item_number = ?`,
		itemNo).Scan(&p.ItemNumber, &p.NameZh, &p.Category, &p.NameEn, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ProductsFor 批量查一批货号的词条（入库套用、盯价展示用）。
func (s *Store) ProductsFor(itemNos []string) map[string]Product {
	out := map[string]Product{}
	if len(itemNos) == 0 {
		return out
	}
	q := `SELECT item_number, name_zh, category, name_en, updated_at FROM products WHERE item_number IN (`
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
		var p Product
		if err := rows.Scan(&p.ItemNumber, &p.NameZh, &p.Category, &p.NameEn, &p.UpdatedAt); err == nil {
			out[p.ItemNumber] = p
		}
	}
	return out
}

// UpsertProducts 批量新增/更新词条，并追溯更新历史商品行。
// 同一事务内完成：词典行 + 历史商品行 + 分类记忆要么全改，要么全不动。
// 调用方已校验 category 合法、每条至少提供一个字段；这里只负责落库。
func (s *Store) UpsertProducts(entries []ProductUpdate) ([]UpsertResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowStr()
	results := make([]UpsertResult, 0, len(entries))
	for _, e := range entries {
		itemNo := strings.TrimSpace(e.ItemNumber)
		// 读已有词条（没有就是新建），只覆盖提供的字段
		var cur Product
		var created bool
		err := tx.QueryRow(
			`SELECT item_number, name_zh, category, name_en, updated_at FROM products WHERE item_number = ?`,
			itemNo).Scan(&cur.ItemNumber, &cur.NameZh, &cur.Category, &cur.NameEn, &cur.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			created = true
			cur = Product{ItemNumber: itemNo}
		} else if err != nil {
			return nil, err
		}
		if e.NameZh != nil {
			cur.NameZh = strings.TrimSpace(*e.NameZh)
		}
		if e.Category != nil {
			cur.Category = strings.TrimSpace(*e.Category)
		}
		// name_en：从最近一次购买记录里取英文名（仅供对照，不影响展示）
		var lastName string
		if err := tx.QueryRow(
			`SELECT ri.name FROM receipt_items ri JOIN receipts r ON r.id = ri.receipt_id
			 WHERE ri.item_no = ? ORDER BY r.purchased_at DESC LIMIT 1`,
			itemNo).Scan(&lastName); err == nil && lastName != "" {
			cur.NameEn = lastName
		}
		cur.UpdatedAt = now
		if _, err := tx.Exec(
			`INSERT INTO products (item_number, name_zh, category, name_en, updated_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(item_number) DO UPDATE SET
				name_zh = excluded.name_zh, category = excluded.category,
				name_en = excluded.name_en, updated_at = excluded.updated_at`,
			cur.ItemNumber, cur.NameZh, cur.Category, cur.NameEn, cur.UpdatedAt); err != nil {
			return nil, err
		}
		// 追溯：改掉该货号全部历史商品行的中文名/分类（只改提供的字段）
		rowsUpdated, err := retroUpdateItems(tx, itemNo, e.NameZh, e.Category)
		if err != nil {
			return nil, err
		}
		// 分类记忆同步：下次确认页的兜底分类也用新分类
		if e.Category != nil {
			if _, err := tx.Exec(
				`INSERT INTO item_categories (item_no, category, updated_at) VALUES (?, ?, ?)
				 ON CONFLICT(item_no) DO UPDATE SET category = excluded.category, updated_at = excluded.updated_at`,
				itemNo, cur.Category, now); err != nil {
				return nil, err
			}
		}
		results = append(results, UpsertResult{
			ItemNumber: itemNo, Created: created, RowsUpdated: rowsUpdated,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

// retroUpdateItems 追溯更新某货号的历史商品行：只改非 nil 的字段。
// 返回改动的行数（UPDATE 影响行数；值没变的行 SQLite 不计入）。
func retroUpdateItems(tx *sql.Tx, itemNo string, nameZh, category *string) (int, error) {
	set := []string{}
	args := []any{}
	if nameZh != nil {
		set = append(set, "name_zh = ?")
		args = append(args, strings.TrimSpace(*nameZh))
	}
	if category != nil {
		set = append(set, "category = ?")
		args = append(args, strings.TrimSpace(*category))
	}
	if len(set) == 0 {
		return 0, nil
	}
	args = append(args, itemNo)
	res, err := tx.Exec(
		`UPDATE receipt_items SET `+strings.Join(set, ", ")+` WHERE item_no = ?`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteProduct 删一条词条。历史商品行已写入的中文名/分类保留不动，
// 不会因为删词条而被清空（删词条只影响以后的新小票）。
func (s *Store) DeleteProduct(itemNo string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM products WHERE item_number = ?`, itemNo)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListUnmapped 返回买过、但还没进词典的商品（按最近购买倒序）。
// 助手用它发现哪些商品还等着命名与分类。
func (s *Store) ListUnmapped() ([]UnmappedItem, error) {
	rows, err := s.db.Query(
		`SELECT ri.item_no,
			(SELECT ri2.name FROM receipt_items ri2
			 JOIN receipts r2 ON r2.id = ri2.receipt_id
			 WHERE ri2.item_no = ri.item_no ORDER BY r2.purchased_at DESC LIMIT 1),
			COUNT(*), MAX(r.purchased_at)
		 FROM receipt_items ri JOIN receipts r ON r.id = ri.receipt_id
		 WHERE ri.item_no NOT IN (SELECT item_number FROM products)
		 GROUP BY ri.item_no ORDER BY MAX(r.purchased_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnmappedItem{}
	for rows.Next() {
		var x UnmappedItem
		if err := rows.Scan(&x.ItemNo, &x.Name, &x.PurchaseCount, &x.LastDate); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// applyProducts 把词典套到解析出的商品行上：有中文名就填中文名，
// 有分类就用词典分类；词典没分类的，退回分类记忆或“其他”。
// 上传解析（uploadHandler）与保存（createReceiptHandler）共用。
func applyProducts(items []ParsedItem, cats map[string]string, prods map[string]Product) {
	for i := range items {
		it := &items[i]
		if p, ok := prods[it.ItemNo]; ok {
			it.NameZh = p.NameZh
			if p.Category != "" {
				it.Category = p.Category
				continue
			}
		}
		if c, ok := cats[it.ItemNo]; ok {
			it.Category = c
		} else if it.Category == "" || !validCategory(it.Category) {
			it.Category = "其他"
		}
	}
}
