package main

// 调价与盯价提醒逻辑。
//
// Costco 调价规则（已核实）：购买后 30 天内，Costco 自身降价可退差价；
// 同渠道才行（店里买的只对照店里价）；申请时须有货且在促销期内；
// .97 清仓价不参与。
//
// 价格来源只有两条（没有实时货架价接口，应用不爬、不轮询任何价格源）：
//   (a) 新小票入库：同货号在 30 天内有更高价购买记录 → price_drop 提醒；
//   (b) 手动记价：用户在店里看到价格随手记，同样比对 30 天内购买记录。
// 盯价清单另外看：观察价低于上次观察 → watch_drop；达到目标价 → watch_target。
// 每月券册（Instant Savings PDF）匹配是 Phase 2，这里只留接口与页面提示。

import (
	"strings"
	"time"
)

// checkReceiptAlerts 在新小票入库后跑：对每件商品做调价比对，
// 并为盯价商品记一条价格观察。返回新生成的提醒数。
// seed 导入也走这条路（当时没有更早的购买记录，自然不会产生提醒）。
func (a *App) checkReceiptAlerts(r *Receipt, items []ReceiptItem) int {
	n := 0
	for _, it := range items {
		newUnit := it.UnitPriceCents - it.DiscountCents
		n += a.checkPriceDrop(it.ItemNo, it.Name, newUnit, r.PurchasedAt, r.ID)
		n += a.observePrice(it.ItemNo, newUnit, r.Store, dateOf(r.PurchasedAt), "receipt", r.ID)
	}
	return n
}

// checkManualPrice 处理一次手动记价：同样比对 30 天内购买记录 + 盯价检查。
func (a *App) checkManualPrice(itemNo, name string, priceCents int, store, date string) int {
	n := a.checkPriceDrop(itemNo, name, priceCents, date+" 23:59", "")
	n += a.observePrice(itemNo, priceCents, store, date, "manual", "")
	return n
}

// checkPriceDrop 比对某货号在 [newDate-30d, newDate) 内的购买记录：
// 凡是单价高于 newUnit 的，每张小票各生成一条 price_drop 提醒
// （每张小票的调价要单独去柜台办，所以按小票分开提醒）。
// 截止日 = 那次购买日期 + 30 天；差价 = (旧单价 − 新单价) × 当时件数。
func (a *App) checkPriceDrop(itemNo, name string, newUnit int, newDate, newReceiptID string) int {
	t, err := time.Parse(receiptTimeLayout, newDate)
	if err != nil {
		// 手动记价传的是 "YYYY-MM-DD 23:59"，格式一样；解析失败就放弃比对
		return 0
	}
	from := t.AddDate(0, 0, -adjustmentDays).Format(receiptTimeLayout)
	earlier, err := a.store.EarlierPurchases(itemNo, from, newDate)
	if err != nil {
		return 0
	}
	n := 0
	for _, old := range earlier {
		oldUnit := old.UnitPriceCents - old.DiscountCents
		if oldUnit <= newUnit {
			continue
		}
		// 去重：同一张旧小票 + 同价已有未处理提醒时不重复
		//（新小票是哪张不重要，提醒的内容——“这张旧小票能退差价”——是一样的）
		dup, err := a.store.HasOpenPriceDropAlert(itemNo, old.ReceiptID, newUnit)
		if err == nil && dup {
			continue
		}
		deadline := addDaysToReceiptDate(old.PurchasedAt, adjustmentDays)
		_, err = a.store.CreateAlert(&Alert{
			Kind:          "price_drop",
			ItemNo:        itemNo,
			Name:          old.Name,
			OldPriceCents: oldUnit,
			NewPriceCents: newUnit,
			OldReceiptID:  old.ReceiptID,
			NewReceiptID:  newReceiptID,
			PurchaseDate:  dateOf(old.PurchasedAt),
			Deadline:      deadline,
			DiffCents:     (oldUnit - newUnit) * old.Qty,
			Qty:           old.Qty,
		})
		if err == nil {
			n++
		}
	}
	return n
}

// observePrice 记一条价格观察，并做盯价检查：
//   - 观察价低于上次观察 → watch_drop；
//   - 达到目标价（<= target）→ watch_target。
//
// 非盯价商品不记观察（避免 observations 表被所有商品塞满）。
// 返回新生成的提醒数。
func (a *App) observePrice(itemNo string, priceCents int, store, date, source, receiptID string) int {
	w, err := a.store.GetWatch(itemNo)
	if err != nil {
		return 0 // 不在盯价清单里，不记观察
	}
	prev, _ := a.store.LatestObservation(itemNo)
	if _, err := a.store.AddObservation(&Observation{
		ItemNo:     itemNo,
		PriceCents: priceCents,
		Store:      store,
		ObservedOn: date,
		Source:     source,
		ReceiptID:  receiptID,
	}); err != nil {
		return 0
	}
	n := 0
	name := w.Name
	if name == "" {
		name = itemNo
	}
	if prev != nil && priceCents < prev.PriceCents {
		dup, err := a.store.HasOpenWatchAlert("watch_drop", itemNo, priceCents)
		if !(err == nil && dup) {
			if _, err := a.store.CreateAlert(&Alert{
				Kind:          "watch_drop",
				ItemNo:        itemNo,
				Name:          name,
				OldPriceCents: prev.PriceCents,
				NewPriceCents: priceCents,
				NewReceiptID:  receiptID,
				DiffCents:     prev.PriceCents - priceCents,
				Qty:           1,
			}); err == nil {
				n++
			}
		}
	}
	if w.TargetCents != nil && priceCents <= *w.TargetCents {
		dup, err := a.store.HasOpenWatchAlert("watch_target", itemNo, priceCents)
		if !(err == nil && dup) {
			if _, err := a.store.CreateAlert(&Alert{
				Kind:          "watch_target",
				ItemNo:        itemNo,
				Name:          name,
				OldPriceCents: priceCents,
				NewPriceCents: priceCents,
				NewReceiptID:  receiptID,
				DiffCents:     0,
				Qty:           1,
			}); err == nil {
				n++
			}
		}
	}
	return n
}

// backfillWatchObservation 在用户新加盯价时，用最近一次购买记录回填观察，
// 这样盯价页立刻有“上次价格”可看，下次小票进来才能比出降价。
func (a *App) backfillWatchObservation(itemNo string) {
	latest, err := a.store.LatestPurchase(itemNo)
	if err != nil {
		return // 没买过，等以后小票或手动记价
	}
	if _, err := a.store.LatestObservation(itemNo); err == nil {
		return // 已有观察，不重复回填
	}
	a.store.AddObservation(&Observation{
		ItemNo:     itemNo,
		PriceCents: latest.UnitPriceCents - latest.DiscountCents,
		Store:      latest.Store,
		ObservedOn: dateOf(latest.PurchasedAt),
		Source:     "receipt",
		ReceiptID:  latest.ReceiptID,
	})
}

// dateOf 取 "2006-01-02 15:04" 的日期部分。
func dateOf(purchasedAt string) string {
	if len(purchasedAt) >= 10 {
		return purchasedAt[:10]
	}
	return strings.TrimSpace(purchasedAt)
}
