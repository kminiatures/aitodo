// aitodo Web UI。ビルド不要の素の JS。データはすべて textContent で描画する（innerHTML は使わない）。
'use strict';

const POLL_MS = 1500;
const $ = (id) => document.getElementById(id);
const mobile = window.matchMedia('(max-width: 760px)');

const state = {
  config: { initial_session: 0, author: '' },
  sessions: [],
  sessionId: 0,
  tasks: [],
  selTaskId: 0,
  detail: null,
  folded: new Set(loadJSON('aitodo.folded', [])),
  hideDone: loadJSON('aitodo.hideDone', false),
  showArchived: loadJSON('aitodo.showArchived', false),
  last: { sessions: '', tasks: '', detail: '' },
};

const MARK = { todo: '', doing: '▶', done: '✓', skipped: '–', blocked: '!' };
const STATUS_LABEL = { todo: 'todo', doing: 'doing', done: 'done', skipped: 'skipped', blocked: 'blocked' };

// ---------- utils ----------

function loadJSON(key, def) {
  try {
    const v = localStorage.getItem(key);
    return v == null ? def : JSON.parse(v);
  } catch { return def; }
}
function saveJSON(key, v) {
  try { localStorage.setItem(key, JSON.stringify(v)); } catch { /* ignore */ }
}

function el(tag, attrs, ...children) {
  const e = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === 'class') e.className = v;
      else if (k === 'text') e.textContent = v;
      else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
      else e.setAttribute(k, v === true ? '' : v);
    }
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

