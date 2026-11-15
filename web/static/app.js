// ReceiptBin 前端公共助手：API 请求、提示、弹窗、格式化、复制。
// 沿用 SimpleBin 的 app.js（copyText 的 iOS 手势处理原样保留：
// 复制必须在点按手势的同步调用栈内完成，先 await 再复制必失败）。

// api 调 JSON 接口；失败时抛 Error（message 为服务端返回的 error 字段）。
async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
    ...opts,
  });
  if (res.status === 401) { location.href = '/login'; throw new Error('需要登录'); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const e = new Error(data.error || ('请求失败：' + res.status));
    e.status = res.status; e.data = data;
    throw e;
  }
  return data;
}

// 上传文件：FormData，不设 Content-Type（浏览器自动加 boundary）。
async function apiUpload(path, formData) {
  const res = await fetch(path, { method: 'POST', credentials: 'same-origin', body: formData });
  if (res.status === 401) { location.href = '/login'; throw new Error('需要登录'); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || ('上传失败：' + res.status));
  return data;
}

let toastTimer = null;
function toast(msg) {
  const el = document.getElementById('toast');
  if (!el) return;
  el.textContent = msg;
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), 2200);
}

// esc 转义 HTML：用户内容拼进 innerHTML 前必须过一遍。
function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// fmtMoney：整数分 → "$12.49"。
function fmtMoney(cents) {
  if (cents == null) return '';
  const neg = cents < 0;
  const a = Math.abs(cents);
  return (neg ? '-$' : '$') + Math.floor(a / 100) + '.' + String(a % 100).padStart(2, '0');
}

// fmtDate："2026-09-12 14:25" → "09/12 14:25"；"2026-09-12" → "09/12"。
function fmtDate(s) {
  if (!s) return '';
  if (s.length >= 16) return s.slice(5, 10) + ' ' + s.slice(11, 16);
  if (s.length >= 10) return s.slice(5, 10);
  return s;
}

function fmtMonth(m) { // "2026-11" → "2026年11月"
  if (!m || m.length < 7) return m || '';
  return m.slice(0, 4) + '年' + String(Number(m.slice(5, 7))) + '月';
}

// copyText 写剪贴板：必须在点击事件里同步调用。
// 先同步试 execCommand（立即返回成败，iOS 上最可靠），不行再同步调
// Clipboard API（仍在手势内），都失败才报失败。
function copyText(s) {
  const ta = document.createElement('textarea');
  ta.value = s;
  ta.readOnly = true;
  ta.style.position = 'fixed';
  ta.style.top = '0';
  ta.style.left = '0';
  ta.style.width = '2em';
  ta.style.height = '2em';
  ta.style.padding = '0';
  ta.style.border = 'none';
  ta.style.outline = 'none';
  ta.style.background = 'transparent';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.setSelectionRange(0, ta.value.length);
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
  ta.blur(); // 先失焦再移除，避免 iOS 给这个隐藏输入框弹出编辑菜单
  ta.remove();
  if (ok) { toast('已复制'); return true; }
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(s).then(
      () => toast('已复制'),
      () => toast('复制失败，请长按手动复制'));
    return true;
  }
  toast('复制失败，请长按手动复制');
  return false;
}

// ---------- 弹窗 ----------
function openModal(html) {
  closeModal();
  const back = document.createElement('div');
  back.className = 'modal-backdrop';
  back.innerHTML = '<div class="modal">' + html + '</div>';
  back.addEventListener('click', e => { if (e.target === back) closeModal(); });
  document.body.appendChild(back);
  return back.querySelector('.modal');
}

function closeModal() {
  document.querySelectorAll('.modal-backdrop').forEach(el => el.remove());
}

function confirmModal(title, body, okText, onOk) {
  const m = openModal('<h3>' + esc(title) + '</h3><p>' + esc(body) + '</p>' +
    '<div class="btn-row"><button class="btn danger" id="mOk">' + esc(okText) + '</button>' +
    '<button class="btn" id="mCancel">取消</button></div>');
  m.querySelector('#mCancel').onclick = closeModal;
  m.querySelector('#mOk').onclick = async () => {
    try { await onOk(); } catch (e) { toast(e.message); return; }
    closeModal();
  };
}

// ---------- 顶栏 ----------
// 每页加载时调一次：⋯ 菜单开关、登出按钮、未处理提醒角标。
async function initTopbar() {
  const menuBtn = document.getElementById('topMenuBtn');
  const menuPop = document.getElementById('topMenuPop');
  if (menuBtn && menuPop) {
    menuBtn.addEventListener('click', e => {
      e.stopPropagation();
      menuPop.hidden = !menuPop.hidden;
    });
    document.addEventListener('click', e => {
      if (!e.target.closest('.topbar-menu')) menuPop.hidden = true;
    });
  }
  const btn = document.getElementById('logoutBtn');
  if (btn) btn.onclick = async () => {
    await api('/api/v1/logout', { method: 'POST' }).catch(() => {});
    location.href = '/login';
  };
  try {
    const d = await api('/api/v1/alerts?status=open');
    const n = d.open_count || 0;
    document.querySelectorAll('[data-alert-badge]').forEach(el => {
      el.textContent = n > 0 ? String(n) : '';
      el.hidden = n === 0;
    });
  } catch { /* 未登录页不调这个 */ }
}

// ---------- 分类颜色（环形图与图例共用） ----------
const CATEGORY_COLORS = {
  '生鲜': '#2fa84f', '肉类海鲜': '#e31837', '乳制品/饮品': '#f5a623',
  '烘焙/干货': '#b07d4f', '日用品': '#005daa', '厨房用品': '#7b61c9',
  '宠物': '#e67e22', '运动': '#00a3a3', '家居': '#8a8a8e', '其他': '#c0c0c8',
};
function categoryColor(c) { return CATEGORY_COLORS[c] || '#c0c0c8'; }
