package app

// 内嵌控制台页面。

func consoleHTML(prefix string) string {
	return `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Today · CPA 插件控制台</title>
<style>
:root{--bg:#0e0f12;--card:#171a1f;--line:#262b33;--fg:#e8eaed;--muted:#9aa3ad;--accent:#5b8def;--ok:#3fb950;--err:#f85149}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
.wrap{max-width:900px;margin:0 auto;padding:28px 20px 64px}
h1{font-size:20px;margin:0 0 4px}
.sub{color:var(--muted);margin:0 0 24px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:18px;margin-bottom:16px}
.card h2{font-size:14px;margin:0 0 14px;color:var(--muted);font-weight:600;letter-spacing:.04em;text-transform:uppercase}
label{display:block;font-size:12px;color:var(--muted);margin:10px 0 5px}
input,textarea,select{width:100%;background:#0f1216;border:1px solid var(--line);color:var(--fg);border-radius:7px;padding:9px 11px;font:inherit;outline:none}
input:focus,textarea:focus,select:focus{border-color:var(--accent)}
textarea{min-height:70px;resize:vertical;font-family:ui-monospace,Consolas,monospace;font-size:12px}
button{background:var(--accent);color:#fff;border:0;border-radius:7px;padding:9px 16px;font:inherit;font-weight:600;cursor:pointer;margin-top:14px}
button.ghost{background:transparent;border:1px solid var(--line);color:var(--fg);font-weight:400;margin-left:8px}
button:disabled{opacity:.5;cursor:not-allowed}
pre{background:#0b0d10;border:1px solid var(--line);border-radius:7px;padding:12px;overflow:auto;font-size:12px;max-height:340px;white-space:pre-wrap;word-break:break-word}
.row{display:flex;gap:12px}.row>*{flex:1}
.pill{display:inline-block;padding:2px 8px;border-radius:20px;font-size:11px;border:1px solid var(--line);color:var(--muted)}
.pill.ok{border-color:var(--ok);color:var(--ok)}
.hint{font-size:12px;color:var(--muted);margin:8px 0 0}
</style></head>
<body><div class="wrap">
<h1>Today 插件控制台</h1>
<div id="banner" style="display:none;background:#3d1d1d;border:1px solid #f85149;color:#ffb3b3;padding:10px 12px;border-radius:8px;margin:0 0 16px;font-size:13px;white-space:pre-wrap"></div>
<p class="sub">today.ai → CPA · 插件 <code>` + prefix + `</code> · 端口 8317</p>

<div class="card">
  <h2>管理密钥</h2>
  <div class="row">
    <div><input id="secret" type="password" placeholder="CPA 的 management secret-key（即登录管理面板用的密钥）"></div>
    <div style="flex:0 0 110px"><button id="btnSecret" style="margin-top:0">保存</button></div>
  </div>
  <p class="hint">本页数据接口在 <code>/v0/management/</code> 下，需要管理密钥。只存在本机浏览器 localStorage。</p>
</div>

<div class="card">
  <h2>状态</h2>
  <div id="status"><span class="pill">加载中…</span></div>
</div>

<div class="card">
  <h2>登录账号</h2>
  <label>区域</label>
  <select id="realm"><option value="global">国际版 today.ai</option><option value="cn">国内版 todayai.cn</option></select>
  <label>方式一：粘贴浏览器 Cookie（推荐）</label>
  <textarea id="cookie" placeholder="__Secure-better-auth.session_token=xxxxxxxx  或整段 Cookie 头"></textarea>
  <p class="hint">浏览器登录 today.ai → F12 → Application → Cookies → today.ai → 复制 <code>__Secure-better-auth.session_token</code> 的值。</p>
  <button id="btnCookie">用 Cookie 登录</button>

  <hr style="border:0;border-top:1px solid var(--line);margin:22px 0">
  <label>方式二：邮箱验证码</label>
  <div class="row">
    <div><input id="email" placeholder="you@example.com"></div>
    <div style="flex:0 0 150px"><button id="btnSendOtp" style="margin-top:0">发送验证码</button></div>
  </div>
  <label>验证码</label>
  <input id="otp" placeholder="6 位验证码">
  <button id="btnOtp">用验证码登录</button>
</div>

<div class="card">
  <h2>账号清单</h2>
  <div id="accounts"><span class="pill">加载中…</span></div>
</div>

<div class="card">
  <h2>直连测试</h2>
  <label>模型</label>
  <select id="model"><option>today-deepseek-v4.1-flash</option><option>today-glm-5.3</option></select>
  <label>提示词</label>
  <textarea id="prompt" placeholder="用一句话介绍你自己">用一句话介绍你自己</textarea>
  <button id="btnChat">发送</button>
  <pre id="chatOut" style="margin-top:14px;display:none"></pre>
</div>
</div>
<script>
// 页面挂在 /v0/resource/plugins/today/console（不需要管理鉴权），
// 但数据接口只注册在 /v0/management/plugins/today/*（需要 management secret）。
const BASE = '/v0/management/plugins/today';
const out = (el, v) => { el.textContent = typeof v === 'string' ? v : JSON.stringify(v, null, 2); el.style.display='block'; };
const statusEl = document.getElementById('status');
const accountsEl = document.getElementById('accounts');
const chatOut = document.getElementById('chatOut');

const bannerEl = document.getElementById('banner');
function showError(msg) { bannerEl.textContent = msg; bannerEl.style.display='block'; }
function clearError() { bannerEl.style.display='none'; bannerEl.textContent=''; }

function secret() { return localStorage.getItem('cpa-mgmt-secret') || ''; }

async function api(path, opts) {
  const headers = {'Content-Type':'application/json', 'Authorization': 'Bearer ' + secret()};
  let r;
  try {
    r = await fetch(BASE + path, Object.assign({headers}, opts||{}));
  } catch (e) {
    showError('请求失败：' + e.message + ' | 目标：' + BASE + path);
    return {status: 0, data: null};
  }
  const text = await r.text();
  let data; try { data = JSON.parse(text); } catch { data = text; }
  if (r.status === 401 || r.status === 403) {
    showError('管理密钥不正确（HTTP ' + r.status + '）。请在上方「管理密钥」填入 CPA 的 management secret-key。');
  } else if (r.status === 404) {
    showError('接口不存在（HTTP 404）：' + BASE + path + ' | 插件可能未加载，检查 CPA 日志。');
  } else if (r.status >= 500) {
    showError('上游/插件错误（HTTP ' + r.status + '）：' + (typeof data === 'string' ? data : JSON.stringify(data)));
  } else if (r.status >= 200 && r.status < 300) {
    clearError();
  }
  return {status: r.status, data};
}

function saveSecret() {
  const v = document.getElementById('secret').value.trim();
  localStorage.setItem('cpa-mgmt-secret', v);
  loadStatus(); loadAccounts();
}

async function loadStatus() {
  const r = await api('/status');
  if (r.status !== 200) { statusEl.innerHTML = '<span class="pill">加载失败</span>'; return; }
  const d = r.data;
  statusEl.innerHTML = '<span class="pill ok">v' + d.version + '</span> '
    + '<span class="pill">默认模型 ' + d.default_model + '</span> '
    + '<span class="pill">超时 ' + d.timeout_seconds + 's</span> '
    + '<span class="pill">模型数 ' + d.models.length + '</span>';
}

async function loadAccounts() {
  const r = await api('/accounts');
  if (r.status !== 200) { accountsEl.innerHTML = '<span class="pill">加载失败</span>'; return; }
  if (!r.data.count) { accountsEl.innerHTML = '<span class="pill">暂无账号，先在上面登录一个</span>'; return; }
  accountsEl.innerHTML = r.data.accounts.map(a =>
    '<div style="padding:8px 0;border-bottom:1px solid var(--line)"><span class="pill ' + (a.healthy?'ok':'') + '">' +
    (a.healthy?'可用':'不可用') + '</span> <code>' + a.name + '</code></div>').join('');
}

document.getElementById('btnCookie').onclick = async () => {
  const cookie = document.getElementById('cookie').value.trim();
  if (!cookie) return alert('请粘贴 Cookie');
  const btn = document.getElementById('btnCookie'); btn.disabled = true;
  const r = await api('/accounts/login', {method:'POST', body: JSON.stringify({cookie, realm: document.getElementById('realm').value})});
  btn.disabled = false;
  if (r.status === 401 || r.status === 403) return alert('管理密钥不正确（401/403）。请在上方「管理密钥」里填入 CPA 的 management secret-key。');
  alert(r.status + ' ' + JSON.stringify(r.data, null, 2));
  loadAccounts();
};

document.getElementById('btnSendOtp').onclick = async () => {
  const email = document.getElementById('email').value.trim();
  if (!email) return alert('请填邮箱');
  const btn = document.getElementById('btnSendOtp'); btn.disabled = true;
  const r = await api('/accounts/login', {method:'POST', body: JSON.stringify({email, realm: document.getElementById('realm').value})});
  btn.disabled = false;
  alert(JSON.stringify(r.data));
};

document.getElementById('btnOtp').onclick = async () => {
  const email = document.getElementById('email').value.trim();
  const otp = document.getElementById('otp').value.trim();
  if (!email || !otp) return alert('邮箱和验证码都要填');
  const btn = document.getElementById('btnOtp'); btn.disabled = true;
  const r = await api('/accounts/login', {method:'POST', body: JSON.stringify({email, otp, realm: document.getElementById('realm').value})});
  btn.disabled = false;
  alert(JSON.stringify(r.data));
  loadAccounts();
};

document.getElementById('btnChat').onclick = async () => {
  const prompt = document.getElementById('prompt').value.trim();
  const model = document.getElementById('model').value;
  if (!prompt) return;
  const btn = document.getElementById('btnChat'); btn.disabled = true;
  out(chatOut, '等待上游回复…');
  const r = await api('/chat', {method:'POST', body: JSON.stringify({prompt, model})});
  btn.disabled = false;
  out(chatOut, r.data);
};

document.getElementById('btnSecret').onclick = saveSecret;
document.getElementById('secret').value = localStorage.getItem('cpa-mgmt-secret') || '';
loadStatus(); loadAccounts();
</script>
</body></html>`
}