async function api(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(path, opt);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`);
  return data;
}

let toastTimer = 0;
function toast(msg) {
  const t = $('toast');
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 3500);
}

// 書き込み操作を実行し、失敗したらトーストで知らせ、最後に必ず再読込する。
async function act(fn) {
  try {
    await fn();
  } catch (e) {
    toast(e.message);
  }
  await refresh(true);
}

function fmtTime(s) {
  if (!s) return '';
  const d = new Date(s);
  if (isNaN(d)) return s;
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function fmtDuration(ms) {
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 1) return '1m 未満';
  const d = Math.floor(m / 1440), h = Math.floor((m % 1440) / 60), mm = m % 60;
  if (d) return `${d}d${h}h`;
  if (h) return `${h}h${mm}m`;
  return `${mm}m`;
}

const finished = (st) => st === 'done' || st === 'skipped';

// 着手から完了まで（作業中なら現在まで）の経過時間。store.Task.WorkTime と同じ規則。
function workTime(t) {
  if (!t.started_at) return null;
  const start = Date.parse(t.started_at);
  if (t.done_at && finished(t.status)) return Date.parse(t.done_at) - start;
  if (t.status === 'doing') return Date.now() - start;
  return null;
}

// ---------- data ----------

// refresh は重なって呼ばれうる（ポーリング・操作後・選択変更）。古い応答で新しい状態を上書きしないよう世代で捨てる。
let refreshGen = 0;

async function refresh(force) {
  const gen = ++refreshGen;
  const stale = () => gen !== refreshGen;
  try {
    const sessions = await api('GET', '/api/sessions' + (state.showArchived ? '?all=1' : ''));
    if (stale()) return;
    const sj = JSON.stringify(sessions);
    if (force || sj !== state.last.sessions) {
      state.sessions = sessions;
      state.last.sessions = sj;
      if (!sessions.some((s) => s.id === state.sessionId)) {
        state.sessionId = sessions.length ? sessions[0].id : 0;
        state.selTaskId = 0;
      }
      renderSessions();
    }

    let tasks = [];
    if (state.sessionId) tasks = await api('GET', `/api/sessions/${state.sessionId}/tasks`);
    if (stale()) return;
    const tj = JSON.stringify(tasks);
    if (force || (tj !== state.last.tasks && !selectingIn($('task-list')))) {
      state.tasks = tasks;
      state.last.tasks = tj;
      if (state.selTaskId && !tasks.some((t) => t.id === state.selTaskId)) state.selTaskId = 0;
      if (!state.selTaskId) {
        const v = visibleTasks();
        if (v.length) state.selTaskId = v[0].id;
      }
      renderTasks();
    }

    let detail = null;
    if (state.selTaskId) detail = await api('GET', `/api/tasks/${state.selTaskId}`).catch(() => null);
    if (stale()) return;
    const dj = JSON.stringify(detail);
    // 作業中のタスクは経過時間を進めるため毎回描き直す。ただし文字列を選択中はコピーできるよう触らない
    const ticking = detail && detail.status === 'doing' && !selectingIn($('detail-body'));
    if (force || dj !== state.last.detail || ticking) {
      state.detail = detail;
      state.last.detail = dj;
      renderDetail();
    }
    if (stale()) return;
    $('conn').classList.remove('err');
    $('conn').title = '自動更新: ' + new Date().toLocaleTimeString();
  } catch (e) {
    $('conn').classList.add('err');
    $('conn').title = '接続エラー: ' + e.message;
  }
}

function selectingIn(node) {
  const sel = window.getSelection();
  return sel && !sel.isCollapsed && node.contains(sel.anchorNode);
}

function currentSession() {
  return state.sessions.find((s) => s.id === state.sessionId) || null;
}

// 折りたたみ・完了非表示を反映した、表示中のタスク（木の順）。
function visibleTasks() {
  const out = [];
  let skipDepth = -1;
  for (const t of state.tasks) {
    if (skipDepth >= 0) {
      if (t.depth > skipDepth) continue;
      skipDepth = -1;
    }
    if (state.hideDone && finished(t.status)) {
      skipDepth = t.depth;
      continue;
    }
    out.push(t);
    if (state.folded.has(t.id) && t.subtasks_total > 0) skipDepth = t.depth;
  }
  return out;
}

// ---------- render ----------

function renderSessions() {
  const ul = $('session-list');
  ul.replaceChildren();
  if (!state.sessions.length) {
    ul.append(el('li', { class: 'empty', text: 'セッションがありません' }));
  }
  for (const s of state.sessions) {
    ul.append(el('li', {
      class: [s.id === state.sessionId && 'sel', s.status === 'archived' && 'archived'].filter(Boolean).join(' '),
      title: s.workdir || '',
      onclick: () => selectSession(s.id),
    },
    el('span', { class: 'name', text: s.name }),
    s.doing > 0 ? el('span', { class: 'doing-dot', title: `${s.doing} 件が進行中` }) : null,
    el('span', { class: 'count', text: `${s.done}/${s.total}` })));
  }
  renderSessionHeader();
}

function renderSessionHeader() {
  const s = currentSession();
  $('session-title').textContent = s ? s.name : '';
  document.title = s ? `${s.name} — aitodo` : 'aitodo';
  $('tasks-head').textContent = s ? `${s.name}  ${s.done}/${s.total}` : 'Tasks';
  $('session-actions').hidden = !s;
  $('add-task').hidden = !s;
  $('btn-archive-session').textContent = s && s.status === 'archived' ? 'Unarchive' : 'Archive';
  const meta = $('session-meta');
  meta.replaceChildren();
  if (s) {
    const lines = [];
    if (s.workdir) lines.push('📁 ' + s.workdir);
    if (s.description) lines.push(s.description);
    meta.append(lines.join('\n'));
    if (s.total) {
      meta.append(el('div', { class: 'progress' }, el('div', { style: `width:${(100 * s.done / s.total).toFixed(1)}%` })));
    }
  }
}

function checkBox(t) {
  return el('span', {
    class: 'check ' + t.status,
    title: finished(t.status) ? 'クリックで未完了に戻す' : 'クリックで完了',
    text: MARK[t.status],
    onclick: (ev) => { ev.stopPropagation(); toggleDone(t); },
  });
}

function renderTasks() {
  const ul = $('task-list');
  const scroll = ul.scrollTop;
  ul.replaceChildren();
  const vis = visibleTasks();
  for (const t of vis) {
    const has = t.subtasks_total > 0;
    const folded = state.folded.has(t.id);
    const sub = [];
    if (has) sub.push(el('span', { text: `☐ ${t.subtasks_done}/${t.subtasks_total}` }));
    if (t.comment_count) sub.push(el('span', { text: `💬 ${t.comment_count}` }));
    const wt = workTime(t);
    if (wt != null) sub.push(el('span', { text: `⏱ ${fmtDuration(wt)}` }));
    if (t.note) sub.push(el('span', { class: 'note', text: '→ ' + t.note }));
    const li = el('li', {
      class: [t.status, t.id === state.selTaskId && 'sel'].filter(Boolean).join(' '),
      'data-id': t.id,
      style: `padding-left:${8 + t.depth * 20}px`,
      onclick: () => { selectTask(t.id); if (mobile.matches) pushView('detail'); },
      ondblclick: () => { if (!mobile.matches) editTask(t); },
    },
    el('span', {
      class: 'fold' + (has ? ' has' : ''),
      text: has ? (folded ? '▸' : '▾') : '',
      title: has ? (folded ? '開く' : '折りたたむ') : null,
      onclick: has ? (ev) => { ev.stopPropagation(); toggleFold(t.id); } : null,
    }),
    checkBox(t),
    el('div', { class: 'task-main' },
      el('div', { class: 'task-title', text: t.title }),
      sub.length ? el('div', { class: 'task-sub' }, sub) : null),
    t.status === 'doing' || t.status === 'blocked' ? el('span', { class: 'badge ' + t.status, text: t.status }) : null,
    el('span', { class: 'task-id', text: '#' + t.id }));
    ul.append(li);
  }
  ul.scrollTop = scroll;
  const empty = $('tasks-empty');
  if (!state.sessionId) {
    empty.textContent = '左の「+ Session」からセッションを作成してください';
  } else if (!state.tasks.length) {
    empty.textContent = 'タスクがありません。上の入力欄から追加できます';
  } else if (!vis.length) {
    empty.textContent = 'すべて完了しています 🎉';
  } else {
    empty.textContent = '';
  }
  empty.hidden = !empty.textContent;
  renderSessionHeader();
}

function renderDetail() {
  const body = $('detail-body');
  const d = state.detail;
  const form = $('comment-form');
  $('detail-head-title').textContent = d ? `#${d.id}` : '';
  if (!d) {
    if (document.body.dataset.view === 'detail') backToTasks();
    body.replaceChildren(el('div', { class: 'empty', text: state.sessionId ? 'タスクを選択してください' : '' }));
    form.hidden = true;
    return;
  }
  form.hidden = false;
  $('comment-author').textContent = state.config.author ? `投稿者: ${state.config.author}` : '';

  const statusBtn = (st, label, key) => el('button', {
    class: 'ghost' + (d.status === st ? ' on' : ''),
    title: key ? `(${key})` : null,
    onclick: () => setStatus(d.id, st),
  }, label);

  const times = el('div', { class: 'times' },
    el('span', { text: '作成' }), el('b', { text: fmtTime(d.created_at) }),
    d.started_at ? [el('span', { text: '着手' }), el('b', { text: fmtTime(d.started_at) })] : null,
    d.done_at && finished(d.status) ? [el('span', { text: '完了' }), el('b', { text: fmtTime(d.done_at) })] : null,
    workTime(d) != null ? [el('span', { text: '所要' }), el('b', { text: fmtDuration(workTime(d)) })] : null,
    el('span', { text: '更新' }), el('b', { text: fmtTime(d.updated_at) }));

  const nodes = [
    el('div', { class: 'detail-head' },
      checkBox(d),
      el('h3', { text: d.title }),
      el('span', { class: 'badge ' + d.status, text: STATUS_LABEL[d.status] || d.status }),
      el('span', { class: 'task-id', text: '#' + d.id })),
    el('div', { class: 'toolbar' },
      statusBtn('done', '✓ Done', 'space'),
      statusBtn('doing', '▶ Start', 's'),
      statusBtn('blocked', '! Block', 'b'),
      statusBtn('skipped', '– Skip', '-'),
      statusBtn('todo', '↺ Todo'),
      el('button', { class: 'ghost', title: '(A)', onclick: () => addSubtask(d) }, '+ Sub'),
      el('button', { class: 'ghost', title: '(e)', onclick: () => editTask(d) }, 'Edit'),
      el('button', { class: 'ghost', title: '上へ (K)', onclick: () => move(d.id, -1) }, '↑'),
      el('button', { class: 'ghost', title: '下へ (J)', onclick: () => move(d.id, 1) }, '↓'),
      el('button', { class: 'ghost danger', title: '(d)', onclick: () => deleteTask(d) }, 'Delete')),
  ];
  if (d.note) nodes.push(el('div', { class: 'field-label', text: 'Note' }), el('div', { class: 'prose', text: d.note }));
  if (d.body) nodes.push(el('div', { class: 'field-label', text: '詳細' }), el('div', { class: 'prose', text: d.body }));
  if (d.subtasks.length) {
    nodes.push(el('div', { class: 'field-label', text: `サブタスク ${d.subtasks_done}/${d.subtasks_total}` }),
      el('ul', { class: 'subtasks' }, d.subtasks.map((s) => el('li', { onclick: () => selectTask(s.id) },
        checkBox(s), el('span', { class: 'task-title', text: s.title }), el('span', { class: 'task-id', text: '#' + s.id })))));
  }
  nodes.push(el('div', { class: 'field-label', text: '日時' }), times);
  nodes.push(el('div', { class: 'field-label', text: `コメント ${d.comments.length}` }));
  if (d.comments.length) {
    nodes.push(el('ul', { class: 'comments' }, d.comments.map((c) => el('li', null,
      el('div', { class: 'comment-head' },
        el('span', { class: 'author', text: c.author || '?' }),
        el('span', { text: fmtTime(c.created_at) }),
        el('button', { class: 'ghost danger del', onclick: () => deleteComment(c) }, '削除')),
      el('div', { class: 'prose', text: c.body })))));
  } else {
    nodes.push(el('div', { class: 'hint', text: 'まだコメントはありません' }));
  }
  const scroll = body.scrollTop;
  body.replaceChildren(...nodes);
  body.scrollTop = scroll;
}

