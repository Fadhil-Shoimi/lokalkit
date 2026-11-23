import '@fontsource/sora/700.css';
import './style.css';

// Go methods bound by Wails live on window.go.main.App; events on window.runtime.
const api = new Proxy({}, { get: (_, name) => (...args) => window.go.main.App[name](...args) });
const on = (event, fn) => window.runtime.EventsOn(event, fn);

const $ = (sel) => document.querySelector(sel);
let state = null;
let logs = [];

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

let toastTimer;
function toast(msg, isError = false) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast' + (isError ? ' error' : '');
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), isError ? 7000 : 3000);
}

// Run a Go call, showing errors as a toast. Disables `btn` while running.
async function run(fn, btn) {
  if (btn) btn.disabled = true;
  try {
    return await fn();
  } catch (e) {
    toast(String(e), true);
  } finally {
    if (btn) btn.disabled = false;
  }
}

// ---------- icons (stroke icons, 24x24 grid) ----------

const ICONS = {
  globe: '<circle cx="12" cy="12" r="10"/><path d="M2 12h20"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>',
  database: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14a9 3 0 0 0 18 0V5"/><path d="M3 12a9 3 0 0 0 18 0"/>',
  terminal: '<polyline points="4 17 10 11 4 5"/><line x1="12" x2="20" y1="19" y2="19"/>',
  folder: '<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>',
  mail: '<rect width="20" height="16" x="2" y="4" rx="2"/><path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7"/>',
  refresh: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  import: '<path d="M12 3v12"/><path d="m8 11 4 4 4-4"/><path d="M8 5H4a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2h-4"/>',
  chevron: '<path d="m6 9 6 6 6-6"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/>',
  up: '<path d="m5 12 7-7 7 7"/><path d="M12 19V5"/>',
  x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
  code: '<polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>',
  play: '<polygon points="6 4 20 12 6 20 6 4"/>',
  stop: '<rect width="12" height="12" x="6" y="6" rx="2"/>',
};
const FILLED = new Set(['play', 'stop']);
function icon(name) {
  return `<svg class="ico${FILLED.has(name) ? ' fill' : ''}" viewBox="0 0 24 24" aria-hidden="true">${ICONS[name] || ''}</svg>`;
}
// Static buttons: <button data-icon="globe">Web</button>
document.querySelectorAll('[data-icon]').forEach((el) => el.insertAdjacentHTML('afterbegin', icon(el.dataset.icon)));

// ---------- rendering ----------

function renderServices(services) {
  state.services = services;
  $('#services').innerHTML = services.map((s) => {
    const cls = ['card', 'svc', s.running && 'running', s.busy && 'busy'].filter(Boolean).join(' ');
    const stateLbl = !s.installed ? 'Not installed' : s.busy ? 'Working…' : s.running ? 'Running' : 'Stopped';
    const ports = String(s.ports || '').split('/').filter(Boolean).map((p) => `<span class="port">:${esc(p)}</span>`).join('');
    const button = s.installed
      ? `<button class="btn small ${s.running ? 'danger' : ''}" data-svc="${s.id}" ${s.busy ? 'disabled' : ''}>${icon(s.running ? 'stop' : 'play')}${s.busy ? '…' : s.running ? 'Stop' : 'Start'}</button>`
      : `<button class="btn small primary" data-tab-go="install">${icon('download')}Install</button>`;
    return `<div class="${cls}">
      <div class="svc-head"><span class="dot"></span><span class="svc-name">${esc(s.name)}</span><span class="svc-ver">${esc(s.version)}</span><span class="svc-state">${stateLbl}</span></div>
      <div class="svc-foot">${s.installed ? `<div class="svc-ports">${ports}</div>` : `<div class="svc-missing">Get it from the Install tab</div>`}${button}</div>
    </div>`;
  }).join('');

  const anyRunning = services.some((s) => s.running);
  const anyBusy = services.some((s) => s.busy);
  const all = $('#btnAll');
  all.innerHTML = anyRunning ? `${icon('stop')}Stop All` : `${icon('play')}Start All`;
  all.disabled = anyBusy;
  all.dataset.mode = anyRunning ? 'stop' : 'start';
}

