// 小票列表：胶囊筛选 + 底部选择器（月份 / 分类 / 门店）。
// 仪表盘点图表会带筛选跳过来（?month= / ?category=）。

initTopbar();

const params = new URLSearchParams(location.search);
let page = 1;

// 筛选状态（store 存门店号）
const state = {
  month: params.get('month') || '',
  category: params.get('category') || '',
  store: '',
};
// 选项数据，第一次加载后从接口取
const options = { month: [], category: [], store: [] };

const chipsEl = document.getElementById('chips');
const clearAll = document.getElementById('clearAll');
const sheet = document.getElementById('sheet');
const scrim = document.getElementById('sheetScrim');
const sheetTitle = document.getElementById('sheetTitle');
const sheetOpts = document.getElementById('sheetOpts');
let openFilter = null;

const TITLES = { month: '选择月份', category: '选择分类', store: '选择门店' };
const ALL_LABELS = { month: '全部月份', category: '全部分类', store: '全部门店' };

// '2026-11' → '2026年11月'
function monthLabel(v) {
  return v ? v.slice(0, 4) + '年' + String(Number(v.slice(5))) + '月' : ALL_LABELS.month;
}

function labelFor(key, val) {
  if (!val) return ALL_LABELS[key];
  if (key === 'month') return monthLabel(val);
  if (key === 'store') {
    const o = options.store.find(o => o.v === val);
    return o ? o.label : val;
  }
  return val;
}

function renderChips() {
  chipsEl.querySelectorAll('.fchip').forEach(chip => {
    const key = chip.dataset.filter;
    const val = state[key];
    chip.querySelector('.label').textContent = labelFor(key, val);
    chip.classList.toggle('active', !!val);
    let x = chip.querySelector('.x');
    if (val && !x) {
      x = document.createElement('span');
      x.className = 'x';
      x.textContent = '✕';
      x.addEventListener('click', e => { e.stopPropagation(); setFilter(key, ''); });
      chip.appendChild(x);
    } else if (!val && x) x.remove();
  });
  clearAll.hidden = !(state.month || state.category || state.store);
}

function setFilter(key, val) {
  state[key] = val;
  page = 1;
  syncURL();
  renderChips();
  load();
}

// 筛选进地址栏（replaceState，不污染历史），刷新与分享都保持筛选
function syncURL() {
  const q = new URLSearchParams();
  if (state.month) q.set('month', state.month);
  if (state.category) q.set('category', state.category);
  if (state.store) q.set('store_no', state.store);
  history.replaceState(null, '', q.toString() ? '/receipts?' + q : '/receipts');
}

function openSheet(key) {
  openFilter = key;
  sheetTitle.textContent = TITLES[key];
  const opts = [{ v: '', label: ALL_LABELS[key] }, ...options[key]];
  sheetOpts.innerHTML = opts.map(o =>
    '<button class="opt' + (o.v === state[key] ? ' sel' : '') + '" data-v="' + esc(o.v) + '">' +
    esc(o.label) + '<span class="tick">✓</span></button>').join('');
  sheet.classList.add('open');
  scrim.classList.add('show');
}
function closeSheet() { sheet.classList.remove('open'); scrim.classList.remove('show'); openFilter = null; }

chipsEl.querySelectorAll('.fchip').forEach(chip => {
  chip.addEventListener('click', () => openSheet(chip.dataset.filter));
});
sheetOpts.addEventListener('click', e => {
  const btn = e.target.closest('.opt');
  if (!btn || !openFilter) return;
  const val = btn.dataset.v;
  const key = openFilter;
  closeSheet();
  if (val !== state[key]) setFilter(key, val);
});
scrim.addEventListener('click', closeSheet);
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeSheet(); });
// 下滑选择器关闭
let sheetY = null;
sheet.addEventListener('touchstart', e => { sheetY = e.touches[0].clientY; }, { passive: true });
sheet.addEventListener('touchend', e => {
  if (sheetY !== null && e.changedTouches[0].clientY - sheetY > 70) closeSheet();
  sheetY = null;
}, { passive: true });
clearAll.addEventListener('click', () => {
  state.month = state.category = state.store = '';
  page = 1;
  syncURL();
  renderChips();
  load();
});

async function load() {
  const q = new URLSearchParams();
  if (state.month) q.set('month', state.month);
  if (state.category) q.set('category', state.category);
  if (state.store) q.set('store_no', state.store);
  q.set('page', page);
  q.set('per_page', 20);
  let d;
  try {
    d = await api('/api/v1/receipts?' + q.toString());
  } catch (e) { toast(e.message); return; }

  // 选项只在第一次加载时填充
  if (options.month.length === 0) {
    options.month = (d.months || []).map(m => ({ v: m, label: monthLabel(m) }));
    options.category = (d.categories || []).map(c => ({ v: c, label: c }));
    options.store = (d.stores || []).map(s => ({ v: s.store_no, label: s.store + ' #' + s.store_no }));
    renderChips(); // 门店中文名到了，刷新胶囊文字
  }

  const list = document.getElementById('list');
  if (!d.receipts.length) {
    list.innerHTML = '<div class="empty">没有符合条件的小票。</div>';
  } else {
    // 带分类筛选时把分类带进详情页，详情默认只显示该分类的商品
    const catParam = state.category ? '?category=' + encodeURIComponent(state.category) : '';
    list.innerHTML = d.receipts.map(r =>
      '<div class="item-row" onclick="location.href=\'/receipt/' + esc(r.id) + catParam + '\'">' +
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

renderChips();
load();
