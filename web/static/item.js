// 商品详情：价格走势线（购买 + 手动记价）、购买历史、盯价、手动记价。
// 复制货号用同步 copyText（iOS 手势内完成，不先 await）。

initTopbar();

const itemNo = decodeURIComponent(location.pathname.split('/').pop());
let data = null;

async function load() {
  try {
    data = await api('/api/v1/items/' + encodeURIComponent(itemNo));
  } catch (e) {
    document.getElementById('content').innerHTML = '<div class="empty">' + esc(e.message) + '</div>';
    return;
  }
  const purchases = data.purchases || [];
  const obs = data.observations || [];
  const watch = data.watch;
  const last = purchases[purchases.length - 1];
  const lastUnit = last.unit_price_cents - last.discount_cents;

  let html = '<h1>' + esc(data.name) + '</h1>' +
    '<p class="muted">货号 ' + esc(itemNo) +
    ' <button class="mini-btn" id="copyNoBtn">复制货号</button> · ' + esc(data.category) +
    ' · 最近实付 <span class="money">' + fmtMoney(lastUnit) + '</span></p>' +
    '<div class="btn-row" style="margin-bottom:16px">' +
    (watch
      ? '<button class="btn" id="watchBtn">已盯价' + (watch.target_cents != null ? '（目标 ' + fmtMoney(watch.target_cents) + '）' : '') + '</button>'
      : '<button class="btn primary" id="watchBtn">＋ 加入盯价</button>') +
    '<button class="btn" id="logPriceBtn">记录现价</button>' +
    '</div>' +
    '<div class="card chart-card"><h2>价格走势</h2>' +
    '<p class="muted">● 购买价 ○ 手动记价（店里看到随手记）</p>' +
    '<div id="timeline"></div></div>' +
    '<div class="card"><h2>购买记录（' + purchases.length + ' 次）</h2><table>' +
    '<tr><th>日期</th><th>门店</th><th class="num">单价</th><th class="num">实付</th></tr>' +
    purchases.slice().reverse().map(p =>
      '<tr onclick="location.href=\'/receipt/' + encodeURIComponent(p.receipt_id) + '\'" style="cursor:pointer">' +
      '<td>' + fmtDate(p.purchased_at) + '</td><td>' + esc(p.store) + '</td>' +
      '<td class="num money">' + fmtMoney(p.unit_price_cents) +
      (p.discount_cents > 0 ? '<br><span class="good">−' + fmtMoney(p.discount_cents) + '</span>' : '') + '</td>' +
      '<td class="num money">' + fmtMoney(p.unit_price_cents - p.discount_cents) +
      (p.qty > 1 ? '<br><span class="muted">×' + p.qty + '</span>' : '') + '</td></tr>').join('') +
    '</table></div>';
  document.getElementById('content').innerHTML = html;

  document.getElementById('copyNoBtn').onclick = () => copyText(itemNo);
  document.getElementById('watchBtn').onclick = watch ? editWatchModal : addWatchModal;
  document.getElementById('logPriceBtn').onclick = logPriceModal;
  drawTimeline(purchases, obs);
}

// ---------- 价格走势线（手写 SVG） ----------
function drawTimeline(purchases, obs) {
  const el = document.getElementById('timeline');
  const pts = [];
  purchases.forEach(p => pts.push({
    date: p.purchased_at.slice(0, 10), price: p.unit_price_cents - p.discount_cents,
    label: fmtDate(p.purchased_at) + ' 购买', manual: false,
  }));
  obs.forEach(o => {
    // 小票观察与购买是同一价格，不重复画点（只画手动记价的点）
    if (o.source === 'manual') pts.push({
      date: o.observed_on, price: o.price_cents,
      label: fmtDate(o.observed_on) + ' 记价' + (o.store ? '（' + o.store + '）' : ''), manual: true,
    });
  });
  pts.sort((a, b) => a.date < b.date ? -1 : 1);
  if (pts.length === 0) { el.innerHTML = '<div class="empty">暂无价格数据</div>'; return; }
  if (pts.length === 1) {
    el.innerHTML = '<p style="text-align:center;font-size:20px;font-weight:700" class="money">' +
      fmtMoney(pts[0].price) + '</p><p class="muted" style="text-align:center">' + esc(pts[0].label) +
      ' · 只有一次记录，走势线需要两次以上</p>';
    return;
  }
  const W = 640, H = 220, padL = 56, padR = 16, padT = 24, padB = 30;
  const prices = pts.map(p => p.price);
  let min = Math.min(...prices), max = Math.max(...prices);
  if (min === max) { min = Math.max(0, min - 100); max = max + 100; }
  const pad = (max - min) * 0.15;
  min = Math.max(0, min - pad); max = max + pad;
  const t0 = new Date(pts[0].date).getTime();
  const t1 = new Date(pts[pts.length - 1].date).getTime();
  const span = Math.max(1, t1 - t0);
  const X = d => padL + (new Date(d).getTime() - t0) / span * (W - padL - padR);
  const Y = p => padT + (1 - (p - min) / (max - min)) * (H - padT - padB);
  let svg = '<svg viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="价格走势">';
  // 网格线 + 纵轴刻度
  for (let i = 0; i <= 3; i++) {
    const v = min + (max - min) * i / 3;
    const y = Y(v);
    svg += '<line x1="' + padL + '" y1="' + y + '" x2="' + (W - padR) + '" y2="' + y + '" stroke="rgba(0,0,0,.06)"></line>' +
      '<text x="' + (padL - 8) + '" y="' + (y + 4) + '" text-anchor="end" font-size="11" fill="#8a8a8e">' + fmtMoney(Math.round(v)) + '</text>';
  }
  const line = pts.map(p => X(p.date).toFixed(1) + ',' + Y(p.price).toFixed(1)).join(' ');
  svg += '<polyline points="' + line + '" fill="none" stroke="#005daa" stroke-width="2"></polyline>';
  pts.forEach(p => {
    svg += '<circle cx="' + X(p.date) + '" cy="' + Y(p.price) + '" r="5" fill="' +
      (p.manual ? '#fff' : '#005daa') + '" stroke="#005daa" stroke-width="2">' +
      '<title>' + esc(p.label) + ' ' + fmtMoney(p.price) + '</title></circle>' +
      '<text x="' + X(p.date) + '" y="' + (Y(p.price) - 10) + '" text-anchor="middle" font-size="11" font-weight="600">' +
      fmtMoney(p.price) + '</text>';
  });
  // 横轴：首尾日期
  svg += '<text x="' + padL + '" y="' + (H - 8) + '" font-size="11" fill="#8a8a8e">' + pts[0].date.slice(5) + '</text>' +
    '<text x="' + (W - padR) + '" y="' + (H - 8) + '" text-anchor="end" font-size="11" fill="#8a8a8e">' +
    pts[pts.length - 1].date.slice(5) + '</text></svg>';
  el.innerHTML = svg;
}

