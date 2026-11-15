// 小票列表：按月份 / 分类 / 门店筛选（仪表盘点图表会带筛选跳过来）。

initTopbar();

const params = new URLSearchParams(location.search);
let page = 1;

const fMonth = document.getElementById('fMonth');
const fCategory = document.getElementById('fCategory');
const fStore = document.getElementById('fStore');
if (params.get('month')) fMonth.value = params.get('month');
if (params.get('category')) fCategory.value = params.get('category');

async function load() {
  const q = new URLSearchParams();
  if (fMonth.value) q.set('month', fMonth.value);
  if (fCategory.value) q.set('category', fCategory.value);
  if (fStore.value) q.set('store_no', fStore.value);
  q.set('page', page);
  q.set('per_page', 20);
  let d;
  try {
    d = await api('/api/v1/receipts?' + q.toString());
  } catch (e) { toast(e.message); return; }

  // 筛选下拉只在第一次加载时填充（避免每次刷新丢选中值）
  if (fCategory.options.length === 1) {
    (d.categories || []).forEach(c => {
      const o = document.createElement('option');
      o.value = c; o.textContent = c;
      fCategory.appendChild(o);
    });
    if (params.get('category')) fCategory.value = params.get('category');
  }
  if (fStore.options.length === 1) {
    (d.stores || []).forEach(s => {
      const o = document.createElement('option');
      o.value = s.store_no; o.textContent = s.store + ' #' + s.store_no;
      fStore.appendChild(o);
    });
  }

  const list = document.getElementById('list');
  if (!d.receipts.length) {
    list.innerHTML = '<div class="empty">没有符合条件的小票。</div>';
  } else {
    list.innerHTML = d.receipts.map(r =>
      '<div class="item-row" onclick="location.href=\'/receipt/' + esc(r.id) + '\'">' +
      '<div class="row-head"><span class="row-title">' + esc(r.store) + ' #' + esc(r.store_no) +
      '</span><span class="row-meta money">' + fmtMoney(r.total_cents) + '</span></div>' +
      '<div class="row-sub">' + fmtDate(r.purchased_at) + ' · ' + r.item_count + ' 件' +
      (r.savings_cents > 0 ? ' · 立减 ' + fmtMoney(r.savings_cents) : '') +
      (r.has_photo ? ' · 有照片' : '') + '</div></div>').join('');
  }
  const totalPages = Math.max(1, Math.ceil(d.total / d.per_page));
  document.getElementById('pager').innerHTML =
    '<button class="mini-btn" id="prevBtn" ' + (page <= 1 ? 'disabled' : '') + '>上一页</button>' +
    '<span class="muted">' + page + ' / ' + totalPages + '</span>' +
    '<button class="mini-btn" id="nextBtn" ' + (page >= totalPages ? 'disabled' : '') + '>下一页</button>';
  document.getElementById('stats').textContent = '共 ' + d.total + ' 张小票';
  const prev = document.getElementById('prevBtn');
  const next = document.getElementById('nextBtn');
  if (prev) prev.onclick = () => { page--; load(); window.scrollTo({ top: 0 }); };
  if (next) next.onclick = () => { page++; load(); window.scrollTo({ top: 0 }); };
}

[fMonth, fCategory, fStore].forEach(el => el.addEventListener('change', () => { page = 1; load(); }));
document.getElementById('clearBtn').addEventListener('click', () => {
  fMonth.value = ''; fCategory.value = ''; fStore.value = '';
  page = 1; load();
});

load();