function renderProjects(projects) {
  $('#projCount').textContent = projects.length || '';
  $('#projCount').hidden = !projects.length;
  const apacheUp = state.services?.find((s) => s.id === 'apache')?.running;
  if (!projects.length) {
    $('#projects').innerHTML = `<div class="empty">No projects yet. Create one above, import from Laragon, or put a folder in <b>www</b>.</div>`;
    return;
  }
  $('#projects').innerHTML = projects.map((p) => `
    <div class="card proj">
      <div class="proj-icon ${p.kind}">${icon(p.kind === 'php' ? 'code' : p.kind === 'laravel' ? 'code' : 'globe')}</div>
      <div class="proj-main">
        <div class="proj-name">${esc(p.name)}${p.kind !== 'php' ? `<span class="badge ${p.kind}">${p.kind}</span>` : ''}${p.linked ? `<span class="badge linked" title="${esc(p.path)}">imported</span>` : ''}</div>
        <div class="proj-links">
          ${p.https
            ? `<a class="proj-url" data-url="${esc(p.https)}" title="${apacheUp ? 'Open in browser' : 'Start Apache first'}">${esc(p.https)}</a>
               <a class="proj-url alt" data-url="${esc(p.url)}">http</a>`
            : `<a class="proj-url" data-url="${esc(p.url)}" title="${apacheUp ? 'Open in browser' : 'Start Apache first'}">${esc(p.url)}</a>`}
        </div>
        ${p.task ? `<div class="proj-task"><span class="spin"></span>${esc(p.task)}</div>`
          : p.error ? `<div class="proj-warn">⚠ Laravel setup failed: ${esc(p.error)} <button class="linkish" data-repair="${esc(p.name)}">Retry</button></div>`
          : p.needsAssets && !p.needsDeps ? `<div class="proj-warn">⚠ The front-end is not built yet (needs Node.js). <button class="linkish" data-repair="${esc(p.name)}">Build now</button></div>`
          : p.needsDeps ? `<div class="proj-warn">⚠ Composer packages are missing (vendor folder), so the app can't run. <button class="linkish" data-repair="${esc(p.name)}">Repair</button></div>` : ''}
        ${(state.missingDbs || []).filter((d) => d.project === p.name).map((d) => `<div class="proj-warn">⚠ Database <code>${esc(d.database)}</code> (from .env) doesn't exist in MySQL. <button class="linkish" data-createdb="${esc(p.name)}">Create it${d.laravel ? ' + migrate' : ''}</button></div>`).join('')}
        ${(state.missingHosts || []).includes(p.host) ? `<div class="proj-warn">⚠ Not in the Windows hosts file yet, so browsers can't find it. <button class="linkish" data-fixhosts="1">Fix now</button> or <button class="linkish" data-uselocalhost="1">use .localhost instead</button> (works without the hosts file)</div>` : ''}
      </div>
      <div class="proj-btns">
        <button class="btn small" data-folder="${esc(p.name)}" title="Open folder">${icon('folder')}Folder</button>
        <button class="btn small" data-term="${esc(p.path)}" title="Open a terminal here">${icon('terminal')}Terminal</button>
        ${p.linked ? `<button class="btn small ghost" data-unlink="${esc(p.name)}" title="Remove from LokalKit. The files stay where they are.">${icon('x')}Remove</button>` : ''}
      </div>
    </div>`).join('');
}

// Branches still supported by the PHP project (older ones go under "Older").
const SUPPORTED_FROM = '8.2';
let phpShowOlder = false;

function renderPHP() {
  const cur = state.php?.find((p) => p.name === state.currentPhp);
  $('#phpCur').textContent = cur ? cur.version : (state.php?.length ? '–' : 'not found');
  $('#phpBtn').disabled = phpInstalling;
  const up = $('#phpUpdate');
  up.hidden = !phpUpdates.update && !phpInstalling;
  up.disabled = phpInstalling;
  if (!phpInstalling && phpUpdates.update) up.innerHTML = `${icon('up')}PHP ${esc(phpUpdates.update.version)}`;
  if (!$('#phpPop').hidden) renderPHPMenu();
}

function renderPHPMenu() {
  const have = new Set((state.php || []).map((p) => branchOf(p.version)));
  const rel = (phpUpdates.releases || []).filter((r) => !have.has(r.branch));
  const supported = rel.filter((r) => cmpVer(r.branch, SUPPORTED_FROM) >= 0);
  const older = rel.filter((r) => cmpVer(r.branch, SUPPORTED_FROM) < 0);
  let h = '<div class="pop-head">Installed</div>';
  h += (state.php || []).map((p) => `<button class="pop-item ${p.name === state.currentPhp ? 'current' : ''}" data-php-use="${esc(p.name)}">
      ${p.name === state.currentPhp ? icon('check') : '<span class="ico"></span>'}<span class="grow">PHP ${esc(p.version)}</span>${p.ts ? '' : '<small>NTS</small>'}</button>`).join('')
    || '<div class="pop-item pop-more">None yet</div>';
  if (phpUpdates.update) {
    h += `<div class="pop-sep"></div><button class="pop-item update" data-php-dl="${esc(phpUpdates.update.branch)}">${icon('up')}<span class="grow">Update to PHP ${esc(phpUpdates.update.version)}</span></button>`;
  }
  if (supported.length || older.length) h += '<div class="pop-sep"></div><div class="pop-head">Download</div>';
  h += supported.map((r) => `<button class="pop-item" data-php-dl="${esc(r.branch)}">${icon('download')}<span class="grow">PHP ${esc(r.version)}</span></button>`).join('');
  if (older.length) {
    h += phpShowOlder
      ? older.map((r) => `<button class="pop-item" data-php-dl="${esc(r.branch)}">${icon('download')}<span class="grow">PHP ${esc(r.version)}</span><small>end of life</small></button>`).join('')
      : `<button class="pop-item pop-more" data-php-older="1"><span class="ico"></span><span class="grow">Older versions (no longer supported)…</span></button>`;
  }
  if (phpUpdates.error) h += `<div class="pop-sep"></div><div class="pop-item pop-more">Couldn't check for downloads</div>`;
  $('#phpPop').innerHTML = h;
}

const cmpVer = (a, b) => {
  const pa = String(a).split('.').map(Number), pb = String(b).split('.').map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] || 0) - (pb[i] || 0);
    if (d) return d;
  }
  return 0;
};

