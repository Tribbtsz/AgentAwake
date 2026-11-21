// AgentAwake 面板前端
// 桥接：window.mygo.call("Api.Method", ...args) → Promise

const $ = (id) => document.getElementById(id);
const call = (m, ...a) => window.mygo.call(`Api.${m}`, ...a);

let page = 'status';
let lastLeaseSig = '';
const leaseItems = new Map(); // 租约行按 id 复用，新行才播放入场动画
let offsetHeld = false;      // 手指/鼠标在滑条上，轮询不要覆盖显示值
let maxHoursHeld = false;
let firstPage = true;

// ---------- Toast（成功/失败提示 + 加载态） ----------
let toastTimer;
let toastLoading = false;

// showToast 显示一条提示；loading=true 时常驻并显示内置转圈图标，
// 直到 hideToast 或下一条提示替换。toast 固定在底部，不占布局高度。
function showToast(msg, loading = false, holdMs = 1800) {
  clearTimeout(toastTimer);
  toastLoading = loading;
  $('toastSpinner').hidden = !loading;
  $('toastText').textContent = msg;
  const el = $('toast');
  el.hidden = false;
  void el.offsetWidth; // 让隐藏→显示的过渡生效
  el.classList.add('show');
  if (!loading) {
    toastTimer = setTimeout(hideToast, holdMs);
  }
}

function hideToast() {
  toastLoading = false;
  clearTimeout(toastTimer);
  const el = $('toast');
  el.classList.remove('show');
  toastTimer = setTimeout(() => {
    if (!el.classList.contains('show')) el.hidden = true;
  }, 220);
}

