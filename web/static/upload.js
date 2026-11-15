// 扫小票：拍照上传 → OCR → 确认页人工核对 → 保存。
// 确认页实时对账：原价 − 折扣 = 小计，小计 + 税 = 总计，
// 对不上（误差 > 1 分）时保存按钮禁用；服务端保存时还会再校验一次。

initTopbar();

const CATEGORIES = ['生鲜', '肉类海鲜', '乳制品/饮品', '烘焙/干货', '日用品', '厨房用品', '宠物', '运动', '家居', '其他'];

let uploadId = '';
let rawText = '';
let items = []; // {item_no, name, qty, unit_price_cents, discount_cents, taxable, food_exempt, category}
let categories = {};

const $ = id => document.getElementById(id);

// ---------- 金额：界面上是美元字符串，内部是整数分 ----------
function dollarsToCents(s) {
  if (s == null) return 0;
  s = String(s).trim().replace(/^\$/, '').replace(/,/g, '');
  if (s === '') return 0;
  const v = Math.round(parseFloat(s) * 100);
  return isNaN(v) ? NaN : v;
}
function centsToDollars(c) {
  if (c == null) return '';
  return (c / 100).toFixed(2);
}

// ---------- 上传与 OCR ----------
const dropzone = $('dropzone');
const photoInput = $('photoInput');
dropzone.addEventListener('click', e => {
  if (e.target.tagName !== 'INPUT') photoInput.click();
});
dropzone.addEventListener('dragover', e => { e.preventDefault(); dropzone.classList.add('over'); });
dropzone.addEventListener('dragleave', () => dropzone.classList.remove('over'));
dropzone.addEventListener('drop', e => {
  e.preventDefault();
  dropzone.classList.remove('over');
  if (e.dataTransfer.files.length) handleFile(e.dataTransfer.files[0]);
});
photoInput.addEventListener('change', () => {
  if (photoInput.files.length) handleFile(photoInput.files[0]);
});

async function handleFile(file) {
  if (!file.type.startsWith('image/')) { toast('请上传图片'); return; }
  const preview = $('previewImg');
  preview.src = URL.createObjectURL(file);
  preview.hidden = false;
  $('ocrStatus').innerHTML = '<span class="spinner"></span> 正在识别小票，请稍候…';
  const fd = new FormData();
  fd.append('photo', file);
  let d;
  try {
    d = await apiUpload('/api/v1/upload', fd);
  } catch (e) {
    $('ocrStatus').textContent = '';
    toast(e.message);
    return;
  }
  $('ocrStatus').textContent = '';
  uploadId = d.upload_id;
  rawText = d.raw_text || '';
  showConfirm(d.receipt, d.problems || []);
}

// 手动录入：OCR 完全失败时的退路，从空表开始填。
$('manualBtn').addEventListener('click', () => {
  uploadId = '';
  rawText = '';
  const now = new Date();
  const pad = n => String(n).padStart(2, '0');
  showConfirm({
    store: '', store_no: '',
    purchased_at: now.getFullYear() + '-' + pad(now.getMonth() + 1) + '-' + pad(now.getDate()) + ' ' + pad(now.getHours()) + ':' + pad(now.getMinutes()),
    subtotal_cents: 0, tax_cents: 0, total_cents: 0, savings_cents: 0,
    items: [emptyItem()], warnings: ['手动录入模式：请逐项填写，金额对上账才能保存。'],
  }, []);
});

function emptyItem() {
  return { item_no: '', name: '', qty: 1, unit_price_cents: 0, discount_cents: 0, taxable: false, food_exempt: false, category: '其他' };
}

