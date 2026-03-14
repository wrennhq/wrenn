package api

import (
	"fmt"
	"net/http"
)

func serveTestUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, testUIHTML)
}

const testUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Wrenn Sandbox — Test Console</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: 'Menlo', 'Consolas', 'JetBrains Mono', monospace;
    font-size: 13px;
    background: #0f1211;
    color: #c8c4bc;
    padding: 16px;
  }
  h1 { font-size: 18px; color: #e8e5df; margin-bottom: 12px; }
  h2 { font-size: 14px; color: #89a785; margin: 16px 0 8px; border-bottom: 1px solid #262c2a; padding-bottom: 4px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
  .panel {
    background: #151918;
    border: 1px solid #262c2a;
    border-radius: 8px;
    padding: 12px;
  }
  .full { grid-column: 1 / -1; }
  label { display: block; color: #8a867f; margin: 6px 0 2px; font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; }
  input, select {
    width: 100%;
    background: #1b201e;
    border: 1px solid #262c2a;
    color: #e8e5df;
    padding: 6px 8px;
    border-radius: 4px;
    font-family: inherit;
    font-size: 13px;
  }
  input:focus, select:focus { outline: none; border-color: #5e8c58; }
  .btn-row { display: flex; gap: 6px; margin-top: 8px; flex-wrap: wrap; }
  button {
    padding: 6px 14px;
    border: 1px solid #262c2a;
    border-radius: 4px;
    font-family: inherit;
    font-size: 12px;
    cursor: pointer;
    background: #1b201e;
    color: #c8c4bc;
    transition: all 0.15s;
  }
  button:hover { border-color: #5e8c58; color: #e8e5df; }
  .btn-green { background: #2a3d28; border-color: #5e8c58; color: #89a785; }
  .btn-green:hover { background: #3a5035; }
  .btn-red { background: #3d2828; border-color: #b35544; color: #c27b6d; }
  .btn-red:hover { background: #4d3030; }
  .btn-amber { background: #3d3428; border-color: #9e7c2e; color: #c8a84e; }
  .btn-amber:hover { background: #4d4030; }
  .btn-blue { background: #28343d; border-color: #3d7aac; color: #6da0cc; }
  .btn-blue:hover { background: #304050; }
  table { width: 100%; border-collapse: collapse; margin-top: 8px; }
  th { text-align: left; font-size: 11px; color: #8a867f; text-transform: uppercase; letter-spacing: 0.05em; padding: 4px 8px; border-bottom: 1px solid #262c2a; }
  td { padding: 6px 8px; border-bottom: 1px solid #1b201e; }
  tr:hover td { background: #1b201e; }
  .status { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 600; }
  .status-running { background: rgba(94,140,88,0.15); color: #89a785; }
  .status-paused { background: rgba(158,124,46,0.15); color: #c8a84e; }
  .status-pending { background: rgba(61,122,172,0.15); color: #6da0cc; }
  .status-stopped { background: rgba(138,134,127,0.15); color: #8a867f; }
  .status-error { background: rgba(179,85,68,0.15); color: #c27b6d; }
  .status-hibernated { background: rgba(61,122,172,0.15); color: #6da0cc; }
  .log {
    background: #0f1211;
    border: 1px solid #262c2a;
    border-radius: 4px;
    padding: 8px;
    max-height: 300px;
    overflow-y: auto;
    margin-top: 8px;
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-all;
  }
  .log-entry { margin-bottom: 4px; }
  .log-time { color: #5f5c57; }
  .log-ok { color: #89a785; }
  .log-err { color: #c27b6d; }
  .log-info { color: #6da0cc; }
  .exec-output {
    background: #0f1211;
    border: 1px solid #262c2a;
    border-radius: 4px;
    padding: 8px;
    max-height: 200px;
    overflow-y: auto;
    margin-top: 8px;
    font-size: 12px;
    white-space: pre-wrap;
  }
  .clickable { cursor: pointer; color: #89a785; text-decoration: underline; }
  .clickable:hover { color: #aacdaa; }
  .auth-badge {
    display: inline-block;
    padding: 2px 8px;
    border-radius: 10px;
    font-size: 11px;
    font-weight: 600;
    margin-left: 8px;
  }
  .auth-badge.authed { background: rgba(94,140,88,0.15); color: #89a785; }
  .auth-badge.unauthed { background: rgba(179,85,68,0.15); color: #c27b6d; }
  .key-display {
    background: #1b201e;
    border: 1px solid #5e8c58;
    border-radius: 4px;
    padding: 8px;
    margin-top: 8px;
    font-size: 12px;
    word-break: break-all;
    color: #89a785;
  }
</style>
</head>
<body>

<h1>Wrenn Sandbox Test Console <span id="auth-status" class="auth-badge unauthed">not authenticated</span></h1>

<div class="grid">
  <!-- Auth Panel -->
  <div class="panel">
    <h2>Authentication</h2>
    <label>Email</label>
    <input type="email" id="auth-email" value="" placeholder="user@example.com">
    <label>Password</label>
    <input type="password" id="auth-password" value="" placeholder="min 8 characters">
    <div class="btn-row">
      <button class="btn-green" onclick="signup()">Sign Up</button>
      <button class="btn-blue" onclick="login()">Log In</button>
      <button class="btn-red" onclick="logout()">Log Out</button>
    </div>
    <div id="auth-info" style="margin-top:8px;font-size:12px;color:#5f5c57"></div>
  </div>

  <!-- API Keys Panel -->
  <div class="panel">
    <h2>API Keys</h2>
    <label>Key Name</label>
    <input type="text" id="key-name" value="" placeholder="my-api-key">
    <div class="btn-row">
      <button class="btn-green" onclick="createAPIKey()">Create Key</button>
      <button onclick="listAPIKeys()">Refresh</button>
    </div>
    <div id="new-key-display" style="display:none" class="key-display"></div>
    <div id="api-keys-table"></div>
    <label style="margin-top:12px">Active API Key</label>
    <input type="text" id="active-api-key" value="" placeholder="wrn_...">
  </div>

  <!-- Create Sandbox -->
  <div class="panel">
    <h2>Create Sandbox</h2>
    <label>Template</label>
    <input type="text" id="create-template" value="minimal" placeholder="minimal or snapshot name">
    <label>vCPUs</label>
    <input type="number" id="create-vcpus" value="1" min="1" max="8">
    <label>Memory (MB)</label>
    <input type="number" id="create-memory" value="512" min="128" max="8192">
    <label>Timeout (sec, 0 = no auto-pause)</label>
    <input type="number" id="create-timeout" value="0" min="0">
    <div class="btn-row">
      <button class="btn-green" onclick="createSandbox()">Create</button>
    </div>
  </div>

  <!-- Snapshot Management -->
  <div class="panel">
    <h2>Create Snapshot</h2>
    <label>Sandbox ID</label>
    <input type="text" id="snap-sandbox-id" placeholder="sb-xxxxxxxx">
    <label>Snapshot Name (optional)</label>
    <input type="text" id="snap-name" placeholder="auto-generated if empty">
    <div class="btn-row">
      <button class="btn-amber" onclick="createSnapshot()">Create Snapshot</button>
      <label style="display:inline-flex;align-items:center;margin:0;font-size:12px;text-transform:none;letter-spacing:0">
        <input type="checkbox" id="snap-overwrite" style="width:auto;margin-right:4px"> Overwrite
      </label>
    </div>

    <h2>Snapshots / Templates</h2>
    <div class="btn-row">
      <button onclick="listSnapshots()">Refresh</button>
    </div>
    <div id="snapshots-table"></div>
  </div>

  <!-- Execute Command -->
  <div class="panel">
    <h2>Execute Command</h2>
    <label>Sandbox ID</label>
    <input type="text" id="exec-sandbox-id" placeholder="sb-xxxxxxxx">
    <label>Command</label>
    <input type="text" id="exec-cmd" value="/bin/sh" placeholder="/bin/sh">
    <label>Args (comma separated)</label>
    <input type="text" id="exec-args" value="-c,uname -a" placeholder="-c,echo hello">
    <div class="btn-row">
      <button class="btn-green" onclick="execCmd()">Run</button>
    </div>
    <div id="exec-output" class="exec-output" style="display:none"></div>
  </div>

  <!-- Activity Log -->
  <div class="panel">
    <h2>Activity Log</h2>
    <div id="log" class="log"></div>
  </div>

  <!-- Sandboxes List -->
  <div class="panel full">
    <h2>Sandboxes</h2>
    <div class="btn-row">
      <button onclick="listSandboxes()">Refresh</button>
      <label style="display:inline-flex;align-items:center;margin:0;font-size:12px;text-transform:none;letter-spacing:0">
        <input type="checkbox" id="auto-refresh" style="width:auto;margin-right:4px"> Auto-refresh (5s)
      </label>
    </div>
    <div id="sandboxes-table"></div>
  </div>
</div>

<script>
const API = '';
let jwtToken = '';
let activeAPIKey = '';

function log(msg, level) {
  const el = document.getElementById('log');
  const t = new Date().toLocaleTimeString();
  const cls = level === 'ok' ? 'log-ok' : level === 'err' ? 'log-err' : 'log-info';
  el.innerHTML = '<div class="log-entry"><span class="log-time">' + t + '</span> <span class="' + cls + '">' + esc(msg) + '</span></div>' + el.innerHTML;
}

function esc(s) {
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

function updateAuthStatus() {
  const badge = document.getElementById('auth-status');
  const info = document.getElementById('auth-info');
  if (jwtToken) {
    badge.textContent = 'authenticated';
    badge.className = 'auth-badge authed';
    try {
      const payload = JSON.parse(atob(jwtToken.split('.')[1]));
      info.textContent = 'User: ' + payload.email + ' | Team: ' + payload.team_id;
    } catch(e) {
      info.textContent = 'Token set';
    }
  } else {
    badge.textContent = 'not authenticated';
    badge.className = 'auth-badge unauthed';
    info.textContent = '';
  }
}

// API call with appropriate auth headers.
async function api(method, path, body, authType) {
  const opts = { method, headers: {} };
  if (authType === 'jwt' && jwtToken) {
    opts.headers['Authorization'] = 'Bearer ' + jwtToken;
  } else if (authType === 'apikey') {
    const key = document.getElementById('active-api-key').value;
    if (!key) {
      throw new Error('No API key set. Create one first and paste it in the Active API Key field.');
    }
    opts.headers['X-API-Key'] = key;
  }
  if (body) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(API + path, opts);
  if (resp.status === 204) return null;
  const data = await resp.json();
  if (resp.status >= 300) {
    throw new Error(data.error ? data.error.message : resp.statusText);
  }
  return data;
}

function statusBadge(s) {
  return '<span class="status status-' + s + '">' + s + '</span>';
}

// --- Auth ---

async function signup() {
  const email = document.getElementById('auth-email').value;
  const password = document.getElementById('auth-password').value;
  if (!email || !password) { log('Email and password required', 'err'); return; }
  log('Signing up as ' + email + '...', 'info');
  try {
    const data = await api('POST', '/v1/auth/signup', { email, password });
    jwtToken = data.token;
    updateAuthStatus();
    log('Signed up! User: ' + data.user_id + ', Team: ' + data.team_id, 'ok');
  } catch (e) {
    log('Signup failed: ' + e.message, 'err');
  }
}

async function login() {
  const email = document.getElementById('auth-email').value;
  const password = document.getElementById('auth-password').value;
  if (!email || !password) { log('Email and password required', 'err'); return; }
  log('Logging in as ' + email + '...', 'info');
  try {
    const data = await api('POST', '/v1/auth/login', { email, password });
    jwtToken = data.token;
    updateAuthStatus();
    log('Logged in! User: ' + data.user_id + ', Team: ' + data.team_id, 'ok');
    listAPIKeys();
  } catch (e) {
    log('Login failed: ' + e.message, 'err');
  }
}

function logout() {
  jwtToken = '';
  updateAuthStatus();
  log('Logged out', 'info');
}

// --- API Keys ---

async function createAPIKey() {
  if (!jwtToken) { log('Log in first to create API keys', 'err'); return; }
  const name = document.getElementById('key-name').value || 'Unnamed API Key';
  log('Creating API key "' + name + '"...', 'info');
  try {
    const data = await api('POST', '/v1/api-keys', { name }, 'jwt');
    const display = document.getElementById('new-key-display');
    display.style.display = 'block';
    display.textContent = 'New key (copy now — shown only once): ' + data.key;
    document.getElementById('active-api-key').value = data.key;
    log('API key created: ' + data.key_prefix, 'ok');
    listAPIKeys();
  } catch (e) {
    log('Create API key failed: ' + e.message, 'err');
  }
}

async function listAPIKeys() {
  if (!jwtToken) return;
  try {
    const data = await api('GET', '/v1/api-keys', null, 'jwt');
    renderAPIKeys(data);
  } catch (e) {
    log('List API keys failed: ' + e.message, 'err');
  }
}

function renderAPIKeys(keys) {
  if (!keys || keys.length === 0) {
    document.getElementById('api-keys-table').innerHTML = '<p style="color:#5f5c57;margin-top:8px">No API keys</p>';
    return;
  }
  let html = '<table><thead><tr><th>Name</th><th>Prefix</th><th>Created</th><th>Last Used</th><th>Actions</th></tr></thead><tbody>';
  for (const k of keys) {
    html += '<tr>';
    html += '<td>' + esc(k.name) + '</td>';
    html += '<td style="font-size:11px">' + esc(k.key_prefix) + '</td>';
    html += '<td>' + new Date(k.created_at).toLocaleString() + '</td>';
    html += '<td>' + (k.last_used ? new Date(k.last_used).toLocaleString() : '-') + '</td>';
    html += '<td><button class="btn-red" onclick="deleteAPIKey(\'' + k.id + '\')">Delete</button></td>';
    html += '</tr>';
  }
  html += '</tbody></table>';
  document.getElementById('api-keys-table').innerHTML = html;
}

async function deleteAPIKey(id) {
  log('Deleting API key ' + id + '...', 'info');
  try {
    await api('DELETE', '/v1/api-keys/' + id, null, 'jwt');
    log('Deleted API key ' + id, 'ok');
    listAPIKeys();
  } catch (e) {
    log('Delete API key failed: ' + e.message, 'err');
  }
}

// --- Sandboxes ---

async function listSandboxes() {
  try {
    const data = await api('GET', '/v1/sandboxes', null, 'apikey');
    renderSandboxes(data);
  } catch (e) {
    log('List sandboxes failed: ' + e.message, 'err');
  }
}

function renderSandboxes(sandboxes) {
  if (!sandboxes || sandboxes.length === 0) {
    document.getElementById('sandboxes-table').innerHTML = '<p style="color:#5f5c57;margin-top:8px">No sandboxes</p>';
    return;
  }
  let html = '<table><thead><tr><th>ID</th><th>Status</th><th>Template</th><th>vCPUs</th><th>Mem</th><th>TTL</th><th>Host IP</th><th>Created</th><th>Actions</th></tr></thead><tbody>';
  for (const sb of sandboxes) {
    html += '<tr>';
    html += '<td class="clickable" onclick="useSandbox(\'' + sb.id + '\')">' + sb.id + '</td>';
    html += '<td>' + statusBadge(sb.status) + '</td>';
    html += '<td>' + esc(sb.template) + '</td>';
    html += '<td>' + sb.vcpus + '</td>';
    html += '<td>' + sb.memory_mb + 'MB</td>';
    html += '<td>' + (sb.timeout_sec ? sb.timeout_sec + 's' : '-') + '</td>';
    html += '<td>' + (sb.host_ip || '-') + '</td>';
    html += '<td>' + new Date(sb.created_at).toLocaleTimeString() + '</td>';
    html += '<td><div class="btn-row">';
    if (sb.status === 'running') {
      html += '<button class="btn-blue" onclick="pingSandbox(\'' + sb.id + '\')">Ping</button>';
      html += '<button class="btn-amber" onclick="pauseSandbox(\'' + sb.id + '\')">Pause</button>';
      html += '<button class="btn-red" onclick="destroySandbox(\'' + sb.id + '\')">Destroy</button>';
    } else if (sb.status === 'paused') {
      html += '<button class="btn-green" onclick="resumeSandbox(\'' + sb.id + '\')">Resume</button>';
      html += '<button class="btn-red" onclick="destroySandbox(\'' + sb.id + '\')">Destroy</button>';
    } else {
      html += '<button class="btn-red" onclick="destroySandbox(\'' + sb.id + '\')">Destroy</button>';
    }
    html += '</div></td>';
    html += '</tr>';
  }
  html += '</tbody></table>';
  document.getElementById('sandboxes-table').innerHTML = html;
}

function useSandbox(id) {
  document.getElementById('exec-sandbox-id').value = id;
  document.getElementById('snap-sandbox-id').value = id;
}

async function createSandbox() {
  const template = document.getElementById('create-template').value;
  const vcpus = parseInt(document.getElementById('create-vcpus').value);
  const memory_mb = parseInt(document.getElementById('create-memory').value);
  const timeout_sec = parseInt(document.getElementById('create-timeout').value);
  log('Creating sandbox (template=' + template + ', vcpus=' + vcpus + ', mem=' + memory_mb + 'MB)...', 'info');
  try {
    const data = await api('POST', '/v1/sandboxes', { template, vcpus, memory_mb, timeout_sec }, 'apikey');
    log('Created sandbox ' + data.id + ' [' + data.status + ']', 'ok');
    listSandboxes();
  } catch (e) {
    log('Create failed: ' + e.message, 'err');
  }
}

async function pauseSandbox(id) {
  log('Pausing ' + id + '...', 'info');
  try {
    await api('POST', '/v1/sandboxes/' + id + '/pause', null, 'apikey');
    log('Paused ' + id, 'ok');
    listSandboxes();
  } catch (e) {
    log('Pause failed: ' + e.message, 'err');
  }
}

async function resumeSandbox(id) {
  log('Resuming ' + id + '...', 'info');
  try {
    await api('POST', '/v1/sandboxes/' + id + '/resume', null, 'apikey');
    log('Resumed ' + id, 'ok');
    listSandboxes();
  } catch (e) {
    log('Resume failed: ' + e.message, 'err');
  }
}

async function destroySandbox(id) {
  log('Destroying ' + id + '...', 'info');
  try {
    await api('DELETE', '/v1/sandboxes/' + id, null, 'apikey');
    log('Destroyed ' + id, 'ok');
    listSandboxes();
  } catch (e) {
    log('Destroy failed: ' + e.message, 'err');
  }
}

async function pingSandbox(id) {
  log('Pinging ' + id + '...', 'info');
  try {
    await api('POST', '/v1/sandboxes/' + id + '/ping', null, 'apikey');
    log('Pinged ' + id + ' — inactivity timer reset', 'ok');
  } catch (e) {
    log('Ping failed: ' + e.message, 'err');
  }
}

// --- Exec ---

async function execCmd() {
  const sandboxId = document.getElementById('exec-sandbox-id').value;
  const cmd = document.getElementById('exec-cmd').value;
  const argsStr = document.getElementById('exec-args').value;
  const args = argsStr ? argsStr.split(',').map(s => s.trim()) : [];

  if (!sandboxId) { log('No sandbox ID for exec', 'err'); return; }

  const out = document.getElementById('exec-output');
  out.style.display = 'block';
  out.textContent = 'Running...';

  log('Exec on ' + sandboxId + ': ' + cmd + ' ' + args.join(' '), 'info');
  try {
    const data = await api('POST', '/v1/sandboxes/' + sandboxId + '/exec', { cmd, args }, 'apikey');
    let text = '';
    if (data.stdout) text += data.stdout;
    if (data.stderr) text += '\n[stderr]\n' + data.stderr;
    text += '\n[exit_code=' + data.exit_code + ', duration=' + data.duration_ms + 'ms]';
    out.textContent = text;
    log('Exec completed (exit=' + data.exit_code + ')', data.exit_code === 0 ? 'ok' : 'err');
  } catch (e) {
    out.textContent = 'Error: ' + e.message;
    log('Exec failed: ' + e.message, 'err');
  }
}

// --- Snapshots ---

async function createSnapshot() {
  const sandbox_id = document.getElementById('snap-sandbox-id').value;
  const name = document.getElementById('snap-name').value;
  const overwrite = document.getElementById('snap-overwrite').checked;

  if (!sandbox_id) { log('No sandbox ID for snapshot', 'err'); return; }

  const body = { sandbox_id };
  if (name) body.name = name;

  const qs = overwrite ? '?overwrite=true' : '';
  log('Creating snapshot from ' + sandbox_id + (name ? ' as "' + name + '"' : '') + '...', 'info');
  try {
    const data = await api('POST', '/v1/snapshots' + qs, body, 'apikey');
    log('Snapshot created: ' + data.name + ' (' + (data.size_bytes / 1024 / 1024).toFixed(1) + 'MB)', 'ok');
    listSnapshots();
    listSandboxes();
  } catch (e) {
    log('Snapshot failed: ' + e.message, 'err');
  }
}

async function listSnapshots() {
  try {
    const data = await api('GET', '/v1/snapshots', null, 'apikey');
    renderSnapshots(data);
  } catch (e) {
    log('List snapshots failed: ' + e.message, 'err');
  }
}

function renderSnapshots(snapshots) {
  if (!snapshots || snapshots.length === 0) {
    document.getElementById('snapshots-table').innerHTML = '<p style="color:#5f5c57;margin-top:8px">No snapshots</p>';
    return;
  }
  let html = '<table><thead><tr><th>Name</th><th>Type</th><th>vCPUs</th><th>Mem</th><th>Size</th><th>Actions</th></tr></thead><tbody>';
  for (const s of snapshots) {
    html += '<tr>';
    html += '<td class="clickable" onclick="useTemplate(\'' + esc(s.name) + '\')">' + esc(s.name) + '</td>';
    html += '<td>' + s.type + '</td>';
    html += '<td>' + (s.vcpus || '-') + '</td>';
    html += '<td>' + (s.memory_mb ? s.memory_mb + 'MB' : '-') + '</td>';
    html += '<td>' + (s.size_bytes / 1024 / 1024).toFixed(1) + 'MB</td>';
    html += '<td><button class="btn-red" onclick="deleteSnapshot(\'' + esc(s.name) + '\')">Delete</button></td>';
    html += '</tr>';
  }
  html += '</tbody></table>';
  document.getElementById('snapshots-table').innerHTML = html;
}

function useTemplate(name) {
  document.getElementById('create-template').value = name;
  log('Template set to "' + name + '" — click Create to launch from this snapshot', 'info');
}

async function deleteSnapshot(name) {
  log('Deleting snapshot "' + name + '"...', 'info');
  try {
    await api('DELETE', '/v1/snapshots/' + encodeURIComponent(name), null, 'apikey');
    log('Deleted snapshot "' + name + '"', 'ok');
    listSnapshots();
  } catch (e) {
    log('Delete snapshot failed: ' + e.message, 'err');
  }
}

// --- Auto-refresh ---
let refreshInterval = null;
document.getElementById('auto-refresh').addEventListener('change', function() {
  if (this.checked) {
    refreshInterval = setInterval(listSandboxes, 5000);
  } else {
    clearInterval(refreshInterval);
    refreshInterval = null;
  }
});

// --- Init ---
updateAuthStatus();
</script>
</body>
</html>`
