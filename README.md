# ReceiptBin

自建 Costco 购物记录：每周去 Costco，买过的东西都管起来。

- **扫小票入库**：手机拍照上传，本地 OCR（tesseract）识别，确认页核对后保存；金额对不上账不让入库。
- **导 PDF 入库**：上传 Costco 官网导出的 In-Warehouse 小票 PDF，直接读文本层解析，比 OCR 更准；会员号在读取时自动去掉，不存库。
- **分类统计**：仪表盘看本月 / 今年 / 累计花费，每月柱状图、分类环形图，点图表进对应小票列表。
- **查商品**：按货号或品名搜，看每次购买的日期、门店、价格，还有价格走势线。
- **调价提醒**：Costco 购买后 30 天内自己降价可退差价——新小票入库或手动记价时自动比对，生成提醒（含截止日与可退金额）。
- **盯价清单**：按货号盯特定商品，降价或到目标价时提醒。
- **商品词典**：按货号存中文名与分类，新小票入库时自动套用，更新后历史记录也追溯改名改分类；助手通过 `/api/products`（Bearer token）批量更新。

技术栈与 SimpleBin v2 一致：Go + SQLite，单静态二进制，Docker 部署，iOS 26 Liquid Glass 风格网页，iPhone 可 Add to Home Screen 当 App 用。

## 价格从哪里来（实话）

没有实时货架价接口，应用**不爬、不轮询**任何价格源。价格观察只来自两条：

1. **新扫的小票**——入库时自动更新盯价商品的观察价，并比对 30 天内购买记录；
2. **手动记价**——在店里看到价格，随手在商品详情页或盯价页记下来。

每月券册（Instant Savings PDF）匹配是 Phase 2，目前只有页面上的提示，没有解析功能。

## 部署（群晖 NAS）

```bash
git clone https://github.com/jefferycn/receiptbin.git
cd receiptbin
docker compose up -d --build
```

- 宿主机端口 **11026**（SimpleBin 用 11025，别撞），容器内 8080。
- 反代：把你自己的域名指到 NAS 的 11026（如 `receipt.example.com`）。
- 首次打开：注册首个管理员（之后不再开放注册）。空库时一张示例小票会自动导入，开箱即有数据可看（示例数据全是虚构的）。
- 备份：把 `data/` 目录整个拷走即可（数据库 + 小票照片都在里面）。
- 更新：`git pull && docker compose up -d --build`。
- 商品词典 API（可选）：想让助手帮你批量改中文名与分类时，先生成 token 并写进 `.env`，再重启：
  ```bash
  openssl rand -hex 24   # 生成 token，记下输出
  cp .env.example .env   # 然后把 token 填进 .env 的 RECEIPTBIN_API_TOKEN
  docker compose up -d --build
  ```
  不配 token 时 `/api/products` 返回 503，网页功能不受影响。`.env` 不进仓库。

内存说明：Go 本身常驻约 10–25MB，但 tesseract OCR 大图时峰值能到 200–400MB，compose 里 `mem_limit: 512m` 是给 OCR 留的，不要调太小。

## 使用

- **扫小票**：首页、小票页或列表页右下角的扫小票按钮，拍照或选照片；识别后在确认页核对（金额、品名、数量可改），对账通过才能保存。分类与中文名由商品词典自动填，确认页不用选分类；词典没覆盖的商品默认“其他”，事后用词典 API 批量改。
- **导 PDF**：costco.com → Orders & Purchases → Warehouse 页签 → View Receipt，保存/打印为 PDF 后上传。PDF 自带文本层，直接解析不用 OCR；会员号在读取时自动去掉。注意：新买的东西最长 24 小时才出现在官网记录里。
- **查商品**：搜索页输货号或品名（中文名也能搜），点进商品详情看购买记录与价格走势；货号可一键复制。
- **调价提醒**：提醒页按截止日排序，办完点“已处理”。规则：购买后 30 天内、同渠道、有货、促销期内，带小票去会员柜台办；.97 清仓价不参与。
- **盯价**：盯价页按货号加，目标价可选；新小票或手动记价触发降价/到价提醒。

## 商品词典 API（v1.2）

小票上的英文品名是缩写（如 `KS TILAPIA`），不一定认得出来，自动猜的分类也可能错。商品词典按 Costco 货号存中文名与分类：新小票入库时自动套用；更新词典后，历史商品行也追溯改名改分类。