function togglePHPMenu(show) {
  const pop = $('#phpPop');
  pop.hidden = show === undefined ? !pop.hidden : !show;
  if (!pop.hidden) renderPHPMenu();
}

let phpUpdates = {};
let phpInstalling = false;
const branchOf = (v) => String(v).split('.').slice(0, 2).join('.');

async function checkPHPUpdates(force = false) {
  try { phpUpdates = (await api.GetPHPUpdates(force)) || {}; } catch (e) { phpUpdates = {}; }
  renderPHP();
}

async function installPHP(branch, label) {
  if (!confirm(`Download ${label} and switch to it?\n\nYour php.ini settings are copied over and Apache restarts. The current PHP stays installed, so you can switch back from this menu.`)) {
    renderPHP();
    return;
  }
  phpInstalling = true;
  $('#phpUpdate').hidden = false;
  $('#phpUpdate').textContent = `Downloading PHP ${branch}…`;
  togglePHPMenu(false);
  renderPHP();
  try {
    await api.InstallPHP(branch);
    toast(`Now using ${label}`);
  } catch (e) {
    toast(String(e), true);
  } finally {
    phpInstalling = false;
    const s = await api.GetState();
    state.php = s.php; state.currentPhp = s.currentPhp; state.config = s.config;
    await checkPHPUpdates(true);
  }
}

function addLog(e) {
  logs.push(e);
  if (logs.length > 300) logs = logs.slice(-300);
  const box = $('#logs');
  const stick = box.scrollTop + box.clientHeight >= box.scrollHeight - 20;
  box.insertAdjacentHTML('beforeend', `<div class="log-line ${e.level}"><time>${esc(e.time)}</time><span>${esc(e.msg)}</span></div>`);
  if (stick) box.scrollTop = box.scrollHeight;
}

function renderSettings() {
  const f = $('#settings');
  for (const [k, v] of Object.entries(state.config)) {
    const el = f.elements[k];
    if (!el) continue;
    if (el.type === 'checkbox') el.checked = v;
    else el.value = v;
  }
  $('#rootPath').textContent = state.paths?.root || '';
  renderSSL(state.ssl);
  renderUpdateStatus(updateInfo);
}

function renderSSL(ssl) {
  const el = $('#sslStatus');
  const btn = $('#btnTrust');
  if (!ssl) return;
  if (!ssl.caExists) {
    el.textContent = 'A certificate will be created when Apache starts with HTTPS on.';
    el.className = 'muted-text';
  } else if (ssl.trusted) {
    el.textContent = '✓ Windows trusts the LokalKit certificate';
    el.className = 'muted-text ok';
  } else {
    el.textContent = 'Certificate not trusted yet: browsers will show a warning.';
    el.className = 'muted-text warn';
  }
  btn.hidden = !ssl.supported || !ssl.caExists || ssl.trusted;
}

