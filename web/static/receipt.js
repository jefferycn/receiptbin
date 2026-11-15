// 小票详情：合计、商品行、照片、删除。

initTopbar();

const id = location.pathname.split('/').pop();

async function load() {
  let d;
  try {
    d = await api('/api/v1/receipts/' + encodeURIComponent(id));
  } catch (e) {
    document.getElementById('content').innerHTML = '<div class="empty">' + esc(e.message) + '</div>';
    return;
  }
  const r = d.receipt;
  const items = d.items || [];
  let html = '<h1>' + esc(r.store) + ' #' + esc(r.store_no) + '</h1>' +
    '<p class="muted">' + fmtDate(r.purchased_at) + ' · ' + r.item_count + ' 件</p>' +
    '<div class="card"><table>' +
    '<tr><td>小计</td><td class="num money">' + fmtMoney(r.subtotal_cents) + '</td></tr>' +
    '<tr><td>税</td><td class="num money">' + fmtMoney(r.tax_cents) + '</td></tr>' +
    (r.savings_cents > 0 ? '<tr><td>立减</td><td class="num money good">−' + fmtMoney(r.savings_cents) + '</td></tr>' : '') +
    '<tr class="total-row"><td>总计</td><td class="num money">' + fmtMoney(r.total_cents) + '</td></tr>' +
    '</table></div>' +
    '<div class="card"><h2>商品</h2><table>' +
    '<tr><th>品名</th><th class="num">数量</th><th class="num">单价</th><th class="num">实付</th></tr>' +
    items.map(it =>
      '<tr onclick="location.href=\'/item/' + encodeURIComponent(it.item_no) + '\'" style="cursor:pointer">' +
      '<td>' + esc(it.name) + '<br><span class="muted">' + esc(it.item_no) + ' · ' + esc(it.category) + '</span></td>' +
      '<td class="num">' + it.qty + '</td>' +
      '<td class="num money">' + fmtMoney(it.unit_price_cents) +
      (it.discount_cents > 0 ? '<br><span class="good">−' + fmtMoney(it.discount_cents) + '</span>' : '') + '</td>' +
      '<td class="num money">' + fmtMoney(it.net_cents) + '</td></tr>').join('') +
    '</table></div>';
  if (r.has_photo) {
    html += '<div class="card"><h2>小票照片</h2>' +
      '<img class="receipt-photo" id="photoImg" src="/api/v1/receipts/' + encodeURIComponent(id) + '/photo" alt="小票照片"></div>';
  }
  html += '<p style="text-align:center;margin-top:24px"><button class="btn danger" id="delBtn">删除这张小票</button></p>';
  document.getElementById('content').innerHTML = html;
  const img = document.getElementById('photoImg');
  if (img) img.onclick = () => {
    const m = openModal('<img src="/api/v1/receipts/' + encodeURIComponent(id) + '/photo" style="max-width:100%;border-radius:12px">' +
      '<p style="margin-top:12px"><button class="btn" id="mClose">关闭</button></p>');
    m.querySelector('#mClose').onclick = closeModal;
  };
  document.getElementById('delBtn').onclick = () => {
    confirmModal('删除小票', '确定删除这张小票吗？商品记录与相关提醒会一起删除，无法恢复。', '删除', async () => {
      await api('/api/v1/receipts/' + encodeURIComponent(id), { method: 'DELETE' });
      toast('已删除');
      location.href = '/receipts';
    });
  };
}

load();
