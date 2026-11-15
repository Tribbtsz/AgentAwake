// 任务栏小条：只做展示 + 点击打开面板

const $ = (id) => document.getElementById(id);
const call = (m, ...a) => window.mygo.call(`Api.${m}`, ...a);

let last = '';

// 胶囊宽度 = 窗口宽度 - 两侧各 8px 留白（WebView2 不支持 max-content 收缩）
function fitPill() {
  const pill = $('pill');
  if (!pill) return;
  const w = Math.max(60, window.innerWidth - 16);
  pill.style.width = w + 'px';
}
window.addEventListener('resize', fitPill);
fitPill();

// 用任务栏底色做页面背景，圆角外不会露黑角
(async () => {
  try {
    const bg = await call('TaskbarColor');
    if (bg) document.body.style.background = bg;
  } catch {}
})();

// 立即推送（Go 主动发事件，不会被页面定时器节流）
if (window.mygo && window.mygo.on) {
  window.mygo.on('bar-state', (s) => apply(s));
}

function apply(s) {
  if (!s) return;
  $('dot').className = 'dot ' + s.state;
  $('label').textContent = s.label;
  $('count').textContent = s.count;
  // 保持中且开启呼吸：整块胶囊泛绿并起伏
  $('pill').classList.toggle('on', !!(s.holding && s.pulse));
}

async function refresh() {
  try {
    const s = await call('BarState');
    if (!s) return;
    const sig = `${s.state}|${s.label}|${s.count}|${s.holding}|${s.pulse}`;
    if (sig === last) return;
    last = sig;
    apply(s);
  } catch {
    /* 静默：小条不打扰用户 */
  }
}

document.body.addEventListener('click', () => {
  call('OpenPanel').catch(() => {});
});

refresh();
setInterval(refresh, 1000);