// ---------- views（スマホでは 1 画面ずつ表示する） ----------

// URL の ?session= を保ったまま history のエントリを積む / 置き換える。
function sessionURL() {
  return state.sessionId ? `?session=${state.sessionId}` : location.pathname;
}

function setView(view) {
  document.body.dataset.view = view;
}

// 詳細やセッション一覧は history に積むので、スマホの「戻る」ジェスチャーで一覧に戻れる。
function pushView(view) {
  if (document.body.dataset.view === view) return;
  setView(view);
  history.pushState({ view }, '', sessionURL());
}

// 積んだ画面から一覧へ戻る。直接開いた（積んでいない）場合は単に切り替える。
function backToTasks() {
  if (history.state && history.state.view && history.state.view !== 'tasks') history.back();
  else setView('tasks');
}

window.addEventListener('popstate', (ev) => {
  setView((ev.state && ev.state.view) || 'tasks');
  history.replaceState(history.state, '', sessionURL());
});

// ---------- actions ----------

function selectSession(id) {
  if (id === state.sessionId) return;
  state.sessionId = id;
  state.selTaskId = 0;
  state.last.tasks = '';
  history.replaceState(history.state, '', sessionURL());
  if (document.body.dataset.view === 'sessions') backToTasks();
  renderSessions();
  refresh(true);
}

