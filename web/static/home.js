// 仪表盘：统计卡片 + 月花费柱状图 + 分类环形图（手写 SVG，无外部依赖）。
// 点柱子 → 那月的小票列表；点扇形/图例 → 该分类的小票列表。

initTopbar();

let period = 'year'; // month / year / all

async function load() {
  const y = new Date().getFullYear();
  const m = String(new Date().getMonth() + 1).padStart(2, '0');
  const periodParam = period === 'month' ? `${y}-${m}` : period === 'year' ? String(y) : 'all';
  let d;
  try {
    d = await api('/api/v1/stats?period=' + encodeURIComponent(periodParam));
  } catch (e) { toast(e.message); return; }

  document.getElementById('statGrid').innerHTML =
    statCard(d.month_cents, '本月') + statCard(d.year_cents, '今年') + statCard(d.total_cents, '累计');

  const banner = document.getElementById('alertBanner');
  if (d.open_alerts > 0) {
    banner.innerHTML = '<div class="card clickable alert-card" onclick="location.href=\'/alerts\'">' +
      '<strong>' + d.open_alerts + ' 条未处理提醒</strong>' +
      '<p class="muted" style="margin:4px 0 0">有商品在 30 天内降价了，可能可以去柜台退差价。点按查看。</p></div>';
  } else {
    banner.innerHTML = '';
  }

  drawMonthChart(d.by_month || []);
  drawCategoryChart(d.by_category || []);

  const recent = d.recent_receipts || [];
  document.getElementById('recentList').innerHTML = recent.length === 0
    ? '<div class="empty">还没有小票，点右下角扫一张吧。</div>'
    : recent.map(r => receiptRow(r)).join('') +
      '<p class="stats"><a href="/receipts">查看全部小票 →</a></p>';
}

function statCard(cents, label) {
  return '<div class="stat-card"><div class="stat-num money">' + fmtMoney(cents) +
    '</div><div class="stat-label">' + esc(label) + '</div></div>';
}

function receiptRow(r) {
  return '<div class="item-row" onclick="location.href=\'/receipt/' + esc(r.id) + '\'">' +
    '<div class="row-head"><span class="row-title">' + esc(r.store) + ' #' + esc(r.store_no) +
    '</span><span class="row-meta money">' + fmtMoney(r.total_cents) + '</span></div>' +
    '<div class="row-sub">' + fmtDate(r.purchased_at) + ' · ' + r.item_count + ' 件' +
    (r.savings_cents > 0 ? ' · 立减 ' + fmtMoney(r.savings_cents) : '') + '</div></div>';
}

// ---------- 月花费柱状图 ----------
function drawMonthChart(months) {
  const el = document.getElementById('monthChart');
  if (months.length === 0) { el.innerHTML = '<div class="empty">暂无数据</div>'; return; }
  const W = 640, H = 200, padB = 28, padT = 24;
  const max = Math.max(...months.map(x => x.total_cents), 1);
  const slot = W / months.length;
  const bw = Math.min(44, slot * 0.55);
  let svg = '<svg viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="每月花费">';
  months.forEach((x, i) => {
    const h = Math.max(2, (x.total_cents / max) * (H - padB - padT));
    const cx = slot * i + slot / 2;
    const y = H - padB - h;
    svg += '<g class="chart-bar" data-month="' + esc(x.month) + '">' +
      '<rect x="' + (cx - bw / 2) + '" y="' + y + '" width="' + bw + '" height="' + h + '" rx="4"></rect>' +
      '<text class="bar-val" x="' + cx + '" y="' + (y - 6) + '" text-anchor="middle">' + fmtMoney(x.total_cents) + '</text>' +
      '<text x="' + cx + '" y="' + (H - 8) + '" text-anchor="middle">' + x.month.slice(5) + '月</text>' +
      '</g>';
  });
  svg += '</svg>';
  el.innerHTML = svg;
  el.querySelectorAll('.chart-bar').forEach(g => {
    g.addEventListener('click', () => {
      location.href = '/receipts?month=' + encodeURIComponent(g.dataset.month);
    });
  });
}

// ---------- 分类环形图 ----------
function drawCategoryChart(cats) {
  const el = document.getElementById('catChart');
  const legend = document.getElementById('catLegend');
  if (cats.length === 0) {
    el.innerHTML = '<div class="empty">这个时段没有数据</div>';
    legend.innerHTML = '';
    return;
  }
  const total = cats.reduce((s, c) => s + c.total_cents, 0);
  const R = 70, C = 2 * Math.PI * R;
  let offset = 0;
  let svg = '<svg viewBox="0 0 200 200" style="max-width:240px;margin:0 auto" role="img" aria-label="分类花费">';
  svg += '<circle cx="100" cy="100" r="' + R + '" fill="none" stroke="rgba(0,0,0,.06)" stroke-width="30"></circle>';
  cats.forEach(c => {
    const frac = c.total_cents / total;
    const len = Math.max(0, frac * C - 2); // 扇形之间留 2px 缝
    svg += '<circle class="donut-seg" data-category="' + esc(c.category) + '" cx="100" cy="100" r="' + R +
      '" fill="none" stroke="' + categoryColor(c.category) + '" stroke-width="30"' +
      ' stroke-dasharray="' + len + ' ' + (C - len) + '" stroke-dashoffset="' + (-offset) +
      '" transform="rotate(-90 100 100)"><title>' + esc(c.category) + ' ' + fmtMoney(c.total_cents) + '</title></circle>';
    offset += frac * C;
  });
  svg += '<text x="100" y="96" text-anchor="middle" font-size="16" font-weight="700">' + fmtMoney(total) + '</text>' +
    '<text x="100" y="116" text-anchor="middle" font-size="11" fill="#8a8a8e">商品实付</text></svg>';
  el.innerHTML = svg;
  legend.innerHTML = cats.map(c =>
    '<button class="legend-item" data-category="' + esc(c.category) + '">' +
    '<span class="legend-dot" style="background:' + categoryColor(c.category) + '"></span>' +
    esc(c.category) + ' <span class="money">' + fmtMoney(c.total_cents) + '</span></button>').join('');
  const go = cat => location.href = '/receipts?category=' + encodeURIComponent(cat);
  el.querySelectorAll('.donut-seg').forEach(s => s.addEventListener('click', () => go(s.dataset.category)));
  legend.querySelectorAll('.legend-item').forEach(b => b.addEventListener('click', () => go(b.dataset.category)));
}

document.getElementById('periodSeg').addEventListener('click', e => {
  const btn = e.target.closest('.seg-btn');
  if (!btn) return;
  period = btn.dataset.period;
  document.querySelectorAll('#periodSeg .seg-btn').forEach(b => b.classList.toggle('selected', b === btn));
  load();
});

load();
