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
    const label = s.busy ? '…' : s.running ? 'Henti' : 'Mula';
    const detail = s.installed
      ? `<div class="svc-name">${esc(s.name)}<small>${esc(s.version)}</small></div>`
      : `<div><div class="svc-name">${esc(s.name)}</div><div class="svc-missing">Tidak dijumpai. Letak di <code>${esc(s.hint)}</code></div></div>`;
    const button = s.installed
      ? `<button class="btn ${s.running ? 'danger' : ''}" data-svc="${s.id}" ${s.busy ? 'disabled' : ''}>${label}</button>`
      : `<button class="btn small ghost" data-folder="bin">Buka bin</button>`;
    return `<div class="${cls}"><span class="dot"></span>${detail}<span class="svc-port">${esc(s.ports)}</span>${button}</div>`;
  }).join('');

  const anyRunning = services.some((s) => s.running);
  const anyBusy = services.some((s) => s.busy);
  const all = $('#btnAll');
  all.textContent = anyRunning ? '■ Henti Semua' : '▶ Mula Semua';
  all.disabled = anyBusy;
  all.dataset.mode = anyRunning ? 'stop' : 'start';
}

function renderProjects(projects) {
  $('#projCount').textContent = projects.length || '';
  const apacheUp = state.services?.find((s) => s.id === 'apache')?.running;
  if (!projects.length) {
    $('#projects').innerHTML = `<div class="empty">Belum ada projek. Cipta projek baru di atas, atau letak folder dalam <b>www</b>.</div>`;
    return;
  }
  $('#projects').innerHTML = projects.map((p) => `
    <div class="proj">
      <div class="proj-main">
        <div class="proj-name">${esc(p.name)}${p.kind !== 'php' ? `<span class="badge ${p.kind}">${p.kind}</span>` : ''}</div>
        <a class="proj-url" data-url="${esc(p.url)}" title="${apacheUp ? 'Buka dalam pelayar' : 'Mulakan Apache dahulu'}">${esc(p.url)}</a>
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
    sel.innerHTML = '<option>tiada</option>';
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
  } else if (t.dataset.folder) {
    run(() => api.OpenFolder(t.dataset.folder));
  } else if (t.dataset.term) {
    run(() => api.OpenTerminal(t.dataset.term));
  } else if (t.dataset.url) {
    api.OpenURL(t.dataset.url);
  } else if (t.dataset.tab) {
    document.querySelectorAll('.tab').forEach((b) => b.classList.toggle('active', b === t));
    document.querySelectorAll('.panel').forEach((p) => (p.hidden = p.id !== 'tab-' + t.dataset.tab));
    if (t.dataset.tab === 'logs') $('#logs').scrollTop = $('#logs').scrollHeight;
  } else if (t.id === 'clearLog') {
    $('#logs').innerHTML = '';
  } else if (t.dataset.act) {
    const acts = {
      web: () => api.OpenWeb(),
      db: () => api.OpenDatabase(),
      term: () => api.OpenTerminal(''),
      www: () => api.OpenFolder('www'),
      mail: () => api.OpenMailpit(),
      reload: async () => { await api.Reload(); await refreshProjects(); toast('Virtual host dikemas kini'); },
    };
    run(acts[t.dataset.act], t);
  }
});

async function refreshProjects() {
  const s = await api.GetState();
  state.projects = s.projects;
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
    toast(`Projek ${p.name} dicipta → ${p.url || ''}`);
  }
});

// Only allow characters valid in a domain name while typing.
$('#npName').addEventListener('input', (e) => {
  e.target.value = e.target.value.toLowerCase().replace(/[\s_]+/g, '-').replace(/[^a-z0-9-]/g, '');
});

$('#phpSelect').addEventListener('change', async (e) => {
  const cfg = { ...state.config, phpVersion: e.target.value };
  const ok = await run(async () => { await api.SaveConfig(cfg); return true; });
  if (ok) { state.config = cfg; toast('Versi PHP ditukar'); } else renderPHP();
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
  };
  const ok = await run(async () => { await api.SaveConfig(cfg); return true; }, ev.submitter);
  if (ok) { toast('Tetapan disimpan'); await refresh(); }
});

on('services', (s) => { renderServices(s); renderProjects(state.projects || []); });
on('log', addLog);

refresh();