function selectTask(id) {
  state.selTaskId = id;
  renderTasks();
  const li = document.querySelector(`#task-list li[data-id="${id}"]`);
  if (li) li.scrollIntoView({ block: 'nearest' });
  refresh(true);
}

function toggleFold(id) {
  if (state.folded.has(id)) state.folded.delete(id);
  else state.folded.add(id);
  saveJSON('aitodo.folded', [...state.folded]);
  renderTasks();
}

function setStatus(id, status) {
  return act(() => api('POST', `/api/tasks/${id}/status`, { status }));
}

function toggleDone(t) {
  return setStatus(t.id, finished(t.status) ? 'todo' : 'done');
}

function move(id, delta) {
  return act(() => api('POST', `/api/tasks/${id}/move`, { delta }));
}

function addSubtask(parent) {
  openModal({
    title: `サブタスクを追加（#${parent.id} ${parent.title}）`,
    fields: [
      { name: 'title', label: 'タイトル' },
      { name: 'body', label: '詳細', multiline: true },
    ],
    onSubmit: async (v) => {
      const t = await api('POST', `/api/tasks/${parent.id}/subtasks`, v);
      state.folded.delete(parent.id);
      state.selTaskId = t.id;
    },
  });
}

function editTask(t) {
  openModal({
    title: `タスクを編集 #${t.id}`,
    fields: [
      { name: 'title', label: 'タイトル', value: t.title },
      { name: 'body', label: '詳細', value: t.body, multiline: true },
      { name: 'note', label: 'Note（結果の一行要約）', value: t.note },
    ],
    onSubmit: (v) => api('PATCH', `/api/tasks/${t.id}`, v),
  });
}

function deleteTask(t) {
  const extra = t.subtasks_total ? `サブタスク ${t.subtasks_total} 件も` : '';
  openModal({
    title: `#${t.id}「${t.title}」を削除しますか？`,
    message: `${extra}コメントも一緒に削除されます。元に戻せません。`,
    ok: 'Delete',
    danger: true,
    onSubmit: async () => {
      await api('DELETE', `/api/tasks/${t.id}`);
      if (document.body.dataset.view === 'detail') backToTasks();
    },
  });
}

function deleteComment(c) {
  openModal({
    title: 'コメントを削除しますか？',
    message: c.body.length > 80 ? c.body.slice(0, 80) + '…' : c.body,
    ok: 'Delete',
    danger: true,
    onSubmit: () => api('DELETE', `/api/comments/${c.id}`),
  });
}

