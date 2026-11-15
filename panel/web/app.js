// AgentAwake 面板前端
// 桥接：window.mygo.call("Api.Method", ...args) → Promise

const $ = (id) => document.getElementById(id);
const call = (m, ...a) => window.mygo.call(`Api.${m}`, ...a);

let page = 'status';
let lastLeaseSig = '';
let offsetTouched = false;
let maxHoursTouched = false;
let firstPage = true;

// 线性图标（hugeicons 风格，1.6 描边）
const ICONS = {
  sleep: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"
    stroke-linecap="round" stroke-linejoin="round"><path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5Z"/></svg>`,
  hibernate: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"
    stroke-linecap="round" stroke-linejoin="round"><path d="M7 5v10a2 2 0 0 0 2 2h8"/><path d="M17 13v6"/><path d="M14 16h6"/></svg>`,
  display: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"
    stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M9 20h6"/><path d="M12 16v4"/></svg>`,
};

// ---------- 页面 ----------
function showPage(name) {
  page = name;
  $('pageStatus').hidden = name !== 'status';
  $('pageSettings').hidden = name !== 'settings';
  $('navBtn').textContent = name === 'status' ? '设置' : '返回';
  const applied = call('SetPage', name).catch(() => {});
  // 设置页内容固定，窗口高度按内容贴合（等后端确认页面已切过去再量）
  if (name === 'settings') applied.then(fitSettingsWindow);
}

// fitSettingsWindow 量出设置页的自然高度，交给主进程把窗口调到刚好放下。
// 做法：临时取消 .app 的 100vh 与 main 的弹性高度，让布局按内容撑开再量，
// 量完立刻还原（同一个任务内完成，不会闪一帧）。
function fitSettingsWindow() {
  if (page !== 'settings') return;
  const appEl = document.querySelector('.app');
  const mainEl = $('pageSettings');
  if (!appEl || !mainEl) return;

  const prevAppHeight = appEl.style.height;
  const prevMainFlex = mainEl.style.flex;
  appEl.style.height = 'auto';
  mainEl.style.flex = '0 0 auto';
  const h = Math.ceil(appEl.getBoundingClientRect().height);
  appEl.style.height = prevAppHeight;
  mainEl.style.flex = prevMainFlex;

  call('FitHeight', h + 2).catch(() => {});
}

$('navBtn').onclick = () => showPage(page === 'status' ? 'settings' : 'status');
$('closeBtn').onclick = () => window.mygo.window.close();

// ---------- 等待 / 异常反馈 ----------
let pending = 0;

function beginPending() {
  pending++;
  for (const id of ['progress', 'progress2']) {
    const el = $(id);
    if (el) el.hidden = false;
  }
}
function endPending() {
  pending = Math.max(0, pending - 1);
  if (pending === 0) {
    for (const id of ['progress', 'progress2']) {
      const el = $(id);
      if (el) el.hidden = true;
    }
  }
}

// 轻量包装：所有桥接调用都带等待指示
async function callPending(method, ...args) {
  beginPending();
  try {
    return await call(method, ...args);
  } finally {
    endPending();
  }
}

function showError(msg) {
  const el = $('alert');
  if (!msg) { el.hidden = true; return; }
  el.querySelector('.alert-text').textContent = msg;
  el.hidden = false;
}
$('alertClose').onclick = () => { showError(''); dismissedError = true; };
let dismissedError = false;

// ---------- 渲染 ----------
function render(s) {
  $('dot').className = 'dot' + (s.holding ? ' on' : (s.error ? ' err' : ''));
  $('statusLabel').textContent = s.label;
  $('statusSub').textContent = s.sub;
  if (s.build) $('buildTag').textContent = '· ' + s.build;

  // 后端报错时显示提醒条（用户可关闭）
  if (s.error) {
    if (!dismissedError) showError(s.error);
  } else {
    dismissedError = false;
    showError('');
  }

  if (s.wantSettings && page !== 'settings') showPage('settings');

  renderChips(s);
  renderLeases(s);

  if (document.activeElement !== $('offsetRange')) $('offsetRange').value = s.offsetX;
  $('offsetVal').textContent = (s.offsetX > 0 ? '+' : '') + s.offsetX + ' px';
  if (document.activeElement !== $('maxHoursRange')) $('maxHoursRange').value = s.maxHours;
  $('maxHoursVal').textContent = s.maxHours + ' 小时';
  $('showBarSwitch').checked = s.showBar;
  $('pulseSwitch').checked = s.pulse;
  // 保持中 + 开启呼吸 → 状态块轻微明暗循环
  const pulsing = s.holding && s.pulse;
  document.body.classList.toggle('pulse-on', pulsing);
}

function renderChips(s) {
  const box = $('powerChips');
  const items = [
    ['sleep', '睡眠', s.sleep],
    ['hibernate', '休眠', s.hibernate],
    ['display', '熄屏', s.display],
  ];
  const sig = items.map(i => i[2]).join('|');
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;
  box.innerHTML = '';
  for (const [key, label, value] of items) {
    const el = document.createElement('div');
    el.className = 'chip';
    el.innerHTML = `${ICONS[key]}<div class="chip-body">
      <span class="chip-label"></span><span class="chip-value"></span></div>`;
    el.querySelector('.chip-label').textContent = label;
    el.querySelector('.chip-value').textContent = value;
    box.appendChild(el);
  }
}

