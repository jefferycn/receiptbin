// 退货清单：待退货可调数量、逐件标记已退、全部已处理；已处理可恢复。

initTopbar();

let status = 'pending';

async function load() {
  let d;
  try {
    d = await api('/api/v1/returns?status=' + status);
  } catch (e) { toast(e.message); return; }
  const list = d.returns || [];
  const summary = document.getElementById('summary');
  const box = document.getElementById('list');
  if (status === 'pending' && list.length) {
    const totalQty = list.reduce((s, x) => s + x.qty, 0);
    const totalRefund = list.reduce((s, x) => s + x.refund_cents, 0);
    summary.innerHTML = '<div class="card"><p style="margin:0">共 <strong>' + list.length +
      ' 种 · ' + totalQty + ' 件</strong>，预计可退 <strong class="money">' + fmtMoney(totalRefund) +
      '</strong>（不含税）</p>' +
      '<div class="btn-row" style="margin-top:12px">' +
      '<button class="btn primary" id="doneAll">全部已处理</button></div></div>';
    document.getElementById('doneAll').onclick = () => {
      confirmModal('全部已处理', '把清单里 ' + list.length + ' 种商品都标记为已退掉吗？', '全部已处理', async () => {
        await api('/api/v1/returns/done-all', { method: 'POST' });
        toast('已全部处理');
        load();
      });
    };
  } else {
    summary.innerHTML = '';
  }
  if (!list.length) {
    box.innerHTML = '<div class="empty">' +
      (status === 'pending' ? '退货清单是空的。在小票详情里点「加入退货」添加。' : '没有已处理的退货。') + '</div>';
    return;
  }
  box.innerHTML = list.map(returnCard).join('');
  box.querySelectorAll('[data-act]').forEach(b => b.addEventListener('click', async () => {
    const id = b.dataset.id;
    const act = b.dataset.act;
    try {
      if (act === 'done' || act === 'pending') {
        await api('/api/v1/returns/' + id + '/status', {
          method: 'POST', body: JSON.stringify({ status: act }),
        });
        toast(act === 'done' ? '已退掉' : '已恢复');
      } else if (act === 'remove') {
        await api('/api/v1/returns/' + id, { method: 'DELETE' });
        toast('已移出清单');
      } else if (act === 'minus' || act === 'plus') {
        const cur = list.find(x => x.id === Number(id));
        const qty = cur.qty + (act === 'plus' ? 1 : -1);
        await api('/api/v1/returns/' + id + '/qty', {
          method: 'POST', body: JSON.stringify({ qty }),
        });
      }
      load();
    } catch (e) { toast(e.message); }
  }));
}

function returnCard(x) {
  const netUnit = x.unit_price_cents - x.discount_cents;
  let qtyHtml;
  if (status === 'pending' && x.bought_qty > 1) {
    qtyHtml = '<span class="qty-stepper">' +
      '<button data-act="minus" data-id="' + x.id + '"' + (x.qty <= 1 ? ' disabled' : '') + '>−</button>' +
      '<span>' + x.qty + '</span>' +
      '<button data-act="plus" data-id="' + x.id + '"' + (x.qty >= x.bought_qty ? ' disabled' : '') + '>+</button></span>' +
      ' <span class="muted">/ 买了 ' + x.bought_qty + '</span>';
  } else {
    qtyHtml = '×' + x.qty;
  }
  const actions = status === 'pending'
    ? '<div class="btn-row" style="margin-top:12px">' +
      '<button class="mini-btn primary" data-act="done" data-id="' + x.id + '">已退</button>' +
      '<button class="mini-btn" data-act="remove" data-id="' + x.id + '">不退了</button></div>'
    : '<div class="btn-row" style="margin-top:12px">' +
      '<button class="mini-btn" data-act="pending" data-id="' + x.id + '">恢复</button></div>';
  return '<div class="card">' +
    '<strong>' + esc(displayName(x)) + '</strong>' +
    '<p class="muted" style="margin:2px 0 0">货号 ' + esc(x.item_no) + ' · ' +
    esc(x.store) + ' #' + esc(x.store_no) + ' · ' + fmtDate(x.purchased_at) + ' 购买</p>' +
    '<p style="margin:8px 0 0">实付单价 <span class="money">' + fmtMoney(netUnit) + '</span> ' + qtyHtml +
    '，可退 <strong class="money">' + fmtMoney(x.refund_cents) + '</strong></p>' +
    '<p class="muted" style="margin:8px 0 0"><a href="/receipt/' + encodeURIComponent(x.receipt_id) + '">购买小票</a> · ' +
    '<a href="/item/' + encodeURIComponent(x.item_no) + '">商品详情</a></p>' +
    actions + '</div>';
}

document.getElementById('statusSeg').addEventListener('click', e => {
  const btn = e.target.closest('.seg-btn');
  if (!btn) return;
  status = btn.dataset.status;
  document.querySelectorAll('#statusSeg .seg-btn').forEach(b => b.classList.toggle('selected', b === btn));
  load();
});

load();
