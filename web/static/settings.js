// 设置页：改密码 + 数据概况。

initTopbar();

api('/api/v1/me').then(me => {
  document.getElementById('meLine').textContent = '当前用户：' + me.username + (me.is_admin ? '（管理员）' : '');
}).catch(() => {});

api('/api/v1/stats?period=all').then(d => {
  document.getElementById('dataLine').textContent =
    '累计花费 ' + fmtMoney(d.total_cents) + '，最近小票 ' + (d.recent_receipts || []).length + ' 张（见首页列表）' +
    '，未处理提醒 ' + (d.open_alerts || 0) + ' 条。';
}).catch(() => {});

document.getElementById('pwForm').addEventListener('submit', async e => {
  e.preventDefault();
  const err = document.getElementById('pwErr');
  err.textContent = '';
  const nw = document.getElementById('pwNew').value;
  if (nw !== document.getElementById('pwNew2').value) {
    err.textContent = '两次新密码不一致';
    return;
  }
  try {
    await api('/api/v1/me/password', {
      method: 'POST',
      body: JSON.stringify({ current: document.getElementById('pwCurrent').value, new: nw }),
    });
    toast('密码已改，请重新登录');
    setTimeout(() => { location.href = '/login'; }, 800);
  } catch (ex) { err.textContent = ex.message; }
});