function renderLeases(s) {
  const sig = JSON.stringify(s.leases.map(l => [l.id, l.active, l.detail, l.name]));
  if (sig === lastLeaseSig) return;
  lastLeaseSig = sig;

  const box = $('leases');
  box.innerHTML = '';
  for (const l of s.leases) {
    const el = document.createElement('div');
    el.className = 'lease' + (l.active ? '' : ' dead');
    el.innerHTML = `<div class="lease-info">
        <div class="lease-top"><span class="lease-name"></span><span class="lease-tag"></span></div>
        <span class="lease-detail"></span>
      </div>`;
    el.querySelector('.lease-name').textContent = l.name;
    el.querySelector('.lease-tag').textContent = l.mode === 'deadline' ? '定时' : '等完成';
    el.querySelector('.lease-detail').textContent = l.detail || '';
    if (l.active) {
      const b = document.createElement('button');
      b.className = 'btn small';
      b.textContent = '完成';
      b.onclick = async () => { await call('Release', l.id); toast('已通知完成'); refresh(); };
      el.appendChild(b);
    }
    box.appendChild(el);
  }
}

// ---------- 提示 ----------
let toastTimer;
function toast(msg) {
  const t = $('toast');
  t.textContent = msg;
  t.hidden = false;
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    t.classList.remove('show');
    setTimeout(() => { t.hidden = true; }, 220);
  }, 1800);
}

// ---------- 操作 ----------
// 按钮执行中显示 pending，避免「点了没反应」的感觉
async function runAction(btn, fn, done) {
  if (btn.dataset.busy === '1') return;
  btn.dataset.busy = '1';
  const prev = btn.textContent;
  btn.disabled = true;
  btn.textContent = '…';
  beginPending();
  try {
    await fn();
    if (done) toast(done);
    dismissedError = false;
  } catch (e) {
    showError('操作失败：' + (e && e.message ? e.message : e));
    toast('操作失败');
  } finally {
    btn.disabled = false;
    btn.textContent = prev;
    btn.dataset.busy = '0';
    endPending();
    refresh();
  }
}

$('holdBtns').addEventListener('click', (e) => {
  const b = e.target.closest('button');
  if (!b) return;
  if (b.id === 'stopBtn') {
    runAction(b, () => call('Stop'), '已全部停止');
  } else if (b.dataset.min) {
    runAction(b, () => call('HoldMinutes', Number(b.dataset.min)), `保持 ${b.dataset.min} 分钟`);
  } else if (b.dataset.hours) {
    runAction(b, () => call('HoldMinutes', Number(b.dataset.hours) * 60), `保持 ${b.dataset.hours} 小时`);
  } else if (b.dataset.until) {
    runAction(b, () => call('HoldUntil', b.dataset.until), `保持到 ${b.dataset.until}`);
  }
});

$('offsetRange').addEventListener('pointerdown', () => { offsetTouched = true; });
$('offsetRange').addEventListener('input', (e) => {
  const v = Number(e.target.value);
  $('offsetVal').textContent = (v > 0 ? '+' : '') + v + ' px';
});
$('offsetRange').addEventListener('change', async (e) => {
  if (!offsetTouched) return;   // 只接受用户真实拖动
  offsetTouched = false;
  await call('SetOffset', Number(e.target.value));
});
$('resetOffset').onclick = async () => {
  offsetTouched = false;
  await call('SetOffset', 0);
  toast('位置已重置');
  refresh();
};
$('showBarSwitch').addEventListener('change', async (e) => {
  await call('SetShowBar', e.target.checked);
});
$('pulseSwitch').addEventListener('change', async (e) => {
  await call('SetPulse', e.target.checked);
  refresh();
});
$('maxHoursRange').addEventListener('pointerdown', () => { maxHoursTouched = true; });
$('maxHoursRange').addEventListener('input', (e) => {
  $('maxHoursVal').textContent = e.target.value + ' 小时';
});
$('maxHoursRange').addEventListener('change', async (e) => {
  if (!maxHoursTouched) return;
  maxHoursTouched = false;
  await call('SetMaxHours', Number(e.target.value));
});

// ---------- 轮询 ----------
async function refresh() {
  try {
    const s = await call('State');
    if (s) render(s);
  } catch (err) {
    showError('无法读取状态：' + (err && err.message ? err.message : err));
  }
}

refresh();
(async () => {
  try { showPage(await call('InitialPage')); } catch { showPage('status'); }
  try {
    const demo = await call('InitialToast');
    if (demo) setTimeout(() => toast(demo), 300);
  } catch {}
})();
setInterval(refresh, 1000);

// 字体加载完成后可能换行方式变了，重新量一次设置页高度
if (document.fonts && document.fonts.ready) {
  document.fonts.ready.then(fitSettingsWindow).catch(() => {});
}
