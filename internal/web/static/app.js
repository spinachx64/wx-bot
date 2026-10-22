'use strict';

// ---------- 基础工具 ----------

const $ = (sel, el = document) => el.querySelector(sel);
const view = $('#view');
const TOKEN_KEY = 'wxbot.token';
const PAGE_SIZE = 20;
const MATERIAL_PAGE_SIZE = 18;

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
// 微信图床同时支持 https，统一升级，避免 https 页面里的混合内容
const https = (u) => String(u || '').replace(/^http:\/\/(\w+\.)?qpic\.cn/, (m) => m.replace('http:', 'https:'));

function fmtTime(sec) {
  if (!sec) return '';
  const d = new Date(sec * 1000);
  const pad = (n) => String(n).padStart(2, '0');
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return `今天 ${hm}`;
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}月${d.getDate()}日 ${hm}`;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

const fmtSize = (n) => (n < 1024 * 1024 ? `${Math.ceil(n / 1024)}KB` : `${(n / 1024 / 1024).toFixed(1)}MB`);

// ---------- 登录令牌 ----------
// 默认只存 sessionStorage（关掉标签页即退出），勾选「保持登录」才存 localStorage

function storages() {
  const out = [];
  for (const k of ['sessionStorage', 'localStorage']) {
    try { if (window[k]) out.push(window[k]); } catch { /* 隐私模式等 */ }
  }
  return out;
}
const tokenStore = {
  get() {
    for (const s of storages()) { try { const v = s.getItem(TOKEN_KEY); if (v) return v; } catch {} }
    return '';
  },
  set(t, remember) {
    this.clear();
    const [sess, local] = [storages().find((s) => s === window.sessionStorage), storages().find((s) => s === window.localStorage)];
    try { (remember ? local : sess)?.setItem(TOKEN_KEY, t); } catch {}
  },
  clear() { for (const s of storages()) { try { s.removeItem(TOKEN_KEY); } catch {} } },
};
let token = tokenStore.get();

// ---------- 接口 ----------

// 微信错误码翻译成用户能看懂的话，其他情况保留原始信息方便排查
const ERRORS = {
  40007: '素材不存在，可能已经被删除',
  40164: '服务器 IP 不在公众号白名单里，请到公众号后台添加',
  45009: '今天的调用次数已用完，请明天再试',
  45011: '操作太频繁，请稍后再试',
  48001: '当前公众号没有这项能力的权限（通常需要完成微信认证）',
  53503: '这篇草稿没有通过发布检查，请检查内容后重试',
  53505: '请先在公众号后台手动保存一次这篇草稿，再来发布',
};

class ApiError extends Error {
  constructor(status, code, msg) { super(msg); this.status = status; this.code = code; }
}

function toError(status, data) {
  const code = data?.errcode;
  if (ERRORS[code]) return new ApiError(status, code, ERRORS[code]);
  const raw = (data?.errmsg || '').replace(/\s*(hint|rid):.*$/, '');
  return new ApiError(status, code, `${raw || '请求失败'}（${code ?? status}）`);
}

async function api(method, path, body) {
  const opts = { method, headers: { Authorization: 'Bearer ' + token } };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  let resp;
  try { resp = await fetch(path, opts); } catch { throw new ApiError(0, -1, '网络连接失败，请检查网络后重试'); }
  const text = await resp.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch {}
  if (resp.status === 401) { signOut('登录已失效，请重新登录'); throw new ApiError(401, -1, '登录已失效'); }
  if (!resp.ok) throw toError(resp.status, data);
  return data;
}

// 上传用 XHR，才能拿到上传进度
function uploadFile(path, fields, onProgress) {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    for (const [k, v] of Object.entries(fields)) fd.append(k, v);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', path);
    xhr.setRequestHeader('Authorization', 'Bearer ' + token);
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total);
    xhr.onerror = () => reject(new ApiError(0, -1, '网络连接失败，请检查网络后重试'));
    xhr.onload = () => {
      let data = {};
      try { data = JSON.parse(xhr.responseText); } catch {}
      if (xhr.status === 401) { signOut('登录已失效，请重新登录'); return reject(new ApiError(401, -1, '登录已失效')); }
      if (xhr.status === 413) return reject(new ApiError(413, -1, '文件太大'));
      xhr.status >= 200 && xhr.status < 300 ? resolve(data) : reject(toError(xhr.status, data));
    };
    xhr.send(fd);
  });
}

// ---------- 反馈：轻提示、确认框 ----------

function toast(msg, isErr) {
  const el = document.createElement('div');
  el.className = 'toast' + (isErr ? ' err' : '');
  el.setAttribute('role', isErr ? 'alert' : 'status');
  el.textContent = msg;
  $('#toasts').append(el);
  setTimeout(() => el.remove(), isErr ? 5000 : 2600);
}

// confirmDialog 用原生 <dialog>，Esc 和遮罩点击都等于取消
function confirmDialog({ title, body, ok = '确定', danger = false }) {
  const dlg = $('#confirm');
  $('#confirm-title').textContent = title;
  $('#confirm-body').textContent = body || '';
  const okBtn = $('#confirm-ok');
  okBtn.textContent = ok;
  okBtn.className = 'btn ' + (danger ? 'danger solid' : 'primary');
  dlg.returnValue = '';
  dlg.showModal();
  okBtn.focus();
  return new Promise((resolve) => {
    dlg.addEventListener('close', () => resolve(dlg.returnValue === 'ok'), { once: true });
  });
}
$('#confirm').addEventListener('click', (e) => { if (e.target === e.currentTarget) e.currentTarget.close('cancel'); });

// busy 执行期间禁用按钮并显示文案，防止重复点击
async function busy(btn, text, fn) {
  const old = btn.textContent;
  btn.disabled = true;
  btn.setAttribute('aria-busy', 'true');
  btn.textContent = text;
  try { return await fn(); } finally {
    btn.disabled = false;
    btn.removeAttribute('aria-busy');
    btn.textContent = old;
  }
}

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); toast('已复制'); }
  catch { toast('复制失败，请手动选择复制', true); }
}

// ---------- 登录 ----------

async function signIn(t, remember) {
  token = t;
  const me = await api('GET', '/api/v1/ping');
  tokenStore.set(t, remember);
  $('#account-name').textContent = me.tenant ? `${me.tenant}` : '';
  $('#account-name').title = me.appid || '';
  $('#login').hidden = true;
  $('#app').hidden = false;
  route();
}

function signOut(msg) {
  tokenStore.clear();
  token = '';
  closeDraftPreview();
  $('#app').hidden = true;
  $('#login').hidden = false;
  $('#login-err').textContent = msg || '';
  $('#token-input').value = '';
  $('#token-input').focus();
}

$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = $('#login-form button[type=submit]');
  $('#login-err').textContent = '';
  const t = $('#token-input').value.trim();
  try {
    await busy(btn, '登录中…', () => signIn(t, $('#remember').checked));
  } catch (err) {
    if (err.status !== 401) $('#login-err').textContent = err.message;
    else $('#login-err').textContent = '令牌不正确，请检查后重试';
  }
});

$('#logout').addEventListener('click', async () => {
  if (await confirmDialog({ title: '退出登录？', body: '退出后需要重新输入访问令牌。', ok: '退出' })) signOut();
});

// ---------- 路由 ----------
// #/drafts                草稿列表
// #/drafts/<media_id>     草稿预览与发布
// #/materials/<type>      素材（image / video / voice / news）

let routeSeq = 0; // 快速切换页面时丢弃过期请求的结果

function route() {
  if (!token) return;
  closeDraftPreview();
  const seq = ++routeSeq;
  const parts = location.hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent);
  const [page, arg] = parts;
  document.querySelectorAll('[data-nav]').forEach((a) => {
    a.toggleAttribute('aria-current', a.dataset.nav === (page || 'drafts'));
    if (a.dataset.nav === (page || 'drafts')) a.setAttribute('aria-current', 'page');
  });
  window.scrollTo(0, 0);
  const alive = () => seq === routeSeq;
  if (page === 'materials') return renderMaterials(MATERIAL_TYPES[arg] ? arg : 'image', alive);
  if (page === 'drafts' && arg) return renderDraft(arg, alive);
  return renderDrafts(alive);
}
window.addEventListener('hashchange', route);

function setTitle(t) { document.title = t ? `${t} · 酷卡公众号助手` : '酷卡公众号助手'; }

// 页面渲染失败时的统一错误态，带重试
function renderError(err, retry) {
  view.innerHTML = `<div class="empty"><div class="icon" aria-hidden="true">⚠️</div>
    <p>${esc(err.message)}</p><button class="btn" type="button">重试</button></div>`;
  $('.btn', view).addEventListener('click', retry);
}

// 统一包一层：过期页面不渲染，401 已经跳回登录，其他错误给重试
async function guard(alive, fn) {
  try { await fn(); } catch (err) {
    if (!alive() || err.status === 401) return;
    renderError(err, route);
  }
}

// pager 返回分页条元素，只有一页时返回 null
function pager(offset, total, go, pageSize = PAGE_SIZE) {
  if (total <= pageSize) return null;
  const pages = Math.ceil(total / pageSize);
  const cur = Math.floor(offset / pageSize) + 1;
  const el = document.createElement('nav');
  el.className = 'pager';
  el.setAttribute('aria-label', '分页');
  el.innerHTML = `<button class="btn sm" type="button" data-pg="-1" ${cur <= 1 ? 'disabled' : ''}>上一页</button>
    <span>第 ${cur} / ${pages} 页</span>
    <button class="btn sm" type="button" data-pg="1" ${cur >= pages ? 'disabled' : ''}>下一页</button>`;
  el.addEventListener('click', (e) => {
    const b = e.target.closest('[data-pg]');
    if (b) go(offset + Number(b.dataset.pg) * pageSize);
  });
  return el;
}

const skeletonCards = (n) => Array.from({ length: n }, () =>
  `<div class="draft-card" aria-hidden="true"><div class="cover skeleton"></div>
   <div class="body"><div class="skeleton" style="height:20px;width:60%"></div>
   <div class="skeleton" style="height:14px;width:90%;margin-top:10px"></div></div></div>`).join('');

// ---------- 草稿箱 ----------

let draftOffset = 0;

function renderDrafts(alive) {
  setTitle('草稿箱');
  view.innerHTML = `<div class="page-head"><div><h1>草稿箱</h1><p class="sub">加载中…</p></div></div>
    <div class="list" aria-busy="true">${skeletonCards(3)}</div>`;
  return guard(alive, async () => {
    const data = await api('GET', `/api/v1/drafts?offset=${draftOffset}&count=${PAGE_SIZE}&no_content=true`);
    if (!alive()) return;
    const total = data.total_count || 0;
    const items = data.item || [];
    if (!items.length && draftOffset > 0) { draftOffset = 0; return route(); }

    view.innerHTML = `<div class="page-head">
        <div><span class="eyebrow">YOUR CONTENT, READY TO GO</span><h1>草稿箱<span class="count-pill">${total}</span></h1><p class="sub">${total ? `共 ${total} 篇草稿 · 点击卡片，直接预览` : '暂无草稿'}</p></div>
        <div class="page-actions"><button class="btn" type="button" id="refresh">刷新</button></div>
      </div><div class="list"></div>`;
    $('#refresh').addEventListener('click', route);
    const list = $('.list', view);

    if (!items.length) {
      list.innerHTML = `<div class="empty"><div class="icon" aria-hidden="true">📝</div>
        <p>还没有草稿</p><p>用 wxctl 把 Markdown 文章提交到草稿箱，或在公众号后台新建</p></div>`;
      return;
    }
    list.innerHTML = items.map((it) => {
      const news = it.content?.news_item || [];
      const a = news[0] || {};
      return `<a class="draft-card" data-draft="${esc(it.media_id)}" href="#/drafts/${encodeURIComponent(it.media_id)}">
        <div class="cover"><span class="cover-label">草稿 · DRAFT</span>${a.thumb_url ? `<img src="${esc(https(a.thumb_url))}" alt="" loading="lazy">` : ''}</div>
        <div class="body">
          <h3>${esc(a.title || '（无标题）')}</h3>
          ${a.digest ? `<p class="digest">${esc(a.digest)}</p>` : ''}
          <div class="meta">
            <span>更新于 ${esc(fmtTime(it.update_time))}</span>
            ${a.author ? `<span>${esc(a.author)}</span>` : ''}
            ${news.length > 1 ? `<span class="badge accent">共 ${news.length} 篇</span>` : ''}
          </div>
          <span class="preview-link">预览文章 <span aria-hidden="true">↗</span></span>
        </div></a>`;
    }).join('');
    list.addEventListener('click', (e) => {
      const card = e.target.closest('[data-draft]');
      if (!card || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      openDraftPreview(card.dataset.draft);
    });
    const pg = pager(draftOffset, total, (o) => { draftOffset = o; route(); });
    if (pg) view.append(pg);
  });
}

// ---------- 草稿详情：手机预览 + 发布 ----------

// 发布任务按草稿记在本页内存里，返回详情页时继续显示进度
const publishJobs = new Map();

const PUBLISH_STATUS = {
  0: ['ok', '发布成功'], 1: ['busy', '发布中，通常需要几十秒'], 2: ['bad', '原创声明失败'], 3: ['bad', '发布失败'],
  4: ['bad', '平台审核未通过'], 5: ['bad', '发布成功后被删除'], 6: ['bad', '发布成功后被封禁'],
};

// 正文里的微信图片升级为 https；页面已禁止 referrer，图床防盗链不会拦截
const fixContent = (html) => String(html || '').replace(/(src=["'])http:\/\/((?:\w+\.)?qpic\.cn)/g, '$1https://$2');

let previewSeq = 0;
const previewDialog = $('#draft-preview');
function closeDraftPreview() {
  ++previewSeq;
  if (previewDialog.open) previewDialog.close();
  $('#preview-content').replaceChildren();
  document.body.classList.remove('preview-open');
}
function openDraftPreview(mediaId) {
  const seq = ++previewSeq;
  if (!previewDialog.open) previewDialog.showModal();
  document.body.classList.add('preview-open');
  renderDraft(mediaId, () => previewDialog.open && seq === previewSeq, $('#preview-content'));
}
$('#close-preview').addEventListener('click', closeDraftPreview);
previewDialog.addEventListener('click', (e) => { if (e.target === previewDialog) closeDraftPreview(); });
previewDialog.addEventListener('close', () => {
  ++previewSeq;
  document.body.classList.remove('preview-open');
  $('#preview-content').replaceChildren();
  if (!location.hash.replace(/^#\/?/, '').split('/')[1]) setTitle('草稿箱');
});

function renderDraft(mediaId, alive, view = document.querySelector('#view')) {
  const guard = async (alive, fn) => {
    try { await fn(); } catch (err) {
      if (!alive() || err.status === 401) return;
      view.innerHTML = `<div class="empty"><p>${esc(err.message)}</p><button class="btn" type="button">重新加载</button></div>`;
      $('button', view).onclick = () => renderDraft(mediaId, alive, view);
    }
  };
  setTitle('草稿预览');
  view.innerHTML = `<a class="back" href="#/drafts">‹ 返回草稿箱</a>
    <div class="detail"><div class="phone skeleton" style="height:560px"></div><div class="side"></div></div>`;
  return guard(alive, async () => {
    const data = await api('GET', `/api/v1/drafts/${encodeURIComponent(mediaId)}`);
    if (!alive()) return;
    const news = data.news_item || [];
    if (!news.length) throw new ApiError(404, -1, '这篇草稿没有内容');
    let idx = 0;

    view.innerHTML = `<a class="back" href="#/drafts">‹ 返回草稿箱</a>
      <div class="detail">
        <div>
          ${news.length > 1 ? `<div class="tabs" role="tablist" aria-label="草稿中的文章">${news.map((a, i) =>
            `<a href="#" role="tab" data-idx="${i}">${i + 1}. ${esc(a.title || '无标题')}</a>`).join('')}</div>` : ''}
          <article class="phone" aria-label="手机预览">
            <div class="phone-bar">预览</div>
            <header class="article-head"><h1></h1><div class="byline"></div></header>
            <iframe title="文章正文预览" sandbox="allow-same-origin" referrerpolicy="no-referrer"></iframe>
          </article>
        </div>
        <aside class="side">
          <section class="panel pub">
            <h2>发布</h2>
            <div id="pub-status"></div>
            <button class="btn primary" type="button" id="publish">发布这篇草稿</button>
            <p class="hint">发布后粉丝可以看到，无法撤回。发布成功后，草稿会从草稿箱移走。</p>
          </section>
          <section class="panel">
            <h2>信息</h2>
            <dl class="kv" id="info"></dl>
            <a class="btn sm" id="open-wx" target="_blank" rel="noopener noreferrer" hidden>在微信中预览</a>
            <button class="btn sm ghost" type="button" id="copy-id">复制草稿 ID</button>
          </section>
          <section class="panel">
            <button class="btn danger" type="button" id="delete">删除草稿</button>
          </section>
        </aside>
      </div>`;
    setTitle(news[0].title);

    const frame = $('iframe', view);
    const show = (i) => {
      idx = i;
      $('.phone', view).scrollTop = 0;
      const a = news[i];
      $('.article-head h1', view).textContent = a.title || '（无标题）';
      $('.article-head .byline', view).innerHTML =
        [a.author].filter(Boolean).map((s) => `<span>${esc(s)}</span>`).join('');
      view.querySelectorAll('[data-idx]').forEach((t) =>
        t.setAttribute('aria-selected', String(Number(t.dataset.idx) === i)));
      view.querySelectorAll('[data-idx]').forEach((t) =>
        Number(t.dataset.idx) === i ? t.setAttribute('aria-current', 'page') : t.removeAttribute('aria-current'));
      frame.srcdoc = `<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer">
        <link rel="stylesheet" href="preview.css"></head><body>${fixContent(a.content)}</body></html>`;
      $('#info').innerHTML = `
        <dt>作者</dt><dd>${esc(a.author || '未填写')}</dd>
        <dt>摘要</dt><dd>${esc(a.digest || '未填写')}</dd>
        <dt>评论</dt><dd>${a.need_open_comment ? (a.only_fans_can_comment ? '已开启，仅粉丝可评论' : '已开启') : '未开启'}</dd>
        <dt>原文链接</dt><dd>${a.content_source_url ? esc(a.content_source_url) : '无'}</dd>`;
      const wx = $('#open-wx');
      wx.hidden = !a.url;
      if (a.url) wx.href = a.url;
    };

    // iframe 高度跟随内容（图片加载后也会变高），整页只有一个滚动条
    frame.addEventListener('load', () => {
      const doc = frame.contentDocument;
      if (!doc) return;
      const fit = () => { frame.style.height = doc.documentElement.scrollHeight + 'px'; };
      fit();
      const observer = new ResizeObserver(fit);
      observer.observe(doc.body);
      frame.addEventListener('load', () => observer.disconnect(), { once: true });
    });

    view.querySelectorAll('[data-idx]').forEach((t) =>
      t.addEventListener('click', (e) => { e.preventDefault(); show(Number(t.dataset.idx)); }));
    show(0);

    $('#copy-id').addEventListener('click', () => copyText(mediaId));
    $('#delete').addEventListener('click', async (e) => {
      const ok = await confirmDialog({
        title: '删除这篇草稿？', danger: true, ok: '删除',
        body: `「${news[0].title || '无标题'}」${news.length > 1 ? `等 ${news.length} 篇文章` : ''}会被永久删除，无法恢复。`,
      });
      if (!ok) return;
      try {
        await busy(e.target, '删除中…', () => api('DELETE', `/api/v1/drafts/${encodeURIComponent(mediaId)}`));
        toast('草稿已删除');
        closeDraftPreview();
        if (location.hash === '#/drafts' || !location.hash) route();
        else location.hash = '#/drafts';
      } catch (err) { toast(err.message, true); }
    });
    $('#publish').addEventListener('click', async (e) => {
      const ok = await confirmDialog({
        title: '确认发布？', ok: '发布',
        body: `「${news[0].title || '无标题'}」发布后会出现在公众号主页，粉丝可以看到，无法撤回。`,
      });
      if (!ok) return;
      try {
        const res = await busy(e.target, '提交中…', () =>
          api('POST', `/api/v1/drafts/${encodeURIComponent(mediaId)}/publish`));
        publishJobs.set(mediaId, { id: String(res.publish_id) });
        toast('已提交发布');
        watchPublish(mediaId, alive);
      } catch (err) { toast(err.message, true); }
    });
    if (publishJobs.has(mediaId)) watchPublish(mediaId, alive);
  });
}

// watchPublish 每 3 秒查询一次发布状态，最多 2 分钟；离开页面就停止
async function watchPublish(mediaId, alive) {
  const job = publishJobs.get(mediaId);
  const box = $('#pub-status');
  const btn = $('#publish');
  if (!box || !job) return;
  const paint = (cls, text, extra = '') => {
    box.innerHTML = `<p class="status-line"><span class="dot ${cls}"></span>${esc(text)}</p>${extra}`;
  };
  btn.disabled = true;
  for (let i = 0; i < 40 && alive(); i++) {
    let st;
    try {
      st = await api('GET', `/api/v1/publish/${encodeURIComponent(job.id)}`);
    } catch (err) {
      if (!alive()) return;
      // 有的公众号没有查询权限，但发布本身已经提交
      paint('busy', '已提交发布，暂时查不到进度', `<p class="hint">${esc(err.message)}。可以稍后到公众号后台查看结果。</p>`);
      return;
    }
    if (!alive()) return;
    const [cls, text] = PUBLISH_STATUS[st.publish_status] || ['busy', `状态 ${st.publish_status}`];
    const urls = (st.article_detail?.item || []).map((it) => it.article_url).filter(Boolean);
    paint(cls, text, urls.map((u) =>
      `<p class="hint"><a class="link" href="${esc(u)}" target="_blank" rel="noopener noreferrer">查看已发布的文章 ›</a></p>`).join(''));
    if (st.publish_status !== 1) {
      if (st.publish_status === 0) btn.hidden = true;
      else btn.disabled = false;
      return;
    }
    await new Promise((r) => setTimeout(r, 3000));
  }
  if (alive()) paint('busy', '仍在发布中', '<p class="hint">可以稍后刷新页面，或到公众号后台查看结果。</p>');
}

// ---------- 素材库 ----------

const MATERIAL_TYPES = {
  image: { label: '图片', count: 'image_count', accept: 'image/png,image/jpeg,image/gif,image/bmp', max: 10 << 20, hint: 'jpg / png / gif / bmp，单张不超过 10MB' },
  video: { label: '视频', count: 'video_count', accept: 'video/mp4', max: 10 << 20, hint: 'mp4，不超过 10MB，需要填写标题' },
  voice: { label: '语音', count: 'voice_count', accept: 'audio/mpeg,audio/amr,audio/wav,audio/x-ms-wma', max: 2 << 20, hint: 'mp3 / amr / wav / wma，不超过 2MB' },
  news: { label: '图文', count: 'news_count' },
};

const materialOffset = {};
let materialCounts = null; // 总数缓存，上传 / 删除后清空

function renderMaterials(type, alive) {
  const meta = MATERIAL_TYPES[type];
  const offset = materialOffset[type] || 0;
  setTitle(`素材库 · ${meta.label}`);
  const statCards = (counts) => Object.entries(MATERIAL_TYPES).map(([k, m]) =>
    `<a class="stat" href="#/materials/${k}" aria-pressed="${k === type}">
       <div class="label">${m.label}</div><div class="num">${counts ? (counts[m.count] ?? 0) : '–'}</div></a>`).join('');

  view.innerHTML = `<div class="page-head"><div><h1>素材库</h1><p class="sub">永久素材，可在文章中直接使用</p></div></div>
    <div class="stats">${statCards(materialCounts)}</div>
    <div id="m-upload"></div>
    <div id="m-list" aria-busy="true"><div class="grid">${Array.from({ length: MATERIAL_PAGE_SIZE }, () =>
      '<div class="tile"><div class="thumb skeleton"></div><div class="cap">&nbsp;</div></div>').join('')}</div></div>`;

  if (meta.accept) mountUploader($('#m-upload'), type);
  else $('#m-upload').innerHTML = '<p class="notice">图文素材是旧版接口的产物，只能查看和删除，新文章请使用草稿箱。</p>';

  return guard(alive, async () => {
    const [counts, data] = await Promise.all([
      materialCounts ? Promise.resolve(materialCounts) : api('GET', '/api/v1/materials/count'),
      api('GET', `/api/v1/materials?type=${type}&offset=${offset}&count=${MATERIAL_PAGE_SIZE}`),
    ]);
    if (!alive()) return;
    materialCounts = counts;
    $('.stats', view).innerHTML = statCards(counts);
    const items = data.item || [];
    const total = data.total_count ?? counts[meta.count] ?? 0;
    if (!items.length && offset > 0) { materialOffset[type] = 0; return route(); }

    const box = $('#m-list');
    box.removeAttribute('aria-busy');
    if (!items.length) {
      box.innerHTML = `<div class="empty"><div class="icon" aria-hidden="true">🗂️</div><p>还没有${meta.label}素材</p>
        ${meta.accept ? '<p>把文件拖到上方区域，或点击上传</p>' : ''}</div>`;
      return;
    }
    box.innerHTML = type === 'image'
      ? `<div class="grid">${items.map((it, i) => `<div class="tile">
          <button class="thumb" type="button" data-view="${i}" aria-label="查看 ${esc(it.name)}">
            <img src="${esc(https(it.url))}" alt="" loading="lazy"></button>
          <div class="cap"><div class="name" title="${esc(it.name)}">${esc(it.name || '未命名')}</div>
            <div class="time">${esc(fmtTime(it.update_time))}</div></div>
          <button class="del" type="button" data-del="${i}">删除</button></div>`).join('')}</div>`
      : `<div class="list">${items.map((it, i) => {
          const title = type === 'news' ? (it.content?.news_item?.[0]?.title || '无标题') : (it.name || '未命名');
          return `<div class="row-item"><span class="name" title="${esc(title)}">${esc(title)}</span>
            <span class="muted">${esc(fmtTime(it.update_time || it.content?.update_time))}</span>
            <button class="btn sm" type="button" data-copy="${i}">复制 media_id</button>
            <button class="btn sm danger" type="button" data-del="${i}">删除</button></div>`;
        }).join('')}</div>`;

    const nameOf = (it) => it.name || it.content?.news_item?.[0]?.title || '这个素材';
    box.addEventListener('click', async (e) => {
      const v = e.target.closest('[data-view]');
      const d = e.target.closest('[data-del]');
      const c = e.target.closest('[data-copy]');
      if (v) openViewer(items[v.dataset.view]);
      if (c) copyText(items[c.dataset.copy].media_id);
      if (d) {
        const it = items[d.dataset.del];
        if (await deleteMaterial(it.media_id, nameOf(it), d)) route();
      }
    });
    const pg = pager(offset, total, (o) => { materialOffset[type] = o; route(); }, MATERIAL_PAGE_SIZE);
    if (pg) view.append(pg);
  });
}

async function deleteMaterial(mediaId, name, btn) {
  const ok = await confirmDialog({
    title: '删除这个素材？', danger: true, ok: '删除',
    body: `「${name}」会从素材库永久删除。已发布文章里用到的图片不受影响。`,
  });
  if (!ok) return false;
  try {
    await busy(btn, '删除中…', () => api('DELETE', `/api/v1/materials/${encodeURIComponent(mediaId)}`));
    materialCounts = null;
    toast('已删除');
    return true;
  } catch (err) { toast(err.message, true); return false; }
}

// openViewer 大图查看，可复制链接、删除
function openViewer(it) {
  const dlg = $('#viewer');
  dlg.innerHTML = `<div class="viewer-body">
      <img src="${esc(https(it.url))}" alt="${esc(it.name)}">
      <div class="viewer-foot"><span class="name">${esc(it.name || '未命名')} · ${esc(fmtTime(it.update_time))}</span>
        <div class="page-actions">
          <button class="btn sm" type="button" data-a="url">复制图片链接</button>
          <button class="btn sm" type="button" data-a="id">复制 media_id</button>
          <button class="btn sm danger" type="button" data-a="del">删除</button>
          <button class="btn sm" type="button" data-a="close">关闭</button>
        </div></div></div>`;
  dlg.onclick = async (e) => {
    if (e.target === dlg) return dlg.close();
    const a = e.target.closest('[data-a]')?.dataset.a;
    if (a === 'close') dlg.close();
    if (a === 'url') copyText(https(it.url));
    if (a === 'id') copyText(it.media_id);
    if (a === 'del') {
      dlg.close();
      if (await deleteMaterial(it.media_id, it.name || '这张图片', e.target)) route();
    }
  };
  dlg.showModal();
}

// mountUploader 点击或拖拽上传，支持多选，逐个上传并显示进度
function mountUploader(box, type) {
  const meta = MATERIAL_TYPES[type];
  box.innerHTML = `
    ${type === 'video' ? `<div class="panel" style="margin-bottom:12px">
      <label for="v-title" style="margin-top:0">视频标题（必填）</label><input id="v-title" maxlength="64">
      <label for="v-intro">视频简介</label><input id="v-intro" maxlength="120"></div>` : ''}
    <label class="dropzone" tabindex="0">
      <input type="file" accept="${meta.accept}" ${type === 'video' ? '' : 'multiple'}>
      <span aria-hidden="true">⬆</span><span>点击或拖拽文件到这里上传${meta.label}</span>
      <span class="muted">${esc(meta.hint)}</span>
    </label>
    <div class="progress" hidden><i></i></div>`;
  const zone = $('.dropzone', box);
  const input = $('input[type=file]', box);
  const bar = $('.progress', box);
  let uploading = false;

  const run = async (files) => {
    if (uploading || !files.length) return;
    if (type === 'video' && !$('#v-title').value.trim()) { toast('请先填写视频标题', true); $('#v-title').focus(); return; }
    const tooBig = files.filter((f) => f.size > meta.max);
    if (tooBig.length) { toast(`${tooBig[0].name} 超过 ${fmtSize(meta.max)}，请压缩后再上传`, true); return; }
    uploading = true;
    zone.setAttribute('aria-disabled', 'true');
    bar.hidden = false;
    let ok = 0;
    for (const [i, f] of files.entries()) {
      $('span:nth-of-type(2)', zone).textContent = `正在上传 ${i + 1}/${files.length}：${f.name}`;
      const fields = { type, media: f };
      if (type === 'video') { fields.title = $('#v-title').value.trim(); fields.introduction = $('#v-intro').value.trim(); }
      try {
        await uploadFile('/api/v1/materials', fields, (p) => { $('i', bar).style.width = `${((i + p) / files.length) * 100}%`; });
        ok++;
      } catch (err) { toast(`${f.name}：${err.message}`, true); }
    }
    uploading = false;
    input.value = '';
    if (ok) {
      materialCounts = null;
      materialOffset[type] = 0; // 新素材排在最前
      toast(ok === files.length ? `已上传 ${ok} 个文件` : `上传成功 ${ok} 个，失败 ${files.length - ok} 个`);
      route();
    } else {
      bar.hidden = true;
      zone.removeAttribute('aria-disabled');
      $('span:nth-of-type(2)', zone).textContent = `点击或拖拽文件到这里上传${meta.label}`;
    }
  };

  input.addEventListener('change', () => run([...input.files]));
  zone.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); input.click(); } });
  zone.addEventListener('dragover', (e) => { e.preventDefault(); zone.classList.add('over'); });
  zone.addEventListener('dragleave', () => zone.classList.remove('over'));
  zone.addEventListener('drop', (e) => {
    e.preventDefault();
    zone.classList.remove('over');
    const files = [...e.dataTransfer.files];
    run(type === 'video' ? files.slice(0, 1) : files);
  });
}

// ---------- 启动 ----------

// 图片加载失败（过期、防盗链）时隐藏破图标，露出占位底色
document.addEventListener('error', (e) => {
  if (e.target instanceof HTMLImageElement) e.target.classList.add('broken');
}, true);

if (token) {
  signIn(token, (() => { try { return !!localStorage.getItem(TOKEN_KEY); } catch { return false; } })())
    .catch((err) => signOut(err.status === 401 ? '登录已失效，请重新登录' : err.message));
} else {
  signOut();
}