function showTab(name) {
  document.querySelectorAll('.tab').forEach((b) => b.classList.toggle('active', b.dataset.tab === name));
  document.querySelectorAll('.panel').forEach((p) => (p.hidden = p.id !== 'tab-' + name));
  if (name === 'logs') $('#logs').scrollTop = $('#logs').scrollHeight;
  if (name === 'install') loadComponents();
  if (name === 'databases') loadDatabases();
}

// ---------- installer ----------

let components = [];
let installing = false;
const progress = {}; // id -> last Progress

function fmtMB(n) { return (n / 1048576).toFixed(n < 10485760 ? 1 : 0) + ' MB'; }

async function loadComponents() {
  components = (await api.GetComponents()) || [];
  renderComponents();
}

function renderComponents() {
  const missing = components.filter((c) => c.recommended && !c.installed).length;
  $('#installCount').textContent = missing || '';
  $('#installCount').hidden = !missing;
  const checked = new Set([...document.querySelectorAll('#components input:checked')].map((i) => i.value));
  const firstRender = !document.querySelector('#components input');
  $('#components').innerHTML = components.map((c) => {
    const p = progress[c.id];
    const isChecked = firstRender ? (c.recommended && !c.installed) : checked.has(c.id);
    let state = c.installed ? `<span class="comp-state ok">✓ Installed${c.version ? ' ' + esc(c.version) : ''}</span>` : '<span class="comp-state">Not installed</span>';
    if (p && p.phase === 'done') state = `<span class="comp-state ok">✓ Installed${p.version && p.version !== 'latest' ? ' ' + esc(p.version) : ''}</span>`;
    if (p && p.phase === 'error') state = '<span class="comp-state err">Failed</span>';
    let prog = '';
    if (p && !['done'].includes(p.phase)) {
      const known = p.phase === 'downloading' && p.total > 0;
      const pct = known ? Math.round((p.received / p.total) * 100) : 0;
      const bar = ['error', 'cancelled'].includes(p.phase) ? ''
        : `<div class="bar ${known ? '' : 'indeterminate'}"><div style="${known ? `width:${pct}%` : ''}"></div></div>`;
      let msg = esc(p.message);
      if (p.phase === 'downloading') msg = `Downloading ${p.version && p.version !== 'latest' ? esc(p.version) + ' ' : ''}· ${fmtMB(p.received)}${p.total > 0 ? ' / ' + fmtMB(p.total) + ` (${pct}%)` : ''}`;
      prog = `<div class="comp-progress">${bar}<span class="comp-msg ${p.phase === 'error' ? 'err' : ''}">${msg}</span></div>`;
    }
    return `<label class="card comp">
      <input type="checkbox" value="${c.id}" ${isChecked ? 'checked' : ''} ${installing ? 'disabled' : ''}/>
      <div><div class="comp-name">${esc(c.name)}</div><div class="comp-desc">${esc(c.description)}</div></div>
      ${state}${prog}
    </label>`;
  }).join('');
  $('#btnInstall').disabled = installing;
  $('#btnInstall').textContent = installing ? 'Installing…' : 'Install selected';
  $('#btnCancelInstall').hidden = !installing;
  $('#installHint').textContent = installing ? 'You can keep using LokalKit while this runs.' : '';
}

async function refresh() {
  state = await api.GetState();
  $('#appVersion').textContent = 'v' + state.version;
  if (state.error) {
    $('#fatal').hidden = false;
    $('#fatal').textContent = state.error;
    return;
  }
  renderServices(state.services);
  renderProjects(state.projects);
  renderPHP();
  renderSettings();
  checkPHPUpdates();
  loadComponents().then(() => {
    // First run with nothing installed: open the Install tab.
    if (!state.services.some((x) => x.installed)) showTab('install');
  });
  $('#logs').innerHTML = '';
  logs = [];
  state.logs.forEach(addLog);
}

// ---------- events ----------