function newSession() {
  openModal({
    title: 'セッションを作成',
    fields: [
      { name: 'name', label: '名前' },
      { name: 'workdir', label: '作業フォルダ（任意。絶対パスか ~/…）' },
      { name: 'description', label: '説明', multiline: true },
    ],
    onSubmit: async (v) => {
      const s = await api('POST', '/api/sessions', v);
      state.sessionId = s.id;
      state.selTaskId = 0;
      history.replaceState(history.state, '', sessionURL());
      if (document.body.dataset.view === 'sessions') backToTasks();
    },
  });
}

function editSession() {
  const s = currentSession();
  if (!s) return;
  openModal({
    title: `セッションを編集 #${s.id}`,
    fields: [
      { name: 'name', label: '名前', value: s.name },
      { name: 'workdir', label: '作業フォルダ（絶対パスか ~/…。空にすると解除）', value: s.workdir },
      { name: 'description', label: '説明', value: s.description, multiline: true },
    ],
    onSubmit: (v) => {
      const patch = {};
      for (const k of Object.keys(v)) if (v[k] !== (s[k] || '')) patch[k] = v[k];
      return api('PATCH', `/api/sessions/${s.id}`, patch);
    },
  });
}

function toggleArchiveSession() {
  const s = currentSession();
  if (!s) return;
  const status = s.status === 'archived' ? 'active' : 'archived';
  act(() => api('PATCH', `/api/sessions/${s.id}`, { status }));
}

function deleteSession() {
  const s = currentSession();
  if (!s) return;
  openModal({
    title: `セッション「${s.name}」を削除しますか？`,
    message: `タスク ${s.total} 件とコメントもすべて削除されます。元に戻せません。`,
    ok: 'Delete',
    danger: true,
    onSubmit: () => api('DELETE', `/api/sessions/${s.id}`),
  });
}

// ---------- modal ----------

let modalSubmit = null;

function openModal({ title, message, fields = [], ok = 'OK', danger = false, onSubmit }) {
  const dlg = $('modal');
  $('modal-title').textContent = title;
  $('modal-error').hidden = true;
  const box = $('modal-fields');
  box.replaceChildren();
  if (message) box.append(el('p', { class: 'prose', text: message }));
  for (const f of fields) {
    const input = f.multiline
      ? el('textarea', { name: f.name, rows: 5 })
      : el('input', { name: f.name, type: 'text' });
    input.value = f.value || '';
    box.append(el('div', { class: 'modal-field' }, el('label', { text: f.label }), input));
  }
  const okBtn = $('modal-ok');
  okBtn.textContent = ok;
  okBtn.style.background = danger ? 'var(--danger)' : '';
  okBtn.style.borderColor = danger ? 'var(--danger)' : '';
  modalSubmit = async () => {
    const v = {};
    for (const f of fields) v[f.name] = box.querySelector(`[name="${f.name}"]`).value;
    okBtn.disabled = true;
    try {
      await onSubmit(v);
      dlg.close();
      await refresh(true);
    } catch (e) {
      $('modal-error').textContent = e.message;
      $('modal-error').hidden = false;
    } finally {
      okBtn.disabled = false;
    }
  };
  dlg.showModal();
  const first = box.querySelector('input, textarea');
  if (first) { first.focus(); first.select?.(); } else okBtn.focus();
}

$('modal-form').addEventListener('submit', (ev) => {
  ev.preventDefault();
  modalSubmit?.();
});
$('modal-cancel').addEventListener('click', () => $('modal').close());
$('modal').addEventListener('keydown', (ev) => {
  // 複数行欄では Enter は改行、Ctrl/Cmd+Enter で送信
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault();
    modalSubmit?.();
  }
});

// ---------- wiring ----------

$('btn-new-session').addEventListener('click', newSession);
$('btn-edit-session').addEventListener('click', editSession);
$('btn-archive-session').addEventListener('click', toggleArchiveSession);
$('btn-delete-session').addEventListener('click', deleteSession);
$('btn-help').addEventListener('click', () => $('help').showModal());
$('btn-back').addEventListener('click', backToTasks);
$('btn-sessions').addEventListener('click', () => {
  if (document.body.dataset.view === 'sessions') backToTasks();
  else pushView('sessions');
});
$('help-close').addEventListener('click', () => $('help').close());