// ---------- 确认页 ----------
function showConfirm(receipt, problems) {
  items = (receipt.items || []).map(it => ({ ...it }));
  $('stepUpload').hidden = true;
  $('stepConfirm').hidden = false;
  $('fStore').value = receipt.store || '';
  $('fStoreNo').value = receipt.store_no || '';
  $('fDate').value = receipt.purchased_at || '';
  $('fSubtotal').value = centsToDollars(receipt.subtotal_cents);
  $('fTax').value = centsToDollars(receipt.tax_cents);
  $('fTotal').value = centsToDollars(receipt.total_cents);
  const thumb = $('confirmThumb');
  if (uploadId) {
    thumb.src = '/api/v1/pending/' + uploadId;
    thumb.hidden = false;
    thumb.onclick = () => {
      const m = openModal('<img src="/api/v1/pending/' + uploadId + '" style="max-width:100%;border-radius:12px">' +
        '<p style="margin-top:12px"><button class="btn" id="mClose">关闭</button></p>');
      m.querySelector('#mClose').onclick = closeModal;
    };
  } else {
    thumb.hidden = true;
  }
  const warns = [...(receipt.warnings || []), ...problems];
  $('warnBox').innerHTML = warns.length
    ? '<div class="warn"><strong>请注意</strong><ul>' + warns.map(w => '<li>' + esc(w) + '</li>').join('') + '</ul></div>'
    : '';
  renderItems();
  reconcile();
  window.scrollTo({ top: 0 });
}

function categorySelect(selected) {
  return '<select data-field="category">' +
    CATEGORIES.map(c => '<option value="' + c + '"' + (c === selected ? ' selected' : '') + '>' + c + '</option>').join('') +
    '</select>';
}

function renderItems() {
  $('itemCountLabel').textContent = items.length;
  $('itemList').innerHTML = items.map((it, i) =>
    '<div class="edit-row" data-index="' + i + '">' +
    '<div class="edit-head">' +
    '<input type="text" data-field="item_no" value="' + esc(it.item_no) + '" placeholder="货号" inputmode="numeric" style="max-width:110px">' +
    '<input type="text" data-field="name" value="' + esc(it.name) + '" placeholder="品名">' +
    '<button class="edit-del" data-del="' + i + '" title="删除这行">×</button>' +
    '</div>' +
    '<div class="edit-grid">' +
    '<div><label>数量</label><input type="number" data-field="qty" value="' + it.qty + '" min="1" step="1"></div>' +
    '<div><label>单价 $</label><input type="text" data-field="unit" value="' + centsToDollars(it.unit_price_cents) + '" inputmode="decimal"></div>' +
    '<div><label>折扣 $</label><input type="text" data-field="discount" value="' + centsToDollars(it.discount_cents) + '" inputmode="decimal"></div>' +
    '<div><label>分类</label>' + categorySelect(it.category || '其他') + '</div>' +
    '</div>' +
    '<div class="muted" style="margin-top:6px">实付 <span class="money" data-net>$' +
    ((it.qty * (it.unit_price_cents - it.discount_cents)) / 100).toFixed(2) + '</span>' +
    (it.taxable ? ' · 应税' : '') + (it.food_exempt ? ' · 食品免税' : '') + '</div>' +
    '</div>').join('');
}

// 输入变化 → 同步到 items 并实时对账（事件委托）
$('itemList').addEventListener('input', e => {
  const row = e.target.closest('.edit-row');
  if (!row) return;
  const i = Number(row.dataset.index);
  const f = e.target.dataset.field;
  if (f === 'item_no') items[i].item_no = e.target.value.trim();
  if (f === 'name') items[i].name = e.target.value;
  if (f === 'qty') items[i].qty = Math.max(1, parseInt(e.target.value, 10) || 1);
  if (f === 'unit') { const v = dollarsToCents(e.target.value); if (!isNaN(v)) items[i].unit_price_cents = v; }
  if (f === 'discount') { const v = dollarsToCents(e.target.value); if (!isNaN(v)) items[i].discount_cents = v; }
  if (f === 'category') items[i].category = e.target.value;
  const net = row.querySelector('[data-net]');
  if (net) net.textContent = '$' + ((items[i].qty * (items[i].unit_price_cents - items[i].discount_cents)) / 100).toFixed(2);
  reconcile();
});
$('itemList').addEventListener('click', e => {
  const del = e.target.closest('[data-del]');
  if (del) {
    items.splice(Number(del.dataset.del), 1);
    renderItems();
    reconcile();
  }
});
['fSubtotal', 'fTax', 'fTotal'].forEach(id => $(id).addEventListener('input', reconcile));

