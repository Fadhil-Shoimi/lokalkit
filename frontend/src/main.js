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

// ---------- rendering ----------

function renderServices(services) {
  state.services = services;
  $('#services').innerHTML = services.map((s) => {
    const cls = ['svc', s.running && 'running', s.busy && 'busy'].filter(Boolean).join(' ');
    const label = s.busy ? '…' : s.running ? 'Stop' : 'Start';
    const detail = s.installed
      ? `<div class="svc-name">${esc(s.name)}<small>${esc(s.version)}</small></div>`
      : `<div><div class="svc-name">${esc(s.name)}</div><div class="svc-missing">Not found. Put it in <code>${esc(s.hint)}</code></div></div>`;
    const button = s.installed
      ? `<button class="btn ${s.running ? 'danger' : ''}" data-svc="${s.id}" ${s.busy ? 'disabled' : ''}>${label}</button>`
      : `<button class="btn small" data-tab-go="install">Install</button>`;
    return `<div class="${cls}"><span class="dot"></span>${detail}<span class="svc-port">${esc(s.ports)}</span>${button}</div>`;
  }).join('');

  const anyRunning = services.some((s) => s.running);
  const anyBusy = services.some((s) => s.busy);
  const all = $('#btnAll');
  all.textContent = anyRunning ? '■ Stop All' : '▶ Start All';
  all.disabled = anyBusy;
  all.dataset.mode = anyRunning ? 'stop' : 'start';
}

function renderProjects(projects) {
  $('#projCount').textContent = projects.length || '';
  $('#projCount').hidden = !projects.length;
  const apacheUp = state.services?.find((s) => s.id === 'apache')?.running;
  if (!projects.length) {
    $('#projects').innerHTML = `<div class="empty">No projects yet. Create one above, or put a folder in <b>www</b>.</div>`;
    return;
  }
  $('#projects').innerHTML = projects.map((p) => `
    <div class="proj">
      <div class="proj-main">
        <div class="proj-name">${esc(p.name)}${p.kind !== 'php' ? `<span class="badge ${p.kind}">${p.kind}</span>` : ''}</div>
        <div class="proj-links">
          ${p.https
            ? `<a class="proj-url" data-url="${esc(p.https)}" title="${apacheUp ? 'Open in browser' : 'Start Apache first'}">${esc(p.https)}</a>
               <a class="proj-url alt" data-url="${esc(p.url)}">http</a>`
            : `<a class="proj-url" data-url="${esc(p.url)}" title="${apacheUp ? 'Open in browser' : 'Start Apache first'}">${esc(p.url)}</a>`}
        </div>
        ${(state.missingHosts || []).includes(p.host) ? `<div class="proj-warn">⚠ Not in the Windows hosts file yet, so browsers can't find it. <button class="linkish" data-fixhosts="1">Fix now</button></div>` : ''}
      </div>
      <div class="proj-btns">
        <button class="btn small" data-folder="${esc(p.name)}">Folder</button>
        <button class="btn small" data-term="${esc(p.path)}">Terminal</button>
      </div>
    </div>`).join('');
}

function renderPHP() {
  const sel = $('#phpSelect');
  if (!state.php?.length) {
    sel.innerHTML = '<option>not found</option>';
    sel.disabled = true;
    return;
  }
  sel.disabled = false;
  sel.innerHTML = state.php.map((p) =>
    `<option value="${esc(p.name)}" ${p.name === state.currentPhp ? 'selected' : ''}>${esc(p.version)}${p.ts ? '' : ' (NTS)'}</option>`).join('');
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
    return `<label class="comp">
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
  renderProjects(s.projects);
}

$('#newProject').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const name = $('#npName').value.trim();
  if (!name) return $('#npName').focus();
  const btn = ev.submitter;
  const p = await run(() => api.CreateProject(name, $('#npKind').value), btn);
  if (p) {
    $('#npName').value = '';
    await refreshProjects();
    toast(`Project ${p.name} created → ${p.https || p.url || ''}`);
  }
});

// Only allow characters valid in a domain name while typing.
$('#npName').addEventListener('input', (e) => {
  e.target.value = e.target.value.toLowerCase().replace(/[\s_]+/g, '-').replace(/[^a-z0-9-]/g, '');
});

$('#phpSelect').addEventListener('change', async (e) => {
  const cfg = { ...state.config, phpVersion: e.target.value };
  const ok = await run(async () => { await api.SaveConfig(cfg); return true; });
  if (ok) { state.config = cfg; toast('PHP version changed'); } else renderPHP();
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

on('projects', () => refreshProjects());

on('install-progress', (p) => { progress[p.id] = p; renderComponents(); });

on('services', (s) => { renderServices(s); renderProjects(state.projects || []); });
on('log', addLog);

refresh();