$('add-task').addEventListener('submit', (ev) => {
  ev.preventDefault();
  const input = $('add-task-input');
  const title = input.value.trim();
  if (!title || !state.sessionId) return;
  act(async () => {
    const t = await api('POST', `/api/sessions/${state.sessionId}/tasks`, { title });
    input.value = '';
    state.selTaskId = t.id;
  });
});

$('comment-form').addEventListener('submit', (ev) => {
  ev.preventDefault();
  const input = $('comment-input');
  const body = input.value.trim();
  if (!body || !state.selTaskId) return;
  const id = state.selTaskId;
  act(async () => {
    await api('POST', `/api/tasks/${id}/comments`, { body });
    input.value = '';
  });
});
$('comment-input').addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault();
    $('comment-form').requestSubmit();
  }
});

const optHide = $('opt-hide-done');
const optArch = $('opt-show-archived');
optHide.checked = state.hideDone;
optArch.checked = state.showArchived;
optHide.addEventListener('change', () => {
  state.hideDone = optHide.checked;
  saveJSON('aitodo.hideDone', state.hideDone);
  renderTasks();
});
optArch.addEventListener('change', () => {
  state.showArchived = optArch.checked;
  saveJSON('aitodo.showArchived', state.showArchived);
  refresh(true);
});

function moveSelection(delta) {
  const vis = visibleTasks();
  if (!vis.length) return;
  let i = vis.findIndex((t) => t.id === state.selTaskId);
  i = i < 0 ? 0 : Math.max(0, Math.min(vis.length - 1, i + delta));
  selectTask(vis[i].id);
}

function selectedTask() {
  return state.tasks.find((t) => t.id === state.selTaskId) || null;
}

document.addEventListener('keydown', (ev) => {
  const tg = ev.target;
  const typing = tg.tagName === 'TEXTAREA' || (tg.tagName === 'INPUT' && tg.type !== 'checkbox');
  if (typing) {
    if (ev.key === 'Escape') tg.blur();
    return;
  }
  if (document.querySelector('dialog[open]')) return;
  if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
  // フォーカス中のボタン・チェックボックスでは Enter / space は既定の動作（押す）に任せる
  if ((tg.tagName === 'BUTTON' || tg.tagName === 'INPUT') && (ev.key === 'Enter' || ev.key === ' ')) return;
  const t = selectedTask();
  const run = (fn) => { ev.preventDefault(); fn(); };
  switch (ev.key) {
    case 'j': case 'ArrowDown': return run(() => moveSelection(1));
    case 'k': case 'ArrowUp': return run(() => moveSelection(-1));
    case '[': case ']': {
      const i = state.sessions.findIndex((s) => s.id === state.sessionId);
      const n = state.sessions[i + (ev.key === ']' ? 1 : -1)];
      return n && run(() => selectSession(n.id));
    }
    case 'a': return run(() => $('add-task-input').focus());
    case 'n': return run(newSession);
    case 'f': return run(() => { optHide.click(); });
    case 'H': return run(() => { optArch.click(); });
    case '?': return run(() => $('help').showModal());
  }
  if (!t) return;
  switch (ev.key) {
    case ' ': case 'x': return run(() => toggleDone(t));
    case 's': return run(() => setStatus(t.id, 'doing'));
    case 'b': return run(() => setStatus(t.id, 'blocked'));
    case '-': return run(() => setStatus(t.id, 'skipped'));
    case 'A': return run(() => addSubtask(t));
    case 'e': case 'Enter': return run(() => editTask(t));
    case 'c': return run(() => $('comment-input').focus());
    case 'K': return run(() => move(t.id, -1));
    case 'J': return run(() => move(t.id, 1));
    case 'd': case 'Delete': return run(() => deleteTask(t));
    case 'h': case 'ArrowLeft':
      if (t.subtasks_total && !state.folded.has(t.id)) return run(() => toggleFold(t.id));
      if (t.parent_id) return run(() => selectTask(t.parent_id));
      return;
    case 'l': case 'ArrowRight':
      if (state.folded.has(t.id)) return run(() => toggleFold(t.id));
      return;
  }
});

// ---------- boot ----------

(async function boot() {
  try {
    state.config = await api('GET', '/api/config');
  } catch { /* ignore */ }
  const q = Number(new URLSearchParams(location.search).get('session'));
  state.sessionId = q || state.config.initial_session || 0;
  history.replaceState({ view: 'tasks' }, '', sessionURL());
  await refresh(true);
  setInterval(() => { if (!document.hidden) refresh(false); }, POLL_MS);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(false); });
})();