function toast(msg) {
  showToast(msg, false);
}

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
// 三页：状态 / 日志 / 设置。每个按钮只管自己的页：当前页的按钮显示「返回」
// （回状态页），另一页按钮直跳——两个按钮各司其职，不会同时变「返回」。
function showPage(name) {
  page = name;
  $('pageStatus').hidden = name !== 'status';
  $('pageLog').hidden = name !== 'log';
  $('pageSettings').hidden = name !== 'settings';
  $('navBtn').textContent = name === 'settings' ? '返回' : '设置';
  $('navLogBtn').textContent = name === 'log' ? '返回' : '日志';
  const applied = call('SetPage', name).catch(() => {});
  if (name === 'settings') {
    $('pageSettings').scrollTop = 0;
    // 设置页内容固定，窗口高度按内容贴合（等后端确认页面已切过去再量）
    applied.then(fitSettingsWindow);
  }
  if (name === 'log') {
    $('logList').scrollTop = 0;
    loadLog(); // 进页立刻拉，不等 1 秒轮询
  }
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

$('navBtn').onclick = () => showPage(page === 'settings' ? 'status' : 'settings');
$('navLogBtn').onclick = () => showPage(page === 'log' ? 'status' : 'log');
$('closeBtn').onclick = () => window.mygo.window.close();

// ---------- Agent 提示词 ----------
let promptText = '';
let promptEditing = false;

$('copyPromptBtn').onclick = async () => {
  try {
    const text = await call('Prompt');
    await call('CopyText', text);
    toast('提示词已复制');
  } catch (e) {
    toast('复制失败');
  }
};

function paintPrompt() {
  $('promptView').textContent = promptText;
  if (!promptEditing) $('promptEditBox').value = promptText;
}

// 编辑框按内容撑开：不要 textarea 自己的滚动条，超出窗口交给页面滚动
function growPromptBox() {
  const box = $('promptEditBox');
  if (!box || box.hidden) return;
  box.style.height = 'auto';
  box.style.height = box.scrollHeight + 'px';
}

async function loadPrompt() {
  try {
    promptText = await call('Prompt');
  } catch {
    promptText = '';
  }
  paintPrompt();
  fitSettingsWindow();
}

function setPromptEditing(on) {
  promptEditing = on;
  $('promptView').hidden = on;
  $('promptEditBox').hidden = !on;
  $('promptEdit').hidden = on;
  $('promptReset').hidden = !on;
  $('promptCancel').hidden = !on;
  $('promptSave').hidden = !on;
  if (on) {
    $('promptEditBox').value = promptText;
    growPromptBox();
    $('promptEditBox').focus();
    fitSettingsWindow();
  } else {
    paintPrompt();
    fitSettingsWindow();
  }
}

$('promptEdit').onclick = () => setPromptEditing(true);
$('promptCancel').onclick = () => setPromptEditing(false);
$('promptEditBox').addEventListener('input', () => {
  growPromptBox();
  fitSettingsWindow();
});
$('promptSave').onclick = async () => {
  const text = $('promptEditBox').value.trim();
  if (!text) {
    toast('提示词不能为空');
    return;
  }
  try {
    await callPending('SetPrompt', text);
    promptText = text;
    setPromptEditing(false);
    toast('提示词已保存');
  } catch (e) {
    toast('保存失败');
  }
};
$('promptReset').onclick = async () => {
  try {
    promptText = await call('DefaultPrompt');
    $('promptEditBox').value = promptText;
    growPromptBox();
    fitSettingsWindow();
    toast('已填入默认提示词，保存后生效');
  } catch (e) {
    toast('读取默认提示词失败');
  }
};

// ---------- 日志页 ----------
// 读 awake.log 的尾部：申请/释放/手动停止/守护进程/错误都在里面。
// 最新在前；1 秒轮询 + 进页立即拉，事件发生后基本上一秒内可见。
let logClearArmed = false; // 清空按钮两段式确认：第一次点亮，3 秒内再点才执行
let logClearTimer;
let lastLogSig = '';

async function loadLog() {
  if (page !== 'log') return;
  let rows = [];
  try {
    rows = await call('Log');
  } catch {
    // 读不到就保持现状，下一秒轮询再试
    return;
  }
  // 只有内容真的变了才重建 DOM：最后一行（最新）的时间+文本变化即代表有新事件
  const top = rows[0] ? rows[0].time + '|' + rows[0].text : '';
  const sig = rows.length + '|' + top;
  if (sig === lastLogSig) return;
  lastLogSig = sig;

  const box = $('logList');
  // 新行总是插在最前面。如果用户正停在顶部，重建后必须钉回顶部：
  // 否则浏览器的滚动锚定会保留旧锚点，把最新一两行顶出可视区（看起来「顶部被遮住」）。
  const pinnedTop = box.scrollTop <= 8;
  box.innerHTML = '';
  $('logCount').textContent = rows.length >= 300 ? '· 最近 300 行' : (rows.length ? `· ${rows.length} 行` : '');
  if (rows.length === 0) return; // 空态样式由 CSS :empty::after 呈现

  // 泳道：同一张租约的「新建→结束」用一条竖线连起来（最新在上：结束点在上、新建点在下）。
  // Go 侧已算好每行穿过哪些泳道（lanes）以及本行事件在哪个泳道（lane）。
  let maxLane = -1;
  for (const r of rows) {
    for (const l of (r.lanes || [])) if (l > maxLane) maxLane = l;
  }
  const laneColors = ['var(--accent)', '#7fc99b', '#c9a878'];
  const laneX = (l) => 4 + l * 8;
  const gutterW = maxLane >= 0 ? (maxLane + 1) * 8 + 2 : 0;

  for (let ri = 0; ri < rows.length; ri++) {
    const r = rows[ri];
    const el = document.createElement('div');
    el.className = 'lrow' + (r.kind === 'warn' ? ' warn' : '');
    if (gutterW) el.style.paddingLeft = (gutterW + 4) + 'px';

    if (gutterW) {
      const gut = document.createElement('div');
      gut.className = 'lgutter';
      gut.title = '竖线：同一张租约「新建 → 结束」';
      for (const lane of (r.lanes || [])) {
        const line = document.createElement('span');
        // 第 0 行是日志窗口顶端：延伸到顶端的线（穿过行 / 起点在顶行）向上渐隐，
        // 表示「还在继续」；结束点就在顶行时线只向下，不碰顶端，无需渐隐。
        const reachesTop = ri === 0 && !(r.lane === lane && r.event === 'end');
        line.className = 'lline' + (reachesTop ? ' edge' : '');
        line.style.left = laneX(lane) + 'px';
        line.style.background = laneColors[lane % laneColors.length];
        if (r.lane === lane) {
          // 起点行：线从圆点向上（接更新的行）；结束行：线从圆点向下（接更旧的行）。
          // 上下各多出 1px，盖住行间距，让竖线跨行连续。
          if (r.event === 'start') {
            line.style.top = '-1px';
            line.style.bottom = '50%';
          } else if (r.event === 'end') {
            line.style.top = '50%';
            line.style.bottom = '-1px';
          }
        }
        gut.appendChild(line);
      }
      if (r.lane >= 0 && r.event) {
        const dot = document.createElement('span');
        dot.className = 'ldot' + (r.event === 'end' ? ' end' : r.event === 'mid' ? ' mid' : '');
        dot.style.left = laneX(r.lane) + 'px';
        dot.style.setProperty('--lane', laneColors[r.lane % laneColors.length]);
        gut.appendChild(dot);
      }
      el.appendChild(gut);
    }

    const time = document.createElement('span');
    time.className = 'ltime';
    time.textContent = r.time || '';
    el.appendChild(time);
    const src = document.createElement('span');
    src.className = 'lsrc ' + (r.source || '');
    src.textContent = r.source || '—';
    el.appendChild(src);
    const msg = document.createElement('span');
    msg.className = 'lmsg';
    msg.textContent = r.text;
    msg.title = r.text; // 单行截断，悬停看全文
    el.appendChild(msg);

    // 行右侧悬浮「复制」：复制完整一行（时间 + 来源 + 内容），便于贴给 agent 或 issue
    const line = `${r.time}  [${r.source || '-'}] ${r.text}`;
    const copy = document.createElement('button');
    copy.className = 'lcopy';
    copy.textContent = '复制';
    copy.title = '复制这一行';
    copy.onclick = async (e) => {
      e.stopPropagation();
      try {
        await call('CopyText', line);
        toast('已复制');
      } catch {
        toast('复制失败');
      }
    };
    el.appendChild(copy);
    box.appendChild(el);
  }
  if (pinnedTop) box.scrollTop = 0;
}

$('clearLogBtn').onclick = async () => {
  const btn = $('clearLogBtn');
  if (!logClearArmed) {
    logClearArmed = true;
    btn.textContent = '确认清空';
    btn.classList.add('danger');
    clearTimeout(logClearTimer);
    logClearTimer = setTimeout(disarmLogClear, 3000);
    return;
  }
  disarmLogClear();
  try {
    await callPending('ClearLog');
    toast('已清空');
    lastLogSig = '';
    $('logList').innerHTML = '';
    $('logCount').textContent = '';
    // 清空后补一条「日志已清空」，确认动作已生效
    loadLog();
  } catch {
    toast('清空失败');
  }
};

function disarmLogClear() {
  logClearArmed = false;
  clearTimeout(logClearTimer);
  const btn = $('clearLogBtn');
  btn.textContent = '清空';
  btn.classList.remove('danger');
}

// ---------- 等待 / 异常反馈 ----------
let pending = 0;

// 加载态用底部 Toast 显示，不再在顶部插入进度条（那会挤动整个页面布局）
function beginPending() {
  pending++;
  if (pending === 1) showToast('正在处理…', true);
}

function endPending() {
  pending = Math.max(0, pending - 1);
  if (pending === 0 && toastLoading) hideToast();
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
let lastErrorText = '';

// ---------- 渲染 ----------
function setText(el, value) {
  if (el && el.textContent !== value) el.textContent = value;
}

const offsetText = (v) => (v > 0 ? '+' : '') + v + ' px';

function render(s) {
  const dot = $('dot');
  const dotClass = 'dot' + (s.holding ? ' on' : (s.error ? ' err' : ''));
  if (dot.className !== dotClass) dot.className = dotClass;
  setText($('statusLabel'), s.label);
  setText($('statusSub'), s.sub);
  if (s.build) setText($('buildTag'), '· ' + s.build);

  // 后端报错时显示提醒条（用户可关闭；错误内容变了要重新提醒）
  if (s.error) {
    if (s.error !== lastErrorText) {
      lastErrorText = s.error;
      dismissedError = false;
    }
    if (!dismissedError) showError(s.error);
  } else {
    lastErrorText = '';
    dismissedError = false;
    showError('');
  }

  if (s.wantSettings && page !== 'settings') showPage('settings');

  renderChips(s);
  renderLeases(s);

  // 滑条还在焦点里（拖动/键盘调整）时不要用轮询来的旧值覆盖，
  // 否则数字和滑块会一秒跳回旧值一次。
  const offsetRange = $('offsetRange');
  if (!offsetHeld && document.activeElement !== offsetRange) {
    offsetRange.value = s.offsetX;
    setText($('offsetVal'), offsetText(s.offsetX));
  }
  const maxHoursRange = $('maxHoursRange');
  if (!maxHoursHeld && document.activeElement !== maxHoursRange) {
    maxHoursRange.value = s.maxHours;
    setText($('maxHoursVal'), s.maxHours + ' 小时');
  }
  $('showBarSwitch').checked = s.showBar;
  $('pulseSwitch').checked = s.pulse;
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
  const sig = JSON.stringify(s.leases.map(l => [l.id, l.active, l.detail, l.name, l.mode]));
  if (sig === lastLeaseSig) return;
  lastLeaseSig = sig;

  const box = $('leases');
  const ordered = [];
  const seen = new Set();
  for (const l of s.leases) {
    seen.add(l.id);
    let item = leaseItems.get(l.id);
    if (!item) {
      const el = document.createElement('div');
      el.className = 'lease enter';
      el.innerHTML = `<div class="lease-info">
          <div class="lease-top"><span class="lease-name"></span><span class="lease-tag"></span></div>
          <span class="lease-detail"></span>
        </div>`;
      item = {
        el,
        name: el.querySelector('.lease-name'),
        tag: el.querySelector('.lease-tag'),
        detail: el.querySelector('.lease-detail'),
        btn: null,
      };
      // 入场动画只跑一次，结束后去掉 class，后续文字更新不会再触发。
      // animationend 偶尔不触发（窗口切后台/减少动画）时用定时器兜底。
      const clearEnter = () => el.classList.remove('enter');
      el.addEventListener('animationend', clearEnter, { once: true });
      setTimeout(clearEnter, 400);
      leaseItems.set(l.id, item);
    }
    item.el.classList.toggle('dead', !l.active);
    setText(item.name, l.name);
    setText(item.tag, l.mode === 'deadline' ? '定时' : '等完成');
    setText(item.detail, l.detail || '');
    if (l.active && !item.btn) {
      const b = document.createElement('button');
      b.className = 'btn small';
      b.textContent = '完成';
      b.onclick = () => runAction(b, () => call('Release', l.id), '已通知完成');
      item.el.appendChild(b);
      item.btn = b;
    } else if (!l.active && item.btn) {
      item.btn.remove();
      item.btn = null;
    }
    ordered.push(item);
  }

  // 只在新行第一次插入时触发入场动画；已有行按顺序移动不会重播
  ordered.forEach((item, i) => {
    if (box.children[i] !== item.el) box.insertBefore(item.el, box.children[i] || null);
  });
  for (const [id, item] of leaseItems) {
    if (!seen.has(id)) {
      item.el.remove();
      leaseItems.delete(id);
    }
  }
}

// ---------- 操作 ----------
// 按钮执行中只禁用防重复点击，不改文字（避免按钮行宽度变化），
// 加载反馈统一交给底部 Toast。
async function runAction(btn, fn, done) {
  if (btn.dataset.busy === '1') return;
  btn.dataset.busy = '1';
  btn.disabled = true;
  beginPending();
  try {
    await fn();
    if (done) toast(done);
    dismissedError = false;
  } catch (e) {
    const msg = e && e.message ? String(e.message) : String(e);
    showError('操作失败：' + msg);
    toast(msg.split('\n')[0].slice(0, 48) || '操作失败');
  } finally {
    btn.disabled = false;
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

const offsetRange = $('offsetRange');
const maxHoursRange = $('maxHoursRange');

// 拖动期间锁住显示值，哪怕焦点意外丢失也不让 1 秒轮询把数字改回旧值
function holdSlider(el, setHeld) {
  el.addEventListener('pointerdown', () => setHeld(true));
  el.addEventListener('pointerup', () => setTimeout(() => setHeld(false), 200));
  el.addEventListener('pointercancel', () => setHeld(false));
}
holdSlider(offsetRange, (v) => { offsetHeld = v; });
holdSlider(maxHoursRange, (v) => { maxHoursHeld = v; });

offsetRange.addEventListener('input', (e) => {
  setText($('offsetVal'), offsetText(Number(e.target.value)));
});
offsetRange.addEventListener('change', async (e) => {
  await call('SetOffset', Number(e.target.value));
  refresh();
});
$('resetOffset').onclick = async () => {
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
maxHoursRange.addEventListener('input', (e) => {
  setText($('maxHoursVal'), e.target.value + ' 小时');
});
maxHoursRange.addEventListener('change', async (e) => {
  await call('SetMaxHours', Number(e.target.value));
  refresh();
});

// ---------- 轮询 ----------
let refreshing = false;

async function refresh() {
  if (refreshing) return;   // 上一次还没回来，不要堆积请求
  refreshing = true;
  try {
    const s = await call('State');
    if (s) render(s);
  } catch (err) {
    showError('无法读取状态：' + (err && err.message ? err.message : err));
  } finally {
    refreshing = false;
  }
  // 日志页可见时顺带刷新（事件发生后一秒内就能看到）
  if (page === 'log') await loadLog();
}

refresh();
loadPrompt();
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