接口都在 `/api/products`，需要 Bearer token（`RECEIPTBIN_API_TOKEN`，见部署一节）。不接受网页登录 Cookie；token 未配置时返回 503，token 不对时返回 401。

```bash
# 下面用 YOUR_TOKEN_HERE 代替真实 token；域名换成你自己的
BASE=https://receipt.example.com
TOKEN=YOUR_TOKEN_HERE

# 查哪些买过的商品还没命名（返回货号、英文名、购买次数、最近购买日）
curl -H "Authorization: Bearer $TOKEN" $BASE/api/products/unmapped

# 批量更新中文名与分类（name_zh 与 category 各自可选，但每条至少给一个；
# 一次最多 500 条；响应按条报告是否新建与追溯改掉的行数）
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"items":[
    {"item_number":"9000001","name_zh":"测试果汁","category":"乳制品/饮品"},
    {"item_number":"9000002","name_zh":"测试小物"}
  ]}' $BASE/api/products

# 列出全部词条（按货号排序）
curl -H "Authorization: Bearer $TOKEN" $BASE/api/products

# 删一条词条（历史商品行已写入的中文名/分类保留不动）
curl -X DELETE -H "Authorization: Bearer $TOKEN" $BASE/api/products/9000002
```

分类必须是这十一个之一：生鲜、肉类海鲜、乳制品/饮品、烘焙/干货、日用品、厨房用品、宠物、运动、家居、礼品卡、其他。

## 盯价清单 API（v1.8）

与词典 API 共用同一个 Bearer <redacted>，接口在 `/api/watchlist`：

```bash
# 列出盯价清单（含词典中文名与最近一次观察价）
curl -H "Authorization: Bearer $TOKEN" $BASE/api/watchlist

# 批量新增/更新（一次最多 100 条；target_cents 可空，只看降价；
# 已在清单里的货号会更新目标价，省略 target_cents 则保持原值）
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"items":[
    {"item_no":"9000001","target_cents":899},
    {"item_no":"9000002"}
  ]}' $BASE/api/watchlist

# 从清单移除（不在清单里返回 404）
curl -X DELETE -H "Authorization: Bearer $TOKEN" $BASE/api/watchlist/9000002
```

## 价格观察 API（v1.9）

盯价检查在应用外做（costco.com / Same-Day / warehouserunner），查到的价格经这个接口写入，观察的 source 记为 `watch`，与小票（receipt）、手动记价（manual）区分。写入后自动跑调价比对（30 天内高价购买 → price_drop 提醒）与盯价检查（低于上次观察 → watch_drop；达到目标价 → watch_target）。与最近一次观察同价同店时跳过不记，避免重复提醒。

```bash
# 批量写入价格观察（一次最多 100 条；observed_on 可空，默认今天；store 可空）
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"observations":[
    {"item_no":"9000001","price_cents":1499,"store":"DANVILLE","observed_on":"2026-11-15"},
    {"item_no":"9000002","price_cents":999}
  ]}' $BASE/api/observations
```

## 本地开发

```bash
go build ./...   # 需要 Go 1.27+
go vet ./...
go test ./...    # 53 个测试：纸质解析器、对账、30 天提醒逻辑、盯价、种子数据、PDF 导出解析与会员号剥离、商品词典与 API、盯价清单 API、价格观察 API、月份列表、PDF 单件折扣、P7 时间陷阱、按日判重、交易号判重、覆盖保存、旧库升级、小票列表、B 类税码
```

OCR 需要本机装 tesseract（`tesseract-ocr` + 英文数据），否则照片上传会报 OCR 失败；PDF 导入需要 `pdftotext`（poppler-utils），缺失时 PDF 上传会明确报错；其余功能不受影响。

## 已知限制

- OCR 不是 100% 准：照片歪斜、反光、字迹模糊时，品名与个别金额可能读错，确认页就是用来纠错的；金额对不上账时保存按钮禁用，服务端保存时还会再校验一次。
- iPhone 传 HEIC 照片时，Go 解码不了，会直接把原图喂给 tesseract 试一次（Alpine 的 tesseract 是否支持 HEIC 取决于 leptonica 编译选项，不保证）。
- PDF 导入只认官网导出的 In-Warehouse 小票 PDF（有文本层）；扫描件 PDF 没有文本层，会提示改传照片。导出格式的折扣行带 `/货号` 时挂给对应商品，没有货号引用时才是整单立减，入库时并入立减合计。
- 目前只有一个共用的家庭账号体系：首个注册为管理员，数据全家共享，不分用户；多用户管理以后再加。