document.addEventListener('click', (ev) => {
  const t = ev.target.closest('button, a');
  if (!t) return;

  if (t.dataset.svc) {
    const svc = state.services.find((s) => s.id === t.dataset.svc);
    run(() => (svc.running ? api.StopService(svc.id) : api.StartService(svc.id)));
  } else if (t.id === 'btnAll') {
    run(() => (t.dataset.mode === 'stop' ? api.StopAll() : api.StartAll()));
  } else if (t.dataset.fixhosts) {
    run(async () => {
      state.missingHosts = await api.FixHosts();
      renderProjects(state.projects);
      if (!state.missingHosts.length) toast('Hosts file updated. Reload the page in your browser.');
    }, t);
  } else if (t.dataset.createdb) {
    run(async () => { await api.CreateProjectDatabase(t.dataset.createdb); await refreshProjects(); if (!$('#tab-databases').hidden) loadDatabases(); toast('Database created'); }, t);
  } else if (t.dataset.dbExport) {
    run(async () => { const path = await api.ExportDatabase(t.dataset.dbExport); toast(`Backup saved: ${path}`); }, t);
  } else if (t.dataset.dbImport) {
    run(async () => { if (await api.ImportSQLFile(t.dataset.dbImport)) { toast(`Imported into ${t.dataset.dbImport}`); loadDatabases(); } }, t);
  } else if (t.dataset.dbDrop) {
    const n = t.dataset.dbDrop;
    if (!confirm(`Delete the database "${n}" and ALL its tables?\n\nThis cannot be undone. Tip: click Export first to keep a backup.`)) return;
    run(async () => { await api.DropDatabase(n); toast(`Deleted ${n}`); loadDatabases(); refreshProjects(); }, t);
  } else if (t.dataset.unlink) {
    if (!confirm(`Remove ${t.dataset.unlink} from LokalKit?\n\nIts files are NOT deleted; they stay in their folder.`)) return;
    run(async () => { await api.UnlinkProject(t.dataset.unlink); await refreshProjects(); toast('Removed from LokalKit'); }, t);
  } else if (t.dataset.repair) {
    run(async () => { await api.RepairProject(t.dataset.repair); await refreshProjects(); toast('Installing packages… progress shows on the project'); }, t);
  } else if (t.dataset.uselocalhost) {
    run(async () => {
      await api.UseLocalhostDomain();
      await refresh();
      toast('Done. Your projects now open at https://name.localhost');
    }, t);
  } else if (t.dataset.folder) {
    run(() => api.OpenFolder(t.dataset.folder));
  } else if (t.dataset.term) {
    run(() => api.OpenTerminal(t.dataset.term));
  } else if (t.dataset.url) {
    api.OpenURL(t.dataset.url);
  } else if (t.dataset.tab || t.dataset.tabGo) {
    showTab(t.dataset.tab || t.dataset.tabGo);
  } else if (t.id === 'clearLog') {
    $('#logs').innerHTML = '';
  } else if (t.dataset.act) {
    const acts = {
      web: () => api.OpenWeb(),
      db: () => api.OpenDatabase(),
      term: () => api.OpenTerminal(''),
      www: () => api.OpenFolder('www'),
      mail: () => api.OpenMailpit(),
      reload: async () => { await api.Reload(); await refreshProjects(); toast('Virtual hosts updated'); },
    };
    run(acts[t.dataset.act], t);
  }
});

async function refreshProjects() {
  const s = await api.GetState();
  state.projects = s.projects;
  state.missingHosts = s.missingHosts;
  state.missingDbs = s.missingDbs;
  renderProjects(s.projects);
}

$('#newProject').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const name = $('#npName').value.trim();
  if (!name) return $('#npName').focus();
  const btn = ev.submitter;
  const db = { mode: $('#npDb').value, name: $('#npDbName').value.trim() };
  const p = await run(() => api.CreateProject(name, $('#npKind').value, db), btn);
  if (p) {
    $('#npName').value = '';
    $('#npDbName').value = '';
    updateDbHint();
    await refreshProjects();
    toast($('#npKind').value.startsWith('laravel') ? `Creating ${p.name} with the latest Laravel… this takes a few minutes` : `Project ${p.name} created → ${p.https || p.url || ''}`);
  }
});

// Database options under the create form.
function updateDbHint() {
  const kind = $('#npKind').value;
  const mode = $('#npDb').value;
  const auto = ($('#npName').value || 'project_name').replace(/-/g, '_');
  $('#npDbName').placeholder = auto;
  $('#npDbNameWrap').hidden = mode !== 'mysql';
  const laravel = kind.startsWith('laravel');
  $('#npDbHint').textContent =
    mode === 'mysql' ? (laravel ? '· .env is set up and tables (users, sessions…) are created' : '· user root, no password')
    : mode === 'sqlite' ? (laravel ? '· database/database.sqlite, tables created' : '· use PDO with a .sqlite file')
    : '';
}
$('#npKind').addEventListener('change', updateDbHint);
$('#npDb').addEventListener('change', updateDbHint);
$('#npName').addEventListener('input', updateDbHint);
$('#npDbName').addEventListener('input', (e) => { e.target.value = e.target.value.replace(/[^A-Za-z0-9_]/g, '_'); });
updateDbHint();

