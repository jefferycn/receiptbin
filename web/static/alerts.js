// 提醒列表：按截止日排序（最近的在前），可标记已处理 / 忽略。

initTopbar();

let status = 'open';

async function load() {
  let d;
  try {
    d = await api('/api/v1/alerts?status=' + status);
  } catch (e) { toast(e.message); return; }
  const list = document.getElementById('list');
  const alerts = d.alerts || [];
  if (!alerts.length) {
    list.innerHTML = '<div class="empty">' +
      (status === 'open' ? '没有未处理提醒。' : status === 'handled' ? '没有已处理提醒。' : '没有已忽略提醒。') + '</div>';
    return;
  }
  list.innerHTML = alerts.map(a => alertCard(a)).join('');
  list.querySelectorAll('[data-act]').forEach(b => b.addEventListener('click', async () => {
    const id = b.dataset.id;
    const act = b.dataset.act;
    try {
      await api('/api/v1/alerts/' + id + '/status', {
        method: 'POST', body: JSON.stringify({ status: act }),
      });
      toast(act === 'handled' ? '已标记处理' : '已忽略');
      load();
    } catch (e) { toast(e.message); }
  }));
}

function alertCard(a) {
  const isWatch = a.kind !== 'price_drop';
  let title, desc;
  if (a.kind === 'price_drop') {
    title = '调价：' + a.name;
    desc = '购买价 <span class="money">' + fmtMoney(a.old_price_cents) + '</span> → 现价 ' +
      '<span class="money">' + fmtMoney(a.new_price_cents) + '</span>，可退差价 ' +
      '<strong class="money">' + fmtMoney(a.diff_cents) + '</strong>' +
      (a.qty > 1 ? '（' + a.qty + ' 件）' : '') +
      '。购买于 ' + fmtDate(a.purchase_date) + '。';
  } else if (a.kind === 'watch_target') {
    title = '盯价到价：' + a.name;
    desc = '观察到 <span class="money">' + fmtMoney(a.new_price_cents) + '</span>，达到你的目标价。';
  } else {
    title = '盯价降价：' + a.name;
    desc = '<span class="money">' + fmtMoney(a.old_price_cents) + '</span> → ' +
      '<span class="money">' + fmtMoney(a.new_price_cents) + '</span>，降了 ' +
      fmtMoney(a.old_price_cents - a.new_price_cents) + '。';
  }
  let deadline = '';
  if (a.deadline) {
    const soon = a.days_left <= 7;
    deadline = '<p style="margin:6px 0 0">截止 <span class="deadline' + (soon ? ' soon' : '') + '">' +
      fmtDate(a.deadline) + '（' + (a.days_left < 0 ? '已过期 ' + (-a.days_left) + ' 天' :
        a.days_left === 0 ? '今天到期' : '还剩 ' + a.days_left + ' 天') + '）</span></p>';
  }
  const links = [];
  if (a.old_receipt_id) links.push('<a href="/receipt/' + encodeURIComponent(a.old_receipt_id) + '">购买小票</a>');
  if (a.new_receipt_id) links.push('<a href="/receipt/' + encodeURIComponent(a.new_receipt_id) + '">降价小票</a>');
  links.push('<a href="/item/' + encodeURIComponent(a.item_no) + '">商品详情</a>');
  return '<div class="card alert-card' + (isWatch ? ' watch' : '') + '">' +
    '<strong>' + esc(title) + '</strong>' +
    '<p class="muted" style="margin:2px 0 0">货号 ' + esc(a.item_no) + '</p>' +
    '<p style="margin:8px 0 0">' + desc + '</p>' + deadline +
    '<p class="muted" style="margin:8px 0 0">' + links.join(' · ') + '</p>' +
    (status === 'open'
      ? '<div class="btn-row" style="margin-top:12px">' +
        '<button class="mini-btn primary" data-act="handled" data-id="' + a.id + '">已处理</button>' +
        '<button class="mini-btn" data-act="dismissed" data-id="' + a.id + '">忽略</button></div>'
      : '') +
    '</div>';
}

document.getElementById('statusSeg').addEventListener('click', e => {
  const btn = e.target.closest('.seg-btn');
  if (!btn) return;
  status = btn.dataset.status;
  document.querySelectorAll('#statusSeg .seg-btn').forEach(b => b.classList.toggle('selected', b === btn));
  load();
});

load();