$('addItemBtn').addEventListener('click', () => {
  items.push(emptyItem());
  renderItems();
  reconcile();
});

$('cancelBtn').addEventListener('click', () => {
  $('stepConfirm').hidden = true;
  $('stepUpload').hidden = false;
  $('previewImg').hidden = true;
  photoInput.value = '';
  window.scrollTo({ top: 0 });
});

function reconcile() {
  let gross = 0, disc = 0, qty = 0;
  for (const it of items) {
    gross += it.qty * it.unit_price_cents;
    disc += it.qty * it.discount_cents;
    qty += it.qty;
  }
  const sub = dollarsToCents($('fSubtotal').value);
  const tax = dollarsToCents($('fTax').value);
  const tot = dollarsToCents($('fTotal').value);
  const problems = [];
  if (!isNaN(sub) && Math.abs(gross - disc - sub) > 1) {
    problems.push('原价 ' + fmtMoney(gross) + ' − 折扣 ' + fmtMoney(disc) + ' = ' +
      fmtMoney(gross - disc) + '，与小计 ' + fmtMoney(sub) + ' 不符');
  }
  if (!isNaN(sub) && !isNaN(tax) && !isNaN(tot) && Math.abs(sub + tax - tot) > 1) {
    problems.push('小计 ' + fmtMoney(sub) + ' + 税 ' + fmtMoney(tax) + ' = ' +
      fmtMoney(sub + tax) + '，与总计 ' + fmtMoney(tot) + ' 不符');
  }
  const el = $('reconStatus');
  const saveBtn = $('saveBtn');
  if (problems.length) {
    el.className = 'recon-bad';
    el.textContent = '对账不通过：' + problems.join('；');
    saveBtn.disabled = true;
  } else {
    el.className = 'recon-ok';
    el.textContent = '✓ 对账通过：' + qty + ' 件，原价 ' + fmtMoney(gross) +
      '，立减 ' + fmtMoney(disc) + '，总计 ' + fmtMoney(isNaN(tot) ? sub + (isNaN(tax) ? 0 : tax) : tot);
    saveBtn.disabled = false;
  }
}

// ---------- 保存 ----------
$('saveBtn').addEventListener('click', async () => {
  const btn = $('saveBtn');
  btn.disabled = true;
  try {
    await saveReceipt(false);
  } catch (e) {
    if (e.status === 409) {
      // 重复小票：让用户确认是否仍要保存
      btn.disabled = false;
      confirmModal('可能重复导入', e.message + '确定仍要保存这张小票吗？', '仍要保存', async () => {
        btn.disabled = true;
        try { await saveReceipt(true); } catch (e2) { toast(e2.message); btn.disabled = false; }
      });
      return;
    }
    toast(e.message);
    btn.disabled = false;
  }
});

async function saveReceipt(force) {
  let qtyTotal = 0, savings = 0;
  for (const it of items) { qtyTotal += it.qty; savings += it.qty * it.discount_cents; }
  const body = {
    upload_id: uploadId,
    store: $('fStore').value.trim(),
    store_no: $('fStoreNo').value.trim(),
    purchased_at: $('fDate').value.trim(),
    subtotal_cents: dollarsToCents($('fSubtotal').value),
    tax_cents: dollarsToCents($('fTax').value),
    total_cents: dollarsToCents($('fTotal').value),
    savings_cents: savings,
    item_count: qtyTotal,
    raw_text: rawText,
    items: items,
    force: force,
  };
  const d = await api('/api/v1/receipts', { method: 'POST', body: JSON.stringify(body) });
  toast(d.alerts_created > 0
    ? '已保存，并生成 ' + d.alerts_created + ' 条提醒'
    : '已保存');
  location.href = '/receipt/' + d.id;
}