// Only allow characters valid in a domain name while typing.
$('#npName').addEventListener('input', (e) => {
  e.target.value = e.target.value.toLowerCase().replace(/[\s_]+/g, '-').replace(/[^a-z0-9-]/g, '');
});

$('#phpUpdate').addEventListener('click', () => {
  if (phpUpdates.update) installPHP(phpUpdates.update.branch, `PHP ${phpUpdates.update.version}`);
});

$('#phpBtn').addEventListener('click', (e) => { e.stopPropagation(); togglePHPMenu(); });
document.addEventListener('click', (e) => { if (!e.target.closest('.php-menu')) togglePHPMenu(false); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') togglePHPMenu(false); });
$('#phpPop').addEventListener('click', async (e) => {
  e.stopPropagation();
  const t = e.target.closest('button');
  if (!t) return;
  if (t.dataset.phpOlder) { phpShowOlder = true; return renderPHPMenu(); }
  if (t.dataset.phpDl) {
    const r = (phpUpdates.releases || []).find((x) => x.branch === t.dataset.phpDl);
    return installPHP(t.dataset.phpDl, `PHP ${r ? r.version : t.dataset.phpDl}`);
  }
  if (t.dataset.phpUse && t.dataset.phpUse !== state.currentPhp) {
    togglePHPMenu(false);
    const cfg = { ...state.config, phpVersion: t.dataset.phpUse };
    const ok = await run(async () => { await api.SaveConfig(cfg); return true; });
    if (ok) {
      state.config = cfg; state.currentPhp = t.dataset.phpUse; renderPHP();
      toast('PHP version changed. Apache restarts to use it.');
    }
  } else togglePHPMenu(false);
});

$('#settings').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const f = ev.target.elements;
  const cfg = {
    ...state.config,
    apachePort: +f.apachePort.value,
    mysqlPort: +f.mysqlPort.value,
    mailpitSmtpPort: +f.mailpitSmtpPort.value,
    mailpitHttpPort: +f.mailpitHttpPort.value,
    tld: f.tld.value.trim().toLowerCase(),
    autoVhost: f.autoVhost.checked,
    startAllOnLaunch: f.startAllOnLaunch.checked,
    stopAllOnExit: f.stopAllOnExit.checked,
    minimizeToTray: f.minimizeToTray.checked,
    ssl: f.ssl.checked,
    sslPort: +f.sslPort.value,
    checkUpdates: f.checkUpdates.checked,
  };
  const ok = await run(async () => { await api.SaveConfig(cfg); return true; }, ev.submitter);
  if (ok) { toast('Settings saved'); await refresh(); }
});

$('#btnTrust').addEventListener('click', async (ev) => {
  const st = await run(() => api.TrustCA(), ev.currentTarget);
  if (st) { state.ssl = st; renderSSL(st); if (st.trusted) toast('Certificate trusted. Restart your browser if needed.'); }
});

$('#btnInstall').addEventListener('click', async () => {
  const ids = [...document.querySelectorAll('#components input:checked')].map((i) => i.value);
  if (!ids.length) return toast('Tick at least one item to install', true);
  ids.forEach((id) => delete progress[id]);
  installing = true;
  renderComponents();
  try {
    await api.InstallComponents(ids);
    toast('Installation finished. Click Start All to begin.');
  } catch (e) {
    toast(String(e), true);
  } finally {
    installing = false;
    await loadComponents();
    const s = await api.GetState();
    state.php = s.php; state.currentPhp = s.currentPhp; renderPHP();
  }
});

$('#btnCancelInstall').addEventListener('click', () => api.CancelInstall());

// ---------- LokalKit updates ----------

let updateInfo = null;

function showUpdate(info) {
  updateInfo = info;
  const b = $('#updateBanner');
  if (!info || !info.available) { b.hidden = true; return; }
  $('#ubTitle').textContent = `LokalKit ${info.latest} is available`;
  $('#ubSub').textContent = `You have ${info.current}. Updating restarts LokalKit; running services start again by themselves.`;
  $('#ubNotes').hidden = !info.pageUrl;
  b.hidden = false;
}

