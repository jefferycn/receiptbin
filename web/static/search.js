// 商品搜索：输入后 300ms 防抖实时搜，点结果进商品详情。

initTopbar();

const q = document.getElementById('q');
const results = document.getElementById('results');
let timer = null;
let seq = 0;

q.addEventListener('input', () => {
  clearTimeout(timer);
  const query = q.value.trim();
  if (!query) {
    results.innerHTML = '<p class="muted">输入货号或品名，实时搜索买过的商品。</p>';
    return;
  }
  timer = setTimeout(() => search(query), 300);
});

async function search(query) {
  const mySeq = ++seq;
  let d;
  try {
    d = await api('/api/v1/search?q=' + encodeURIComponent(query));
  } catch (e) { toast(e.message); return; }
  if (mySeq !== seq) return; // 更新的搜索已发出，丢弃旧结果
  const items = d.items || [];
  if (!items.length) {
    results.innerHTML = '<div class="empty">没找到“' + esc(query) + '”。<br>只搜买过的商品；没买过的请先扫小票入库。</div>';
    return;
  }
  results.innerHTML = items.map(it =>
    '<div class="item-row" onclick="location.href=\'/item/' + encodeURIComponent(it.item_no) + '\'">' +
    '<div class="row-head"><span class="row-title">' + esc(it.name) + '</span>' +
    '<span class="row-meta money">' + fmtMoney(it.last_price_cents) + '</span></div>' +
    '<div class="row-sub">' + esc(it.item_no) + ' · ' + esc(it.category) +
    ' · 买过 ' + it.purchase_count + ' 次 · 最近 ' + fmtDate(it.last_date) +
    '（' + esc(it.last_store) + '）</div></div>').join('');
}

q.focus();
