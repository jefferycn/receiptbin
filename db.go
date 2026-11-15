package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schema 是全部表结构。金额一律整数分（*_cents），避免浮点误差。
// 小票时间 purchased_at 存为 "2006-01-02 15:04"（收据本地时间，无时区），
// 字符串比较即时间比较；created_at 等系统时间用 UTC RFC3339Nano。
const schema = `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	is_admin INTEGER NOT NULL DEFAULT 0,
	disabled INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id),
	expires_at TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS receipts (
	id TEXT PRIMARY KEY,
	store TEXT NOT NULL DEFAULT '',
	store_no TEXT NOT NULL DEFAULT '',
	purchased_at TEXT NOT NULL,
	subtotal_cents INTEGER NOT NULL DEFAULT 0,
	tax_cents INTEGER NOT NULL DEFAULT 0,
	total_cents INTEGER NOT NULL DEFAULT 0,
	savings_cents INTEGER NOT NULL DEFAULT 0,
	item_count INTEGER NOT NULL DEFAULT 0,
	photo_path TEXT NOT NULL DEFAULT '',
	raw_text TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_receipts_purchased ON receipts(purchased_at DESC);
CREATE INDEX IF NOT EXISTS idx_receipts_store_date ON receipts(store_no, purchased_at, total_cents);

CREATE TABLE IF NOT EXISTS receipt_items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	receipt_id TEXT NOT NULL REFERENCES receipts(id) ON DELETE CASCADE,
	item_no TEXT NOT NULL,
	name TEXT NOT NULL,
	name_zh TEXT NOT NULL DEFAULT '',
	qty INTEGER NOT NULL DEFAULT 1,
	unit_price_cents INTEGER NOT NULL,
	discount_cents INTEGER NOT NULL DEFAULT 0,
	net_cents INTEGER NOT NULL,
	taxable INTEGER NOT NULL DEFAULT 0,
	food_exempt INTEGER NOT NULL DEFAULT 0,
	category TEXT NOT NULL DEFAULT '其他'
);
CREATE INDEX IF NOT EXISTS idx_items_receipt ON receipt_items(receipt_id);
CREATE INDEX IF NOT EXISTS idx_items_item_no ON receipt_items(item_no);
CREATE INDEX IF NOT EXISTS idx_items_category ON receipt_items(category);

-- 商品词典（v1.2）：货号 → 中文名 + 分类。Costco 货号跨小票稳定，
-- 是唯一可靠的键；助手通过 /api/products 批量更新，入库时自动套用，
-- 更新后历史商品行也会追溯改名改分类。
CREATE TABLE IF NOT EXISTS products (
	item_number TEXT PRIMARY KEY,
	name_zh TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	name_en TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);

-- 货号 → 分类映射：首次手动选分类后记住，下次同货号自动填。
CREATE TABLE IF NOT EXISTS item_categories (
	item_no TEXT PRIMARY KEY,
	category TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

-- 调价/盯价提醒。kind: price_drop（30 天内买贵了）、watch_drop（盯价商品降价）、
-- watch_target（盯价商品达到目标价）。deadline 只对 price_drop 有意义（购买日 +30 天）。
CREATE TABLE IF NOT EXISTS alerts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	item_no TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	old_price_cents INTEGER NOT NULL DEFAULT 0,
	new_price_cents INTEGER NOT NULL DEFAULT 0,
	old_receipt_id TEXT NOT NULL DEFAULT '',
	new_receipt_id TEXT NOT NULL DEFAULT '',
	purchase_date TEXT NOT NULL DEFAULT '',
	deadline TEXT NOT NULL DEFAULT '',
	diff_cents INTEGER NOT NULL DEFAULT 0,
	qty INTEGER NOT NULL DEFAULT 1,
	status TEXT NOT NULL DEFAULT 'open',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alerts_status_deadline ON alerts(status, deadline);
CREATE INDEX IF NOT EXISTS idx_alerts_item ON alerts(item_no);

-- 盯价清单：按货号盯，目标价可空（只看降价）。
CREATE TABLE IF NOT EXISTS watchlist (
	item_no TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	target_cents INTEGER,
	created_at TEXT NOT NULL
);

-- 价格观察记录：来源只有两条——receipt（新小票入库）与 manual（用户在店里看到随手记）。
-- 注意：没有实时货架价接口，应用不爬、不轮询任何价格源。
CREATE TABLE IF NOT EXISTS price_observations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	item_no TEXT NOT NULL,
	price_cents INTEGER NOT NULL,
	store TEXT NOT NULL DEFAULT '',
	observed_on TEXT NOT NULL,
	source TEXT NOT NULL,
	receipt_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_obs_item_date ON price_observations(item_no, observed_on DESC);
`

// Categories 是分类集合（DESIGN.md 定稿）。"其他" 为默认分类。
var Categories = []string{
	"生鲜", "肉类海鲜", "乳制品/饮品", "烘焙/干货",
	"日用品", "厨房用品", "宠物", "运动", "家居", "其他",
}

func validCategory(c string) bool {
	for _, x := range Categories {
		if x == c {
			return true
		}
	}
	return false
}

// Store 封装数据库与数据目录。
type Store struct {
	db      *sql.DB
	dataDir string // /data：放 receiptbin.db、receipts/ 与 pending/
}

// openStore 打开（或新建）数据库并建表。
func openStore(dataDir string) (*Store, error) {
	for _, d := range []string{"receipts", "pending"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o755); err != nil {
			return nil, err
		}
	}
	dbPath := filepath.Join(dataDir, "receiptbin.db")
	// WAL + busy_timeout：读写并发时少报错；foreign_keys 打开外键约束。
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单连接，避免写锁冲突
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// v1.2 迁移：v1.1 建的库 receipt_items 没有 name_zh 列，补上。
	// products 表由上面的 CREATE TABLE IF NOT EXISTS 直接建好，不用迁移。
	if err := migrateAddItemNameZh(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dataDir: dataDir}, nil
}

// migrateAddItemNameZh 给旧库的 receipt_items 补 name_zh 列（新库已有，跳过）。
func migrateAddItemNameZh(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(receipt_items)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "name_zh" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE receipt_items ADD COLUMN name_zh TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