function renderUpdateStatus(info) {
  const el = $('#updStatus');
  if (!info) { el.textContent = `Version ${state.version}`; return; }
  el.className = 'muted-text' + (info.error ? ' warn' : info.available ? '' : ' ok');
  el.textContent = info.error ? `Version ${info.current} · couldn't check: ${info.error}`
    : info.available ? `Version ${info.current} · ${info.latest} is available`
    : `Version ${info.current} · you have the latest version`;
}

$('#ubNotes').addEventListener('click', () => updateInfo?.pageUrl && api.OpenURL(updateInfo.pageUrl));
$('#ubLater').addEventListener('click', () => ($('#updateBanner').hidden = true));
$('#ubInstall').addEventListener('click', async (ev) => {
  const btn = ev.currentTarget;
  btn.textContent = 'Downloading…';
  const ok = await run(async () => { await api.InstallUpdate(); return true; }, btn);
  if (!ok) btn.textContent = 'Update & restart';
  else btn.textContent = 'Restarting…';
});
$('#btnCheckUpdate').addEventListener('click', async (ev) => {
  const info = await run(() => api.CheckForUpdate(true), ev.currentTarget);
  if (info) { renderUpdateStatus(info); showUpdate(info); }
});
on('update-available', (info) => { showUpdate(info); renderUpdateStatus(info); });
on('update-progress', (p) => {
  const pct = p.total > 0 ? Math.round((p.received / p.total) * 100) : 0;
  $('#ubInstall').textContent = pct >= 100 ? 'Installing…' : `Downloading ${pct}%`;
});

// ---------- databases tab ----------

async function loadDatabases() {
  const v = await api.GetDatabases();
  $('#dbPort').textContent = state.config?.mysqlPort || 3306;
  $('#dbStopped').hidden = v.running;
  $('#dbBody').hidden = !v.running;
  $('#btnDbStart').hidden = !v.installed;
  $('#dbStoppedHint').textContent = v.installed ? 'Start it to see and manage your databases.' : 'Install MySQL from the Install tab first.';
  if (!v.running) return;
  const dbs = v.databases || [];
  $('#dbCount').textContent = dbs.length;
  const missing = v.missing || [];
  $('#dbMissing').innerHTML = missing.length ? `<div class="card db-missing">
      <b>Projects pointing to a database that doesn't exist</b>
      ${missing.map((d) => `<div class="db-missing-row"><span><b>${esc(d.project)}</b> expects <code>${esc(d.database)}</code></span>
        <button class="btn small primary" data-createdb="${esc(d.project)}">${icon('plus')}Create${d.laravel ? ' + migrate' : ''}</button></div>`).join('')}
      <div class="muted-text">Have it in Laragon? Use Projects → Import existing… → Databases to copy it with its data.</div>
    </div>` : '';
  $('#dbList2').innerHTML = dbs.length ? dbs.map((d) => `
    <div class="card proj">
      <div class="proj-icon db">${icon('database')}</div>
      <div class="proj-main">
        <div class="proj-name"><span class="db-name">${esc(d.name)}</span>${(d.projects || []).map((p) => `<span class="badge">${esc(p)}</span>`).join('')}</div>
        <div class="db-meta">${d.tables} table${d.tables === 1 ? '' : 's'} · ${d.sizeMb < 0.1 ? '< 0.1' : d.sizeMb} MB${d.projects?.length ? '' : ' · not used by a project'}</div>
      </div>
      <div class="proj-btns">
        <button class="btn small" data-db-export="${esc(d.name)}" title="Save a .sql backup">${icon('download')}Export</button>
        <button class="btn small" data-db-import="${esc(d.name)}" title="Run a .sql file into this database">${icon('import')}Import .sql</button>
        <button class="btn small danger-ghost" data-db-drop="${esc(d.name)}" title="Delete this database">${icon('x')}Delete</button>
      </div>
    </div>`).join('') : '<div class="empty">No databases yet. Create one above.</div>';
}

$('#btnDbStart').addEventListener('click', (ev) => run(async () => { await api.StartService('mysql'); await loadDatabases(); refreshProjects(); }, ev.currentTarget));

$('#newDb').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const name = $('#ndbName').value.trim();
  if (!name) return $('#ndbName').focus();
  const ok = await run(async () => { await api.CreateDatabase(name); return true; }, ev.submitter);
  if (ok) { $('#ndbName').value = ''; toast(`Database ${name} created`); loadDatabases(); }
});
$('#ndbName').addEventListener('input', (e) => { e.target.value = e.target.value.replace(/[^A-Za-z0-9_]/g, '_'); });

// ---------- import existing projects ----------

