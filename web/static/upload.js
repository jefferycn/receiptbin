// 扫小票：拍照上传 → OCR → 确认页人工核对 → 保存。
// 也可以上传官网导出的小票 PDF：直接读文本层解析，比 OCR 准。
// 确认页实时对账：原价 − 折扣 = 小计，小计 + 税 = 总计，
// 对不上（误差 > 1 分）时保存按钮禁用；服务端保存时还会再校验一次。
// 分类与中文名由商品词典自动填：确认页只显示解析出的分类，不用手动选；
// 词典没覆盖的商品默认“其他”，事后用词典 API 批量改中文名与分类。

initTopbar();

let uploadId = '';
let rawText = '';
let isPDF = false;
let receiptDiscount = 0; // 整单立减（分），PDF 导出才有，不挂给单件商品
let terminalNo = ''; // 收银台号（Trm），判重用；读不出为空
let transactionNo = ''; // 交易号（Trn），同上
let items = []; // {item_no, name, name_zh, qty, unit_price_cents, discount_cents, taxable, food_exempt, category}
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

function isPDFFile(file) {
  return file.type === 'application/pdf' || /\.pdf$/i.test(file.name || '');
}

async function handleFile(file) {
  const pdf = isPDFFile(file);
  if (!pdf && !file.type.startsWith('image/')) { toast('请上传图片或 PDF'); return; }
  const preview = $('previewImg');
  if (pdf) {
    preview.hidden = true;
  } else {
    preview.src = URL.createObjectURL(file);
    preview.hidden = false;
  }
  $('ocrStatus').innerHTML = '<span class="spinner"></span> ' + (pdf ? '正在读取 PDF，请稍候…' : '正在识别小票，请稍候…');
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
  isPDF = !!d.is_pdf;
  receiptDiscount = (d.receipt && d.receipt.receipt_discount_cents) || 0;
  terminalNo = (d.receipt && d.receipt.terminal_no) || '';
  transactionNo = (d.receipt && d.receipt.transaction_no) || '';
  showConfirm(d.receipt, d.problems || []);
}

// 手动录入：OCR 完全失败时的退路，从空表开始填。
$('manualBtn').addEventListener('click', () => {
  uploadId = '';
  rawText = '';
  isPDF = false;
  receiptDiscount = 0;
  terminalNo = '';
  transactionNo = '';
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
  const pdfLink = $('confirmPdfLink');
  if (uploadId && isPDF) {
    thumb.hidden = true;
    pdfLink.href = '/api/v1/pending/' + uploadId;
    pdfLink.hidden = false;
  } else if (uploadId) {
    pdfLink.hidden = true;
    thumb.src = '/api/v1/pending/' + uploadId;
    thumb.hidden = false;
    thumb.onclick = () => {
      const m = openModal('<img src="/api/v1/pending/' + uploadId + '" style="max-width:100%;border-radius:12px">' +
        '<p style="margin-top:12px"><button class="btn" id="mClose">关闭</button></p>');
      m.querySelector('#mClose').onclick = closeModal;
    };
  } else {
    thumb.hidden = true;
    pdfLink.hidden = true;
  }
  const warns = [...(receipt.warnings || []), ...problems];
  $('warnBox').innerHTML = warns.length
    ? '<div class="warn"><strong>请注意</strong><ul>' + warns.map(w => '<li>' + esc(w) + '</li>').join('') + '</ul></div>'
    : '';
  renderItems();
  reconcile();
  window.scrollTo({ top: 0 });
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
    (it.name_zh ? '<div class="muted" style="margin-top:4px">' + esc(it.name_zh) + '</div>' : '') +
    '<div class="edit-grid">' +
    '<div><label>数量</label><input type="number" data-field="qty" value="' + it.qty + '" min="1" step="1"></div>' +
    '<div><label>单价 $</label><input type="text" data-field="unit" value="' + centsToDollars(it.unit_price_cents) + '" inputmode="decimal"></div>' +
    '<div><label>折扣 $</label><input type="text" data-field="discount" value="' + centsToDollars(it.discount_cents) + '" inputmode="decimal"></div>' +
    '<div><label>分类</label><div class="muted" style="padding:8px 0">' + esc(it.category || '其他') + '</div></div>' +
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
  isPDF = false;
  receiptDiscount = 0;
  window.scrollTo({ top: 0 });
});

function reconcile() {
  let gross = 0, disc = receiptDiscount, qty = 0; // 整单立减也计入折扣合计
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
      // 重复小票：覆盖原有小票，或仍要保存一份新的
      btn.disabled = false;
      const dupId = (e.data && e.data.duplicate_id) || '';
      const m = openModal('<h3>已经有一张相同的小票</h3><p>' + esc(e.message) + '</p>' +
        '<p class="muted" style="font-size:14px">覆盖会删除旧的那张（包括它的提醒与价格记录），用这次导入的替换它；解析错了重导时选这个。</p>' +
        '<div style="display:flex;flex-direction:column;gap:8px;margin-top:16px">' +
        '<button class="btn primary big" id="mReplace">覆盖原有小票</button>' +
        '<button class="btn big" id="mForce">仍要保存一份新的</button>' +
        '<button class="btn big" id="mCancelDup">取消</button></div>');
      m.querySelector('#mCancelDup').onclick = closeModal;
      m.querySelector('#mReplace').onclick = async () => {
        closeModal();
        btn.disabled = true;
        try { await saveReceipt(true, dupId); } catch (e2) { toast(e2.message); btn.disabled = false; }
      };
      m.querySelector('#mForce').onclick = async () => {
        closeModal();
        btn.disabled = true;
        try { await saveReceipt(true, ''); } catch (e2) { toast(e2.message); btn.disabled = false; }
      };
      return;
    }
    toast(e.message);
    btn.disabled = false;
  }
});

async function saveReceipt(force, replaceId) {
  let qtyTotal = 0, savings = receiptDiscount;
  for (const it of items) { qtyTotal += it.qty; savings += it.qty * it.discount_cents; }
  const body = {
    upload_id: uploadId,
    store: $('fStore').value.trim(),
    store_no: $('fStoreNo').value.trim(),
    purchased_at: $('fDate').value.trim(),
    terminal_no: terminalNo,
    transaction_no: transactionNo,
    subtotal_cents: dollarsToCents($('fSubtotal').value),
    tax_cents: dollarsToCents($('fTax').value),
    total_cents: dollarsToCents($('fTotal').value),
    savings_cents: savings,
    receipt_discount_cents: receiptDiscount,
    item_count: qtyTotal,
    raw_text: rawText,
    items: items,
    force: force,
    replace_id: replaceId || '',
  };
  const d = await api('/api/v1/receipts', { method: 'POST', body: JSON.stringify(body) });
  toast((d.replaced_id ? '已覆盖原有小票' : '已保存') +
    (d.alerts_created > 0 ? '，并生成 ' + d.alerts_created + ' 条提醒' : ''));
  location.href = '/receipt/' + d.id;
}