// ---------- 盯价 ----------
function addWatchModal() {
  const m = openModal('<h3>加入盯价</h3><p class="muted">新小票入库或手动记价时，会比对上次价格：降价了提醒你；达到目标价也提醒你。</p>' +
    '<div class="field"><label>目标价（可选，留空只看降价）</label>' +
    '<input type="text" id="mTarget" inputmode="decimal" placeholder="如 12.99"></div>' +
    '<div class="btn-row"><button class="btn primary" id="mOk">加入盯价</button>' +
    '<button class="btn" id="mCancel">取消</button></div>');
  m.querySelector('#mCancel').onclick = closeModal;
  m.querySelector('#mOk').onclick = async () => {
    const v = m.querySelector('#mTarget').value.trim();
    const body = { item_no: itemNo };
    if (v !== '') {
      const cents = Math.round(parseFloat(v) * 100);
      if (isNaN(cents) || cents <= 0) { toast('目标价格式不对'); return; }
      body.target_cents = cents;
    }
    try {
      await api('/api/v1/watchlist', { method: 'POST', body: JSON.stringify(body) });
      toast('已加入盯价');
      closeModal();
      load();
    } catch (e) { toast(e.message); }
  };
}

function editWatchModal() {
  const w = data.watch;
  const m = openModal('<h3>盯价设置</h3>' +
    '<div class="field"><label>目标价（留空只看降价）</label>' +
    '<input type="text" id="mTarget" inputmode="decimal" value="' +
    (w.target_cents != null ? (w.target_cents / 100).toFixed(2) : '') + '" placeholder="如 12.99"></div>' +
    '<div class="btn-row"><button class="btn primary" id="mOk">保存</button>' +
    '<button class="btn danger" id="mDel">取消盯价</button>' +
    '<button class="btn" id="mCancel">关闭</button></div>');
  m.querySelector('#mCancel').onclick = closeModal;
  m.querySelector('#mOk').onclick = async () => {
    const v = m.querySelector('#mTarget').value.trim();
    const body = {};
    if (v === '') {
      body.target_cents = null;
    } else {
      const cents = Math.round(parseFloat(v) * 100);
      if (isNaN(cents) || cents <= 0) { toast('目标价格式不对'); return; }
      body.target_cents = cents;
    }
    try {
      await api('/api/v1/watchlist/' + encodeURIComponent(itemNo), { method: 'PUT', body: JSON.stringify(body) });
      toast('已保存');
      closeModal();
      load();
    } catch (e) { toast(e.message); }
  };
  m.querySelector('#mDel').onclick = async () => {
    try {
      await api('/api/v1/watchlist/' + encodeURIComponent(itemNo), { method: 'DELETE' });
      toast('已取消盯价');
      closeModal();
      load();
    } catch (e) { toast(e.message); }
  };
}

// ---------- 手动记价 ----------
function logPriceModal() {
  const today = new Date();
  const pad = n => String(n).padStart(2, '0');
  const todayStr = today.getFullYear() + '-' + pad(today.getMonth() + 1) + '-' + pad(today.getDate());
  const m = openModal('<h3>记录现价</h3><p class="muted">在店里看到的价格随手记下来：会跟 30 天内的购买比对（可能触发调价提醒），也会更新盯价。</p>' +
    '<div class="field"><label>现价 $</label><input type="text" id="mPrice" inputmode="decimal" placeholder="如 14.99"></div>' +
    '<div class="field"><label>门店（可选）</label><input type="text" id="mStore" placeholder="如 Fremont"></div>' +
    '<div class="field"><label>日期</label><input type="date" id="mDate" value="' + todayStr + '"></div>' +
    '<div class="btn-row"><button class="btn primary" id="mOk">记录</button>' +
    '<button class="btn" id="mCancel">取消</button></div>');
  m.querySelector('#mCancel').onclick = closeModal;
  m.querySelector('#mOk').onclick = async () => {
    const cents = Math.round(parseFloat(m.querySelector('#mPrice').value.trim()) * 100);
    if (isNaN(cents) || cents <= 0) { toast('价格格式不对'); return; }
    try {
      const d = await api('/api/v1/observations', {
        method: 'POST',
        body: JSON.stringify({
          item_no: itemNo, price_cents: cents,
          store: m.querySelector('#mStore').value.trim(),
          observed_on: m.querySelector('#mDate').value,
        }),
      });
      toast(d.alerts_created > 0 ? '已记录，并生成 ' + d.alerts_created + ' 条提醒' : '已记录');
      closeModal();
      load();
    } catch (e) { toast(e.message); }
  };
}

load();