let impRoot = ''; // tool root (e.g. C:\laragon) when the folder is its www

async function showImport() {
  $('#importPanel').hidden = false;
  if (!$('#impDir').value) {
    const lar = await api.DetectLaragon();
    if (lar) await scanImport(lar + (lar.includes('\\') ? '\\' : '/') + 'www');
    else $('#impList').innerHTML = '<div class="muted-text">Laragon was not found automatically. Click Browse… and choose the folder that holds your projects.</div>';
  }
}

async function scanImport(dir) {
  $('#impDir').value = dir;
  const cands = await run(() => api.ScanImport(dir));
  if (!cands) return;
  $('#impList').innerHTML = cands.length ? cands.map((c) => {
    const note = c.added ? '<span class="imp-note ok">✓ already added</span>'
      : c.conflict ? `<span class="imp-note err">${esc(c.conflict)}</span>`
      : `<span class="imp-note">${esc(c.host)}</span>`;
    return `<label class="imp-row"><input type="checkbox" value="${esc(c.path)}" ${!c.added && !c.conflict ? 'checked' : 'disabled'}/>
      <div><div>${esc(c.name)} ${c.kind !== 'php' ? `<span class="badge ${c.kind}">${c.kind}</span>` : ''}</div><div class="muted-text">${esc(c.path)}</div></div>${note}</label>`;
  }).join('') : '<div class="muted-text">No project folders found here.</div>';
  // Databases: when the folder is a tool's "www", look for its MySQL data.
  impRoot = /[\\/]www[\\/]?$/i.test(dir) ? dir.replace(/[\\/]www[\\/]?$/i, '') : '';
  $('#impDbs').hidden = true;
  if (impRoot) {
    try {
      const info = await api.LegacyDatabases(impRoot);
      if (info.databases && info.databases.length) {
        $('#impDbHint').textContent = `Found in ${info.dataDir}`;
        $('#dbList').innerHTML = info.databases.map((d) => `<label class="imp-row"><input type="checkbox" value="${esc(d)}" checked/><div>${esc(d)}</div><span></span></label>`).join('');
        $('#impDbs').hidden = false;
      }
    } catch (e) { /* no MySQL data there */ }
  }
}

$('#btnShowImport').addEventListener('click', showImport);
$('#btnHideImport').addEventListener('click', () => ($('#importPanel').hidden = true));
$('#btnBrowse').addEventListener('click', async () => {
  const dir = await run(() => api.PickFolder('Choose the folder that holds your projects (e.g. C:\\laragon\\www)'));
  if (dir) scanImport(dir);
});
$('#btnImport').addEventListener('click', async (ev) => {
  const paths = [...document.querySelectorAll('#impList input:checked')].map((i) => i.value);
  if (!paths.length) return toast('Tick at least one project', true);
  const added = await run(() => api.ImportProjects(paths), ev.currentTarget);
  if (added) {
    await refreshProjects();
    toast(`Added ${added.length} project(s)`);
    scanImport($('#impDir').value);
  }
});
$('#btnImportDb').addEventListener('click', async (ev) => {
  const dbs = [...document.querySelectorAll('#dbList input:checked')].map((i) => i.value);
  if (!dbs.length) return toast('Tick at least one database', true);
  const btn = ev.currentTarget;
  btn.textContent = 'Copying…';
  const ok = await run(async () => { await api.ImportDatabases(impRoot, dbs); return true; }, btn);
  btn.textContent = 'Copy selected databases';
  if (ok) toast(`Copied ${dbs.length} database(s) into LokalKit's MySQL`);
});

on('projects', () => refreshProjects());

on('install-progress', (p) => {
  progress[p.id] = p;
  renderComponents();
  if (phpInstalling && p.id === 'php') {
    const pct = p.phase === 'downloading' && p.total > 0 ? ` ${Math.round((p.received / p.total) * 100)}%` : '';
    $('#phpUpdate').textContent = { downloading: `Downloading${pct}…`, verifying: 'Verifying…', extracting: 'Installing…', done: 'Switching…' }[p.phase] || $('#phpUpdate').textContent;
  }
});

on('services', (s) => {
  const wasUp = state.services?.find((x) => x.id === 'mysql')?.running;
  renderServices(s);
  renderProjects(state.projects || []);
  const nowUp = s.find((x) => x.id === 'mysql')?.running;
  if (wasUp !== nowUp) {
    refreshProjects();
    if (!$('#tab-databases').hidden) loadDatabases();
  }
});
on('log', addLog);

refresh();
