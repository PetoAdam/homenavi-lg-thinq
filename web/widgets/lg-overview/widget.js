const summaryEl = document.getElementById('summary');
const devicesEl = document.getElementById('devices');
const statTotalEl = document.getElementById('statTotal');
const statOnlineEl = document.getElementById('statOnline');
const statPowerOnEl = document.getElementById('statPowerOn');

const basePath = (() => {
  const p = window.location.pathname || '';
  const idx = p.indexOf('/widgets/');
  return idx >= 0 ? p.slice(0, idx) : '';
})();

function normalizePower(device) {
  const power = String(device?.mapped_state?.power || '').toLowerCase();
  if (power === 'on' || power === 'off') return power;
  return 'unknown';
}

function escapeHtml(value) {
  return String(value || '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function deviceTypeToIconName(type) {
  const t = String(type || '').toLowerCase();
  if (t.includes('washer') || t.includes('wash')) return 'washer';
  if (t.includes('dryer')) return 'dryer';
  if (t.includes('tv')) return 'tv';
  if (t.includes('air') && t.includes('condition')) return 'snowflake';
  if (t.includes('ac')) return 'snowflake';
  if (t.includes('refrigerator') || t.includes('fridge')) return 'fridge';
  if (t.includes('dishwasher')) return 'dishwasher';
  if (t.includes('microwave')) return 'microwave';
  if (t.includes('oven') || t.includes('range')) return 'oven';
  if (t.includes('vacuum') || t.includes('robot')) return 'vacuum';
  if (t.includes('speaker') || t.includes('audio') || t.includes('soundbar')) return 'speaker';
  if (t.includes('fan') || t.includes('purifier') || t.includes('dehumid')) return 'fan';
  if (t.includes('light') || t.includes('lamp')) return 'light';
  return 'plug';
}

function iconHtml(name, className = 'hn-icon') {
  const icon = window.hnIcons?.icon;
  if (!icon) return '';
  return icon(name, className);
}

function formatMinutes(raw) {
  const n = typeof raw === 'number' ? raw : Number(raw);
  if (!Number.isFinite(n)) return '';
  return `${Math.max(0, Math.round(n))}m`;
}

function summarizeState(device) {
  const kind = String(device?.type || '').toLowerCase();
  const state = device?.mapped_state || {};
  const parts = [];
  const add = (label, value) => {
    const v = value === undefined || value === null ? '' : String(value);
    if (!v.trim()) return;
    parts.push(`${label}: ${v}`);
  };

  if (kind.includes('washer') || kind.includes('wash') || kind.includes('dryer')) {
    const run = String(state.run_state || '').toLowerCase().trim();
    add('run', run);
    const running = run === 'running' || run.includes('run');
    if (running) {
      const mins = formatMinutes(state.remaining_min);
      if (mins) add('remaining', mins);
    }
    return parts.slice(0, 2).join(' • ');
  }

  if (kind.includes('tv')) {
    add('playback', state.playback);
    if (state.volume !== undefined && state.volume !== null) add('vol', state.volume);
    add('input', state.input);
    return parts.slice(0, 2).join(' • ');
  }

  ['temperature', 'humidity', 'battery', 'linkquality'].forEach((key) => {
    if (key in state) add(key, state[key]);
  });
  if (parts.length) return parts.slice(0, 2).join(' • ');

  return Object.entries(state)
    .filter(([k, v]) => v !== undefined && v !== null && String(k).toLowerCase() !== 'online')
    .slice(0, 2)
    .map(([k, v]) => `${k}: ${String(v)}`)
    .join(' • ');
}

function render(entries) {
  const online = entries.filter(([, device]) => Boolean(device?.online)).length;
  const powerOn = entries.filter(([, device]) => normalizePower(device) === 'on').length;

  if (statTotalEl) statTotalEl.textContent = String(entries.length);
  if (statOnlineEl) statOnlineEl.textContent = String(online);
  if (statPowerOnEl) statPowerOnEl.textContent = String(powerOn);

  if (!entries.length) {
    devicesEl.innerHTML = '<div class="hn-alert hn-small hn-muted">No LG devices mapped yet.</div>';
    return;
  }

  const sorted = [...entries].sort((a, b) => {
    const aOnline = Number(Boolean(a[1]?.online));
    const bOnline = Number(Boolean(b[1]?.online));
    if (aOnline !== bOnline) return bOnline - aOnline;
    return String(a[1]?.name || a[0]).localeCompare(String(b[1]?.name || b[0]));
  });

  devicesEl.innerHTML = sorted.slice(0, 4).map(([id, device]) => {
    const onlineText = device?.online ? 'online' : 'offline';
    const powerText = normalizePower(device);
    const kind = String(device?.type || 'device');
    const name = String(device?.name || id);

    const brief = summarizeState(device);

    const chips = [
      `<span class="hn-chip ${device?.online ? 'hn-chip--ok' : 'hn-chip--err'}">${iconHtml(device?.online ? 'wifi' : 'wifiOff')}${onlineText}</span>`,
      `<span class="hn-chip ${powerText === 'on' ? 'hn-chip--ok' : powerText === 'off' ? 'hn-chip--err' : ''}">${iconHtml('power')}power: ${escapeHtml(powerText)}</span>`,
    ];

    return `
      <div class="hn-device">
        <div class="hn-device__head">
          <div style="min-width:0;">
            <div class="hn-device__name" title="${escapeHtml(name)}">${iconHtml(deviceTypeToIconName(kind), 'hn-icon hn-icon--lg')} ${escapeHtml(name)}</div>
            <div class="hn-device__meta">${escapeHtml(kind)}</div>
          </div>
        </div>
        <div style="margin-top:8px; display:flex; gap:6px; flex-wrap:wrap;">${chips.join('')}</div>
        <div class="hn-device__meta" style="margin-top:8px;">${escapeHtml(brief || 'no state yet')}</div>
      </div>
    `;
  }).join('');
}

function applySnapshot(payload) {
  const setup = payload?.setup || {};
  const entries = Object.entries(payload?.devices || {});
  summaryEl.textContent = entries.length
    ? `${entries.length} mapped LG device${entries.length > 1 ? 's' : ''}`
    : 'No LG devices mapped yet.';
  render(entries);
}

async function load() {
  try {
    const res = await fetch(`${basePath}/api/realtime/snapshot`, { credentials: 'include' });
    if (!res.ok) {
      summaryEl.textContent = res.status === 401 ? 'Sign in required for live state.' : `Snapshot unavailable (${res.status})`;
      if (devicesEl) devicesEl.innerHTML = '';
      return;
    }
    const payload = await res.json();
    applySnapshot(payload);
  } catch {
    summaryEl.textContent = 'Failed to load widget snapshot.';
  }
}

function wsURL() {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${basePath}/api/realtime/ws`;
}

let ws = null;
let wsConnected = false;
let wsHasMessage = false;
let wsReconnectTimer = null;
let snapshotFallbackTimer = null;

function clearSnapshotFallback() {
  if (!snapshotFallbackTimer) return;
  clearTimeout(snapshotFallbackTimer);
  snapshotFallbackTimer = null;
}

function scheduleSnapshotFallback() {
  if (snapshotFallbackTimer) return;
  // WS-first: only fetch snapshot if WS doesn't deliver quickly
  // (auth errors, WS blocked, transient network).
  snapshotFallbackTimer = setTimeout(() => {
    snapshotFallbackTimer = null;
    if (!wsConnected || !wsHasMessage) {
      void load();
    }
  }, 1200);
}

function scheduleWSReconnect() {
  if (wsReconnectTimer) return;
  wsReconnectTimer = setTimeout(() => {
    wsReconnectTimer = null;
    connectWS();
  }, 3000);
}

function connectWS() {
  try {
    if (ws) {
      try { ws.close(); } catch {}
      ws = null;
    }
    wsConnected = false;
    wsHasMessage = false;
    ws = new WebSocket(wsURL());

    ws.addEventListener('open', () => {
      wsConnected = true;
      scheduleSnapshotFallback();
    });

    ws.addEventListener('message', (ev) => {
      let payload = null;
      try {
        payload = JSON.parse(String(ev.data || ''));
      } catch {
        return;
      }
      if (payload && typeof payload === 'object' && payload.devices) {
        wsHasMessage = true;
        clearSnapshotFallback();
        applySnapshot(payload);
      }
    });

    ws.addEventListener('close', () => {
      wsConnected = false;
      scheduleSnapshotFallback();
      scheduleWSReconnect();
    });

    ws.addEventListener('error', () => {
      wsConnected = false;
      scheduleSnapshotFallback();
      scheduleWSReconnect();
    });
  } catch {
    wsConnected = false;
    scheduleSnapshotFallback();
    scheduleWSReconnect();
  }
}

connectWS();
scheduleSnapshotFallback();
