'use strict';

const $ = (id) => document.getElementById(id);
const TOKEN_KEY = 'wxbot.token';

function getToken() {
  try { return sessionStorage.getItem(TOKEN_KEY) || ''; } catch { return ''; }
}
function setToken(t) {
  try { t ? sessionStorage.setItem(TOKEN_KEY, t) : sessionStorage.removeItem(TOKEN_KEY); } catch { /* 无存储时仅本次有效 */ }
  memToken = t;
}
let memToken = getToken();

// api 发起带 token 的请求，返回 {status, type, body}
async function api(method, path, body) {
  const opts = { method, headers: { Authorization: 'Bearer ' + memToken } };
  if (body instanceof FormData) {
    opts.body = body;
  } else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = typeof body === 'string' ? body : JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
  const type = resp.headers.get('Content-Type') || '';
  const data = type.includes('json') ? await resp.text() : await resp.blob();
  return { status: resp.status, type, body: data };
}

// ---------- 登录 ----------

async function login(token) {
  memToken = token;
  const r = await api('GET', '/api/v1/ping');
  if (r.status !== 200) throw new Error('Token 无效');
  setToken(token);
  showApp();
}

function showApp() {
  $('login').hidden = true;
  $('app').hidden = false;
}
function showLogin(msg) {
  setToken('');
  $('app').hidden = true;
  $('login').hidden = false;
  $('login-err').textContent = msg || '';
  $('token-input').focus();
}

$('login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('login-err').textContent = '';
  try { await login($('token-input').value.trim()); }
  catch (err) { $('login-err').textContent = err.message; }
});
$('logout').addEventListener('click', () => showLogin());

// ---------- 标签页 ----------

document.querySelectorAll('[role=tab]').forEach((tab) => {
  tab.addEventListener('click', () => {
    document.querySelectorAll('[role=tab]').forEach((t) => t.setAttribute('aria-selected', String(t === tab)));
    document.querySelectorAll('[data-panel]').forEach((p) => { p.hidden = p.dataset.panel !== tab.dataset.tab; });
  });
});

// ---------- 输出 ----------

let previewURL = null;
function show(r, label) {
  const ok = r.status >= 200 && r.status < 300;
  $('status').textContent = `${label} · HTTP ${r.status}`;
  $('status').className = 'status ' + (ok ? 'ok' : 'bad');
  if (previewURL) { URL.revokeObjectURL(previewURL); previewURL = null; }
  $('preview').replaceChildren();

  if (typeof r.body === 'string') {
    try { $('output').textContent = JSON.stringify(JSON.parse(r.body), null, 2); }
    catch { $('output').textContent = r.body; }
    if (r.status === 401) showLogin('登录已失效，请重新登录');
    return;
  }
  // 二进制素材：图片直接预览，其他提供下载
  previewURL = URL.createObjectURL(r.body);
  $('output').textContent = `二进制内容 ${r.type || 'unknown'}，${r.body.size} 字节`;
  if (r.type.startsWith('image/')) {
    const img = document.createElement('img');
    img.src = previewURL;
    img.alt = '素材预览';
    $('preview').append(img);
  }
  const a = document.createElement('a');
  a.href = previewURL;
  a.download = $('m-mid').value.trim() || 'material';
  a.textContent = '下载文件';
  $('preview').append(a);
}

const num = (id) => Number($(id).value || 0);
const need = (id, name) => {
  const v = $(id).value.trim();
  if (!v) throw new Error(`请填写 ${name}`);
  return encodeURIComponent(v);
};
const page = (p) => `offset=${num(p + '-offset')}&count=${num(p + '-count')}`;

function fileForm(withType) {
  const f = $('m-file').files[0];
  if (!f) throw new Error('请选择文件');
  const fd = new FormData();
  fd.append('media', f);
  if (withType) {
    fd.append('type', $('m-uptype').value);
    fd.append('title', $('m-title').value);
    fd.append('introduction', $('m-intro').value);
  }
  return fd;
}

const DRAFT_TEMPLATE = {
  articles: [{
    article_type: 'news',
    title: '标题',
    author: '',
    digest: '',
    content: '<p>正文 HTML</p>',
    content_source_url: '',
    thumb_media_id: '先上传 thumb/image 永久素材，填入 media_id',
    need_open_comment: 0,
    only_fans_can_comment: 0,
  }],
};

const actions = {
  draftList: () => api('GET', `/api/v1/drafts?${page('d')}&no_content=${$('d-nocontent').checked}`),
  draftCount: () => api('GET', '/api/v1/drafts/count'),
  draftGet: () => api('GET', `/api/v1/drafts/${need('d-mid', 'media_id')}`),
  draftPublish: () => api('POST', `/api/v1/drafts/${need('d-mid', 'media_id')}/publish`),
  draftDelete: () => confirmed('删除该草稿') && api('DELETE', `/api/v1/drafts/${need('d-mid', 'media_id')}`),
  draftTemplate: () => { $('d-body').value = JSON.stringify(DRAFT_TEMPLATE, null, 2); },
  draftAdd: () => api('POST', '/api/v1/drafts', $('d-body').value),
  draftUpdate: () => {
    const body = JSON.parse($('d-body').value);
    body.index = num('d-index');
    return api('PUT', `/api/v1/drafts/${need('d-mid', 'media_id')}`, body);
  },

  materialList: () => api('GET', `/api/v1/materials?type=${$('m-type').value}&${page('m')}`),
  materialCount: () => api('GET', '/api/v1/materials/count'),
  materialAdd: () => api('POST', '/api/v1/materials', fileForm(true)),
  uploadImage: () => api('POST', '/api/v1/materials/images', fileForm(false)),
  materialGet: () => api('GET', `/api/v1/materials/${need('m-mid', 'media_id')}`),
  materialDelete: () => confirmed('删除该素材') && api('DELETE', `/api/v1/materials/${need('m-mid', 'media_id')}`),

  publishList: () => api('GET', `/api/v1/publish?${page('p')}&no_content=${$('p-nocontent').checked}`),
  publishGet: () => api('GET', `/api/v1/publish/${need('p-pid', 'publish_id')}`),
  articleGet: () => api('GET', `/api/v1/articles/${need('p-aid', 'article_id')}`),
  articleDelete: () => confirmed('删除已发布文章') && api('DELETE', `/api/v1/articles/${need('p-aid', 'article_id')}?index=${num('p-index')}`),
};

function confirmed(what) {
  return window.confirm(`确认${what}？此操作不可撤销。`);
}

document.addEventListener('click', async (e) => {
  const btn = e.target.closest('[data-act]');
  if (!btn) return;
  const name = btn.dataset.act;
  btn.disabled = true;
  try {
    const r = await actions[name]();
    if (r) show(r, btn.textContent);
  } catch (err) {
    $('status').textContent = btn.textContent;
    $('status').className = 'status bad';
    $('output').textContent = '错误：' + err.message;
  } finally {
    btn.disabled = false;
  }
});

// 启动：有已保存的 token 则校验后直接进入
if (memToken) {
  login(memToken).catch(() => showLogin());
} else {
  showLogin();
}
