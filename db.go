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
	terminal_no TEXT NOT NULL DEFAULT '',
	transaction_no TEXT NOT NULL DEFAULT '',
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
-- 注意：terminal_no / transaction_no 上的索引不能放这里：旧库还没有这两列，
-- 建索引会在迁移之前先失败、导致启动崩溃（v2.4 实测）。索引由迁移函数建。

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

-- 价格观察记录：来源有三条——receipt（新小票入库）、manual（用户在店里看到随手记）、
-- watch（助手盯价检查经机器 API 写入）。应用本身不爬、不轮询任何价格源。
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

-- 退货清单（v2.13）：从小票详情把商品行加入，退掉后标记已处理。
-- 一条记录挂在一个 receipt_items 行上（同小票同货号已聚合，一行一件商品）；
-- qty 是待退数量（可少于购买数量，如买 5 退 2）。
CREATE TABLE IF NOT EXISTS returns (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	receipt_item_id INTEGER NOT NULL REFERENCES receipt_items(id) ON DELETE CASCADE,
	qty INTEGER NOT NULL DEFAULT 1,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at TEXT NOT NULL,
	done_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_returns_status ON returns(status, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_returns_pending_item ON returns(receipt_item_id) WHERE status = 'pending';
`

// Categories 是分类集合（DESIGN.md 定稿）。"其他" 为默认分类。
var Categories = []string{
	"生鲜", "肉类海鲜", "乳制品/饮品", "烘焙/干货",
	"日用品", "厨房用品", "宠物", "运动", "家居", "礼品卡", "其他",
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
	// v1.6 迁移：旧数据里门店名大小写不一（如 Danville 与 DANVILLE），统一大写。
	if err := migrateUppercaseStores(db); err != nil {
		db.Close()
		return nil, err
	}
	// v2.4 迁移：给旧库的 receipts 补收银台号与交易号两列（判重用，新库已有）。
	if err := migrateAddTxnColumns(db); err != nil {
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

// migrateAddTxnColumns 给旧库的 receipts 补 terminal_no / transaction_no 两列
// （新库已有，跳过；旧数据这两列为空，判重时走总价兜底）。
func migrateAddTxnColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(receipts)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, col := range []string{"terminal_no", "transaction_no"} {
		if !have[col] {
			if _, err := db.Exec(`ALTER TABLE receipts ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	// 交易号索引：列补齐之后才能建（放 schema 里会在旧库上先失败导致启动崩溃）。
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_receipts_txn ON receipts(store_no, terminal_no, transaction_no)`)
	return err
}

// migrateUppercaseStores 把旧小票与旧记价的门店名统一成大写（幂等：已大写的行不动）。
func migrateUppercaseStores(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE receipts SET store = UPPER(store) WHERE store != UPPER(store)`); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE price_observations SET store = UPPER(store) WHERE store != UPPER(store)`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
