// 盯价清单：加盯 / 取消盯 / 改目标价 / 记录现价。

initTopbar();

async function load() {
  let d;
  try {
    d = await api('/api/v1/watchlist');
  } catch (e) { toast(e.message); return; }
  const list = document.getElementById('list');
  const watches = d.watchlist || [];
  if (!watches.length) {
    list.innerHTML = '<div class="empty">盯价清单是空的。在上面输入货号开始盯，<br>也可以在商品详情页点“加入盯价”。</div>';
    return;
  }
  list.innerHTML = watches.map(w => {
    const obs = w.last_obs;
    const obsLine = obs
      ? '上次 <span class="money">' + fmtMoney(obs.price_cents) + '</span> · ' + fmtDate(obs.observed_on) +
        (obs.store ? ' · ' + esc(obs.store) : '') + (obs.source === 'manual' ? ' · 手动记价' : ' · 小票')
      : '还没有价格记录：扫到它的小票、或手动记一次价后，这里会显示上次价格。';
    const targetLine = w.target_cents != null
      ? '目标价 <span class="money">' + fmtMoney(w.target_cents) + '</span>' +
        (obs && obs.price_cents <= w.target_cents ? ' · <span class="good">已到价！</span>' : '')
      : '未设目标价，只看降价';
    return '<div class="card">' +
      '<div class="row-head" style="display:flex;gap:8px;align-items:center">' +
      '<strong style="flex:1;min-width:0">' + esc(w.name || w.item_no) + '</strong>' +
      '<span class="muted">' + esc(w.item_no) + '</span></div>' +
      '<p style="margin:8px 0 0">' + obsLine + '</p>' +
      '<p class="muted" style="margin:4px 0 0">' + targetLine + '</p>' +
      '<div class="btn-row" style="margin-top:12px">' +
      '<button class="mini-btn" data-act="detail" data-no="' + esc(w.item_no) + '">商品详情</button>' +
      '<button class="mini-btn" data-act="price" data-no="' + esc(w.item_no) + '">记录现价</button>' +
      '<button class="mini-btn" data-act="target" data-no="' + esc(w.item_no) + '" data-target="' + (w.target_cents != null ? (w.target_cents / 100).toFixed(2) : '') + '">改目标价</button>' +
      '<button class="mini-btn danger" data-act="remove" data-no="' + esc(w.item_no) + '">取消盯价</button>' +
      '</div></div>';
  }).join('');
  list.querySelectorAll('[data-act]').forEach(b => b.addEventListener('click', () => {
    const no = b.dataset.no;
    const act = b.dataset.act;
    if (act === 'detail') location.href = '/item/' + encodeURIComponent(no);
    if (act === 'price') logPriceModal(no);
    if (act === 'target') editTargetModal(no, b.dataset.target);
    if (act === 'remove') {
      confirmModal('取消盯价', '确定不再盯货号 ' + no + ' 吗？历史价格观察会保留。', '取消盯价', async () => {
        await api('/api/v1/watchlist/' + encodeURIComponent(no), { method: 'DELETE' });
        toast('已取消盯价');
        load();
      });
    }
  }));
}

document.getElementById('addBtn').addEventListener('click', async () => {
  const itemNo = document.getElementById('wItemNo').value.trim();
  const targetStr = document.getElementById('wTarget').value.trim();
  if (!itemNo) { toast('请填写货号'); return; }
  const body = { item_no: itemNo };
  if (targetStr !== '') {
    const cents = Math.round(parseFloat(targetStr) * 100);
    if (isNaN(cents) || cents <= 0) { toast('目标价格式不对'); return; }
    body.target_cents = cents;
  }
  try {
    await api('/api/v1/watchlist', { method: 'POST', body: JSON.stringify(body) });
    toast('已加入盯价');
    document.getElementById('wItemNo').value = '';
    document.getElementById('wTarget').value = '';
    load();
  } catch (e) { toast(e.message); }
});

function editTargetModal(itemNo, current) {
  const m = openModal('<h3>改目标价</h3><p class="muted">货号 ' + esc(itemNo) + '。留空表示只看降价，不设目标。</p>' +
    '<div class="field"><label>目标价 $</label>' +
    '<input type="text" id="mTarget" inputmode="decimal" value="' + esc(current || '') + '" placeholder="如 12.99"></div>' +
    '<div class="btn-row"><button class="btn primary" id="mOk">保存</button>' +
    '<button class="btn" id="mCancel">取消</button></div>');
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
}

function logPriceModal(itemNo) {
  const today = new Date();
  const pad = n => String(n).padStart(2, '0');
  const todayStr = today.getFullYear() + '-' + pad(today.getMonth() + 1) + '-' + pad(today.getDate());
  const m = openModal('<h3>记录现价</h3><p class="muted">货号 ' + esc(itemNo) + '。在店里看到的价格随手记下来，会跟 30 天内的购买比对。</p>' +
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
