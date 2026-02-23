const $ = (id) => document.getElementById(id);

const elements = {
  status: $('status'),
  wsStatus: $('wsStatus'),
  realtimeInfo: $('realtimeInfo'),
  deviceList: $('deviceList'),
  events: $('events'),
  setupSummary: $('setupSummary'),
  setupHealth: $('setupHealth'),
  authStatus: $('authStatus'),
  metaRegion: $('metaRegion'),
  metaBase: $('metaBase'),
  metaRealtime: $('metaRealtime'),
  metaPat: $('metaPat'),
  statTotal: $('statTotal'),
  statOnline: $('statOnline'),
  statPowerOn: $('statPowerOn'),
  statRemoteOff: $('statRemoteOff'),
  lastSyncAt: $('lastSyncAt'),
  searchInput: $('searchInput'),
  filterOnline: $('filterOnline'),
  filterPower: $('filterPower'),
  sortBy: $('sortBy'),
  compactModeBtn: $('compactModeBtn'),
  bulkOnBtn: $('bulkOnBtn'),
  bulkOffBtn: $('bulkOffBtn'),
  accountRegion: $('accountRegion'),
  country: $('country'),
  patToken: $('patToken'),
  apiBaseUrl: $('apiBaseUrl'),
  apiKey: $('apiKey'),
  servicePhase: $('servicePhase'),
  clientId: $('clientId'),
  realtimeEnabled: $('realtimeEnabled'),
  realtimeTransport: $('realtimeTransport'),
  realtimeReconnectSec: $('realtimeReconnectSec'),
  hasPatToken: $('hasPatToken'),
  saveBtn: $('saveBtn'),
  verifyBtn: $('verifyBtn'),
  verifyOnlyBtn: $('verifyOnlyBtn'),
  reloadBtn: $('reloadBtn'),
  syncBtn: $('syncBtn'),
  clearPatBtn: $('clearPatBtn'),
  refreshAuthStatusBtn: $('refreshAuthStatusBtn'),
  exportSetupBtn: $('exportSetupBtn'),
  importSetupBtn: $('importSetupBtn'),
  importSetupFile: $('importSetupFile'),
};

const hasSetupForm = Boolean(elements.accountRegion);
const hasDashboard = Boolean(elements.deviceList || elements.events || elements.wsStatus);

const state = {
  devices: {},
  compact: false,
  connected: false,
};

const basePath = (() => {
  const pathName = window.location.pathname || '';
  const idx = pathName.indexOf('/ui/');
  return idx >= 0 ? pathName.slice(0, idx) : '';
})();

const REGION_DEFAULTS = {
  eu: { apiBaseUrl: 'https://api-eic.lgthinq.com', country: 'GB' },
  us: { apiBaseUrl: 'https://api-aic.lgthinq.com', country: 'US' },
  kr: { apiBaseUrl: 'https://api-kic.lgthinq.com', country: 'KR' },
  global: { apiBaseUrl: 'https://api-aic.lgthinq.com', country: 'US' },
};

const DEFAULTS = {
  region: 'eu',
  apiKey: 'v6GFvkweNo7DK7yD3ylIZ9w52aKBU0eJ7wLXkSR3',
  servicePhase: 'OP',
  clientId: 'homenavi-lg-thinq-client',
  realtimeEnabled: true,
  realtimeTransport: 'mqtt',
  realtimeReconnectSec: 30,
};

const FALLBACK_COUNTRIES = [
  { code: 'HU', name: 'Hungary' },
  { code: 'GB', name: 'United Kingdom' },
  { code: 'US', name: 'United States' },
  { code: 'KR', name: 'Korea' },
  { code: 'DE', name: 'Germany' },
];

function detectRegionWithoutGps() {
  const tz = (Intl.DateTimeFormat().resolvedOptions().timeZone || '').toLowerCase();
  const lang = (navigator.language || '').toLowerCase();
  if (tz.includes('asia/seoul') || lang.startsWith('ko')) return 'kr';
  if (tz.startsWith('america/') || lang.includes('-us')) return 'us';
  if (tz.startsWith('europe/')) return 'eu';
  return DEFAULTS.region;
}

function detectCountryWithoutGps(region) {
  const lang = (navigator.language || '').trim();
  const pieces = lang.split('-');
  if (pieces.length > 1 && pieces[1]) {
    const cc = pieces[1].toUpperCase();
    if (/^[A-Z]{2}$/.test(cc)) return cc;
  }
  return (REGION_DEFAULTS[region] || REGION_DEFAULTS[DEFAULTS.region]).country;
}

function ensureCountryOption(countryCode, countryName = '') {
  if (!elements.country) return;
  const code = String(countryCode || '').trim().toUpperCase();
  if (!code) return;
  const exists = Array.from(elements.country.options || []).some((opt) => String(opt.value || '').toUpperCase() === code);
  if (exists) return;
  const option = document.createElement('option');
  option.value = code;
  option.textContent = countryName ? `${countryName} (${code})` : code;
  option.dataset.custom = '1';
  elements.country.appendChild(option);
}

function populateCountryDropdown(countries) {
  if (!elements.country) return;
  const current = String(elements.country.value || '').trim().toUpperCase();
  const normalized = (Array.isArray(countries) ? countries : [])
    .map((item) => ({
      code: String(item?.code || '').trim().toUpperCase(),
      name: String(item?.name || '').trim(),
    }))
    .filter((item) => /^[A-Z]{2}$/.test(item.code));

  const source = normalized.length ? normalized : FALLBACK_COUNTRIES;
  elements.country.innerHTML = '';
  source.forEach((item) => {
    const option = document.createElement('option');
    option.value = item.code;
    option.textContent = `${item.name || item.code} (${item.code})`;
    elements.country.appendChild(option);
  });

  const preferred = current || detectCountryWithoutGps(String(elements.accountRegion?.value || DEFAULTS.region).toLowerCase());
  ensureCountryOption(preferred);
  elements.country.value = preferred;
}

async function loadCountryDropdown() {
  if (!elements.country) return;
  try {
    const data = await api('/api/admin/countries', { method: 'GET' });
    populateCountryDropdown(data?.countries || []);
  } catch {
    populateCountryDropdown(FALLBACK_COUNTRIES);
  }
}

function getCookie(name) {
  const parts = (document.cookie || '').split(';').map((value) => value.trim());
  const key = `${name}=`;
  const hit = parts.find((item) => item.startsWith(key));
  return hit ? decodeURIComponent(hit.slice(key.length)) : '';
}

async function api(path, options = {}) {
  const token = getCookie('auth_token');
  const headers = {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...(options.headers || {}),
  };
  const response = await fetch(`${basePath}${path}`, { credentials: 'include', ...options, headers });
  const text = await response.text();
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = {};
  }
  if (!response.ok) {
    const err = new Error(data.error || `HTTP ${response.status}`);
    err.status = response.status;
    throw err;
  }
  return data;
}

function setStatus(message, ok = true) {
  if (!elements.status) return;
  const base = ['hn-alert', 'hn-small'];
  if (ok === true) base.push('hn-alert--ok');
  if (ok === false) base.push('hn-alert--err');
  if (ok == null) base.push('hn-muted');
  elements.status.className = base.join(' ');
  elements.status.textContent = message;
}

function setWsStatus(message, stateClass) {
  if (!elements.wsStatus) return;
  const textNode = elements.wsStatus.querySelector('span:last-child');
  if (textNode) {
    textNode.textContent = message;
  } else {
    elements.wsStatus.textContent = message;
  }

  const dot = elements.wsStatus.querySelector('.hn-dot');
  if (dot) {
    dot.classList.remove('hn-dot--ok', 'hn-dot--err');
    if (stateClass === 'ok') dot.classList.add('hn-dot--ok');
    if (stateClass === 'err') dot.classList.add('hn-dot--err');
  }
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

function formatRealtimeLabel(setup) {
  const enabled = setup?.realtime_enabled !== false;
  const transport = String(setup?.realtime_transport || DEFAULTS.realtimeTransport).trim().toLowerCase() || 'mqtt';
  return enabled ? transport : 'off';
}

function deviceLabel(deviceId) {
  const dev = state.devices?.[deviceId];
  const name = String(dev?.name || '').trim();
  return name || 'device';
}

function iconHtml(name) {
  const icon = window.hnIcons?.icon;
  if (!icon) return '';
  return icon(name, 'hn-icon hn-icon--lg');
}

function addEvent(evt) {
  if (!elements.events) return;
  const row = document.createElement('div');
  row.className = 'event';
  const timestamp = new Date(evt.ts || Date.now()).toLocaleTimeString();
  row.textContent = `${timestamp} • ${evt.type || 'event'} • ${JSON.stringify(evt)}`;
  elements.events.prepend(row);
  while (elements.events.children.length > 140) {
    elements.events.removeChild(elements.events.lastChild);
  }
}

function normalizePower(device) {
  const value = String(device?.mapped_state?.power || '').trim().toLowerCase();
  if (value === 'on' || value === 'off') return value;
  return 'unknown';
}

function hasPowerCapability(device) {
  const inputs = Array.isArray(device?.inputs) ? device.inputs : [];
  return inputs.some((input) => {
    const inputId = String(input?.id || '').toLowerCase();
    const prop = String(input?.property || '').toLowerCase();
    return inputId === 'set_power' || inputId === 'power' || prop === 'power';
  }) || normalizePower(device) !== 'unknown';
}

function canSendCommands(device) {
  if (!device?.online) return false;
  return device?.mapped_state?.remote_control_enabled !== false;
}

function formatLastSeen(device) {
  const raw = device?.mapped_state?.last_seen || device?.last_seen || '';
  if (!raw) return 'n/a';
  const date = new Date(raw);
  if (Number.isNaN(date.getTime())) return 'n/a';
  return date.toLocaleString();
}

function updateMetaFromSetup(setup) {
  if (elements.setupSummary) {
    const hasPat = setup?.has_pat_token ? 'stored' : 'missing';
    elements.setupSummary.textContent = `Setup: region=${String(setup?.account_region || DEFAULTS.region).toUpperCase()} • PAT ${hasPat}`;
  }
  if (elements.setupHealth) {
    const hasPat = Boolean(setup?.has_pat_token);
    const msg = `Health: ${hasPat ? 'configured' : 'needs PAT'}`;
    const textNode = elements.setupHealth.querySelector('span:last-child');
    if (textNode) {
      textNode.textContent = msg;
    } else {
      elements.setupHealth.textContent = msg;
    }
    const dot = elements.setupHealth.querySelector('.hn-dot');
    if (dot) {
      dot.classList.remove('hn-dot--ok', 'hn-dot--err');
      dot.classList.add(hasPat ? 'hn-dot--ok' : 'hn-dot--err');
    }
  }
  if (elements.metaRegion) elements.metaRegion.textContent = String(setup?.account_region || '-').toUpperCase();
  if (elements.metaBase) elements.metaBase.textContent = setup?.api_base_url || '-';
  if (elements.metaRealtime) {
    const enabled = setup?.realtime_enabled !== false;
    const transport = String(setup?.realtime_transport || DEFAULTS.realtimeTransport).toLowerCase();
    const reconnectSec = Number(setup?.realtime_reconnect_sec || DEFAULTS.realtimeReconnectSec);
    elements.metaRealtime.textContent = `${enabled ? 'enabled' : 'disabled'} • ${transport} • ${reconnectSec}s`;
  }
  if (elements.metaPat) elements.metaPat.textContent = setup?.has_pat_token ? 'stored' : 'missing';
}

function updateKpis(filteredEntries = null) {
  const entries = filteredEntries || Object.entries(state.devices || {});
  let online = 0;
  let powerOn = 0;
  let remoteOff = 0;
  let latestSync = 0;

  entries.forEach(([, device]) => {
    if (device?.online) online += 1;
    if (normalizePower(device) === 'on') powerOn += 1;
    if (device?.mapped_state?.remote_control_enabled === false) remoteOff += 1;
    const raw = device?.mapped_state?.last_seen || device?.last_seen;
    if (raw) {
      const ts = new Date(raw).getTime();
      if (!Number.isNaN(ts) && ts > latestSync) latestSync = ts;
    }
  });

  if (elements.statTotal) elements.statTotal.textContent = String(entries.length);
  if (elements.statOnline) elements.statOnline.textContent = String(online);
  if (elements.statPowerOn) elements.statPowerOn.textContent = String(powerOn);
  if (elements.statRemoteOff) elements.statRemoteOff.textContent = String(remoteOff);
  if (elements.lastSyncAt) elements.lastSyncAt.textContent = latestSync ? new Date(latestSync).toLocaleTimeString() : '-';
}

function currentFilters() {
  return {
    query: String(elements.searchInput?.value || '').trim().toLowerCase(),
    online: elements.filterOnline?.value || 'all',
    power: elements.filterPower?.value || 'all',
    sortBy: elements.sortBy?.value || 'name',
  };
}

function filterAndSortEntries() {
  const entries = Object.entries(state.devices || {});
  const filters = currentFilters();

  const filtered = entries.filter(([id, device]) => {
    if (filters.query) {
      const haystack = [id, device?.name || '', device?.type || ''].join(' ').toLowerCase();
      if (!haystack.includes(filters.query)) return false;
    }
    if (filters.online === 'online' && !device?.online) return false;
    if (filters.online === 'offline' && device?.online) return false;

    const power = normalizePower(device);
    if (filters.power !== 'all' && power !== filters.power) return false;
    return true;
  });

  filtered.sort((a, b) => {
    const [, da] = a;
    const [, db] = b;
    if (filters.sortBy === 'online') {
      return Number(Boolean(db?.online)) - Number(Boolean(da?.online));
    }
    if (filters.sortBy === 'updated') {
      const ta = new Date(da?.mapped_state?.last_seen || da?.last_seen || 0).getTime() || 0;
      const tb = new Date(db?.mapped_state?.last_seen || db?.last_seen || 0).getTime() || 0;
      return tb - ta;
    }
    return String(da?.name || a[0]).localeCompare(String(db?.name || b[0]));
  });

  return filtered;
}

function buildDeviceHTML(id, device) {
  const mappedState = device?.mapped_state || {};
  const online = Boolean(device?.online);
  const power = normalizePower(device);
  const inputs = Array.isArray(device?.inputs) ? device.inputs : [];
  const actionAllowed = canSendCommands(device);
  const stateJSON = JSON.stringify(mappedState, null, state.compact ? 0 : 2);
  const typeLabel = String(device?.type || 'device');
  const chips = [
    `<span class="hn-chip ${online ? 'hn-chip--ok' : 'hn-chip--err'}">${window.hnIcons?.icon ? window.hnIcons.icon(online ? 'wifi' : 'wifiOff', 'hn-icon') : ''}${online ? 'online' : 'offline'}</span>`,
    `<span class="hn-chip">${window.hnIcons?.icon ? window.hnIcons.icon('power', 'hn-icon') : ''}power: ${escapeHtml(power)}</span>`,
    `<span class="hn-chip ${actionAllowed ? 'hn-chip--ok' : 'hn-chip--err'}">${window.hnIcons?.icon ? window.hnIcons.icon(actionAllowed ? 'unlock' : 'lock', 'hn-icon') : ''}${actionAllowed ? 'remote enabled' : 'remote locked'}</span>`,
  ];

  const commandControls = [];

  if (hasPowerCapability(device)) {
    const checked = power === 'on' ? 'checked' : '';
    commandControls.push(`<label class="hn-chip" style="display:inline-flex;gap:8px;align-items:center;">${window.hnIcons?.icon ? window.hnIcons.icon('power', 'hn-icon') : ''}Power <input type="checkbox" class="power-toggle" data-device-id="${escapeHtml(id)}" ${checked} ${online ? '' : 'disabled'} /></label>`);
  }

  inputs.forEach((input) => {
    const cmd = String(input?.id || '');
    if (!cmd || cmd === 'set_power' || cmd === 'power') return;
    if (String(input?.type || '').toLowerCase() === 'button') {
      commandControls.push(`<button class="hn-btn cmd-btn" type="button" data-device-id="${escapeHtml(id)}" data-command="${escapeHtml(cmd)}" ${actionAllowed ? '' : 'disabled'}>${escapeHtml(input?.label || cmd)}</button>`);
    } else if (String(input?.type || '').toLowerCase() === 'select' && Array.isArray(input?.options)) {
      const options = input.options.map((opt) => `<option value="${opt.value}">${opt.label || opt.value}</option>`).join('');
      commandControls.push(`<select class="hn-select cmd-select" data-device-id="${escapeHtml(id)}" data-command="${escapeHtml(cmd)}" data-prop="${escapeHtml(input?.property || '')}" ${actionAllowed ? '' : 'disabled'}>${options}</select>`);
    }
  });

  const name = device?.name || id;
  const lastSeen = formatLastSeen(device);
  const deviceIconName = deviceTypeToIconName(typeLabel);

  return `
    <div class="hn-device" data-device="${escapeHtml(id)}">
      <div class="hn-device__head">
        <div style="min-width:0;">
          <div class="hn-device__name" title="${escapeHtml(name)}">${iconHtml(deviceIconName)} ${escapeHtml(name)}</div>
          <div class="hn-device__meta">${escapeHtml(typeLabel)} • last seen: ${escapeHtml(lastSeen)}</div>
        </div>
      </div>
      <div style="margin-top:10px; display:flex; gap:8px; flex-wrap:wrap;">${chips.join('')}</div>
      <div class="hn-device__actions">${commandControls.length ? commandControls.join('') : '<span class="hn-muted hn-small">No exposed controls</span>'}</div>
      <details style="margin-top:10px;">
        <summary class="hn-muted hn-small" style="cursor:pointer;">State</summary>
        <pre class="hn-code" style="margin:10px 0 0; padding:10px; border-radius:var(--radius-md); border:1px solid var(--color-glass-border-xlight); background: var(--color-surface-panel-muted); white-space:pre-wrap; word-break:break-word;">${escapeHtml(stateJSON || '{}')}</pre>
      </details>
    </div>
  `;
}

function attachDeviceActionHandlers() {
  if (!elements.deviceList) return;

  let refreshBurstTimer = null;
  let refreshBurstRemaining = 0;
  let refreshBurstInFlight = false;

  const startRefreshBurst = ({ attempts = 6, intervalMs = 900 } = {}) => {
    refreshBurstRemaining = Math.max(refreshBurstRemaining, Math.max(1, Number(attempts) || 1));
    if (refreshBurstTimer) return;

    const tick = async () => {
      if (refreshBurstInFlight) return;
      if (refreshBurstRemaining <= 0) {
        clearInterval(refreshBurstTimer);
        refreshBurstTimer = null;
        return;
      }
      refreshBurstInFlight = true;
      try {
        await loadSnapshot();
      } finally {
        refreshBurstRemaining -= 1;
        refreshBurstInFlight = false;
        if (refreshBurstRemaining <= 0 && refreshBurstTimer) {
          clearInterval(refreshBurstTimer);
          refreshBurstTimer = null;
        }
      }
    };

    void tick();
    refreshBurstTimer = setInterval(tick, intervalMs);
  };

  elements.deviceList.querySelectorAll('.power-toggle').forEach((toggle) => {
    toggle.addEventListener('change', async () => {
      const deviceId = String(toggle.getAttribute('data-device-id') || '');
      const previous = !toggle.checked;
      const target = toggle.checked ? 'on' : 'off';
      try {
        await api('/api/admin/device-command', { method: 'POST', body: JSON.stringify({ device_id: deviceId, command: 'set_power', args: { power: target } }) });
        setStatus(`Command sent: ${deviceLabel(deviceId)} power=${target}`, true);
        startRefreshBurst();
      } catch (err) {
        toggle.checked = previous;
        setStatus(`Command failed: ${err.message}`, false);
        startRefreshBurst({ attempts: 2, intervalMs: 1200 });
      }
    });
  });

  elements.deviceList.querySelectorAll('.cmd-btn').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const deviceId = String(btn.getAttribute('data-device-id') || '');
      const command = String(btn.getAttribute('data-command') || '');
      try {
        await api('/api/admin/device-command', { method: 'POST', body: JSON.stringify({ device_id: deviceId, command, args: {} }) });
        setStatus(`Command sent: ${deviceLabel(deviceId)} ${command}`, true);
        startRefreshBurst();
      } catch (err) {
        setStatus(`Command failed: ${err.message}`, false);
        startRefreshBurst({ attempts: 2, intervalMs: 1200 });
      }
    });
  });

  elements.deviceList.querySelectorAll('.cmd-select').forEach((select) => {
    select.addEventListener('change', async () => {
      const deviceId = String(select.getAttribute('data-device-id') || '');
      const command = String(select.getAttribute('data-command') || '');
      const prop = String(select.getAttribute('data-prop') || '');
      const value = select.value;
      const args = prop ? { [prop]: value } : { value };
      try {
        await api('/api/admin/device-command', { method: 'POST', body: JSON.stringify({ device_id: deviceId, command, args }) });
        setStatus(`Command sent: ${deviceLabel(deviceId)} ${command}=${value}`, true);
        startRefreshBurst();
      } catch (err) {
        setStatus(`Command failed: ${err.message}`, false);
        startRefreshBurst({ attempts: 2, intervalMs: 1200 });
      }
    });
  });
}

function renderDevices() {
  if (!elements.deviceList) return;
  const entries = filterAndSortEntries();
  updateKpis(entries);

  if (!entries.length) {
    elements.deviceList.innerHTML = '<div class="hn-alert hn-small hn-muted">No devices match the current filters.</div>';
    return;
  }

  elements.deviceList.innerHTML = entries.map(([id, device]) => buildDeviceHTML(id, device)).join('');
  attachDeviceActionHandlers();
}

function applyRegionDefaults(region, force = false) {
  const selected = REGION_DEFAULTS[region] || REGION_DEFAULTS[DEFAULTS.region];
  if (elements.apiBaseUrl && (force || !elements.apiBaseUrl.value.trim())) elements.apiBaseUrl.value = selected.apiBaseUrl;
  if (elements.country && (force || !elements.country.value.trim())) {
    const detectedCountry = detectCountryWithoutGps(region);
    ensureCountryOption(detectedCountry);
    elements.country.value = detectedCountry;
  }
}

function collectSetupPayload() {
  return {
    mode: 'cloud',
    account_region: String(elements.accountRegion?.value || DEFAULTS.region).trim().toLowerCase(),
    api_base_url: String(elements.apiBaseUrl?.value || '').trim(),
    pat_token: String(elements.patToken?.value || '').trim(),
    api_key: String(elements.apiKey?.value || '').trim(),
    country: String(elements.country?.value || '').trim().toUpperCase(),
    service_phase: String(elements.servicePhase?.value || '').trim(),
    client_id: String(elements.clientId?.value || '').trim(),
    realtime_enabled: String(elements.realtimeEnabled?.value || String(DEFAULTS.realtimeEnabled)) !== 'false',
    realtime_transport: String(elements.realtimeTransport?.value || DEFAULTS.realtimeTransport).trim().toLowerCase(),
    realtime_reconnect_sec: Number(elements.realtimeReconnectSec?.value || DEFAULTS.realtimeReconnectSec),
  };
}

function applySetupForm(setup) {
  if (!hasSetupForm) return;
  const region = String(setup?.account_region || detectRegionWithoutGps() || DEFAULTS.region).toLowerCase();
  elements.accountRegion.value = region;
  const rawBase = String(setup?.api_base_url || '').trim().toLowerCase();
  const isLegacy = rawBase === 'https://api.smartthinq.com' || rawBase === 'http://api.smartthinq.com';
  elements.apiBaseUrl.value = isLegacy ? '' : String(setup?.api_base_url || '');
  elements.apiKey.value = String(setup?.api_key || DEFAULTS.apiKey);
  const selectedCountry = String(setup?.country || '').trim().toUpperCase();
  ensureCountryOption(selectedCountry);
  elements.country.value = selectedCountry;
  elements.servicePhase.value = String(setup?.service_phase || DEFAULTS.servicePhase);
  elements.clientId.value = String(setup?.client_id || DEFAULTS.clientId);
  if (elements.realtimeEnabled) elements.realtimeEnabled.value = String(setup?.realtime_enabled !== false);
  if (elements.realtimeTransport) elements.realtimeTransport.value = String(setup?.realtime_transport || DEFAULTS.realtimeTransport).toLowerCase();
  if (elements.realtimeReconnectSec) elements.realtimeReconnectSec.value = String(setup?.realtime_reconnect_sec || DEFAULTS.realtimeReconnectSec);
  elements.hasPatToken.value = setup?.has_pat_token ? 'Stored' : 'Missing';
  applyRegionDefaults(region, false);
}

async function loadSetup() {
  const data = await api('/api/admin/setup', { method: 'GET' });
  const setup = data.setup || {};
  applySetupForm(setup);
  updateMetaFromSetup(setup);
}

async function saveSetup() {
  const payload = collectSetupPayload();
  const data = await api('/api/admin/setup', { method: 'PUT', body: JSON.stringify(payload) });
  const setup = data.setup || {};
  applySetupForm(setup);
  updateMetaFromSetup(setup);
  setStatus('Setup saved.', true);
}

async function verifySetupWithPat() {
  const payload = collectSetupPayload();
  await api('/api/admin/setup', { method: 'PUT', body: JSON.stringify(payload) });
  const data = await api('/api/admin/auth/login', { method: 'POST', body: JSON.stringify({ pat_token: payload.pat_token }) });
  if (elements.patToken) elements.patToken.value = '';
  setStatus(`PAT verified. Devices: ${data?.device_count || 0}`, true);
  await loadSnapshot();
}

async function verifyStoredPat() {
  const data = await api('/api/admin/auth/verify', { method: 'POST', body: '{}' });
  setStatus(`Stored PAT verified. Devices: ${data?.device_count || 0}`, true);
}

async function loadAuthStatus() {
  if (!elements.authStatus) return;
  try {
    const data = await api('/api/admin/auth/status', { method: 'GET' });
    const status = data?.status || {};
    const provider = status?.provider || 'unknown';
    const success = Boolean(status?.success);
    const message = status?.message || 'No status';
    elements.authStatus.textContent = `Auth status: ${success ? 'ok' : 'not verified'} • provider=${provider} • ${message}`;
  } catch (err) {
    elements.authStatus.textContent = `Auth status unavailable: ${err.message}`;
  }
}

function downloadJson(filename, data) {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  URL.revokeObjectURL(url);
}

async function exportSetup() {
  const data = await api('/api/admin/setup', { method: 'GET' });
  downloadJson('lg-thinq-setup.json', data?.setup || {});
  setStatus('Setup exported.', true);
}

async function importSetupFromFile(file) {
  const text = await file.text();
  const parsed = JSON.parse(text);
  const setup = parsed?.setup || parsed;
  if (typeof setup !== 'object' || setup === null) {
    throw new Error('Invalid setup JSON');
  }

  if (elements.accountRegion) elements.accountRegion.value = String(setup.account_region || DEFAULTS.region);
  if (elements.apiBaseUrl) elements.apiBaseUrl.value = String(setup.api_base_url || '');
  if (elements.apiKey) elements.apiKey.value = String(setup.api_key || DEFAULTS.apiKey);
  if (elements.country) {
    const importedCountry = String(setup.country || '').trim().toUpperCase();
    ensureCountryOption(importedCountry);
    elements.country.value = importedCountry;
  }
  if (elements.servicePhase) elements.servicePhase.value = String(setup.service_phase || DEFAULTS.servicePhase);
  if (elements.clientId) elements.clientId.value = String(setup.client_id || DEFAULTS.clientId);
  if (elements.realtimeEnabled) elements.realtimeEnabled.value = String(setup.realtime_enabled !== false);
  if (elements.realtimeTransport) elements.realtimeTransport.value = String(setup.realtime_transport || DEFAULTS.realtimeTransport).toLowerCase();
  if (elements.realtimeReconnectSec) elements.realtimeReconnectSec.value = String(setup.realtime_reconnect_sec || DEFAULTS.realtimeReconnectSec);
  if (elements.patToken && setup.pat_token) elements.patToken.value = String(setup.pat_token);

  await saveSetup();
  setStatus('Setup imported and saved.', true);
}

async function syncNow() {
  await api('/api/admin/sync-now', { method: 'POST', body: '{}' });
  setStatus('Sync queued.', true);
  setTimeout(loadSnapshot, 700);
}

async function sendBulkPower(targetPower) {
  const entries = filterAndSortEntries();
  const candidates = entries.filter(([, device]) => hasPowerCapability(device) && canSendCommands(device));
  if (!candidates.length) {
    setStatus('No filtered devices eligible for power command.', false);
    return;
  }

  let ok = 0;
  for (const [deviceId] of candidates) {
    try {
      await api('/api/admin/device-command', { method: 'POST', body: JSON.stringify({ device_id: deviceId, command: 'set_power', args: { power: targetPower } }) });
      ok += 1;
    } catch {
    }
  }
  setStatus(`Bulk power ${targetPower}: ${ok}/${candidates.length} commands accepted.`, ok > 0);
}

async function loadSnapshot() {
  if (!hasDashboard) return;
  try {
    const snapshot = await api('/api/realtime/snapshot', { method: 'GET' });
    state.devices = snapshot?.devices || {};
    if (elements.realtimeInfo) elements.realtimeInfo.textContent = `Realtime: ${formatRealtimeLabel(snapshot?.setup || {})}`;
    renderDevices();
  } catch (err) {
    if (elements.realtimeInfo) elements.realtimeInfo.textContent = 'Realtime: unavailable';
    setStatus(`Snapshot unavailable: ${err.message}`, false);
  }
}

function connectWS() {
  if (!hasDashboard) return;
  const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${protocol}://${window.location.host}${basePath}/api/realtime/ws`);

  ws.onopen = () => {
    state.connected = true;
    setWsStatus('WS: connected', 'ok');
  };

  ws.onmessage = (event) => {
    try {
      const evt = JSON.parse(event.data);
      addEvent(evt);
      if (evt?.type === 'sync_failed' && evt?.error) {
        setStatus(`Sync failed: ${evt.error}`, false);
      }
      if (evt?.type === 'sync_suspended') {
        const seconds = Number(evt?.suspend_sec || 0);
        const details = seconds > 0 ? `retry in ~${seconds}s` : 'sync paused';
        setStatus(`Sync paused: ${evt?.reason || 'LG ThinQ API unavailable'} (${details})`, false);
      }
      if (evt?.type === 'sync_suspended_cleared') {
        setStatus('Sync resumed: LG ThinQ API available again.', true);
      }
      if (evt?.devices) {
        state.devices = evt.devices;
        renderDevices();
      }
      if (evt?.setup && elements.realtimeInfo) {
        elements.realtimeInfo.textContent = `Realtime: ${formatRealtimeLabel(evt.setup)}`;
      }
    } catch {
    }
  };

  ws.onclose = () => {
    state.connected = false;
    setWsStatus('WS: reconnecting…', 'err');
    setTimeout(connectWS, 2000);
  };

  ws.onerror = () => {
    state.connected = false;
    setWsStatus('WS: error', 'err');
  };
}

function bindDashboardControls() {
  [elements.searchInput, elements.filterOnline, elements.filterPower, elements.sortBy].forEach((el) => {
    if (!el) return;
    el.addEventListener('input', renderDevices);
    el.addEventListener('change', renderDevices);
  });

  if (elements.compactModeBtn) {
    elements.compactModeBtn.addEventListener('click', () => {
      state.compact = !state.compact;
      elements.compactModeBtn.textContent = `Compact: ${state.compact ? 'On' : 'Off'}`;
      renderDevices();
    });
  }

  if (elements.bulkOnBtn) {
    elements.bulkOnBtn.addEventListener('click', async () => {
      try {
        await sendBulkPower('on');
      } catch (err) {
        setStatus(`Bulk command failed: ${err.message}`, false);
      }
    });
  }

  if (elements.bulkOffBtn) {
    elements.bulkOffBtn.addEventListener('click', async () => {
      try {
        await sendBulkPower('off');
      } catch (err) {
        setStatus(`Bulk command failed: ${err.message}`, false);
      }
    });
  }
}

function bindSetupControls() {
  if (elements.accountRegion) {
    elements.accountRegion.addEventListener('change', () => {
      const region = String(elements.accountRegion.value || DEFAULTS.region).toLowerCase();
      applyRegionDefaults(region, true);
    });
  }

  if (elements.saveBtn) {
    elements.saveBtn.addEventListener('click', async () => {
      try {
        await saveSetup();
      } catch (err) {
        setStatus(`Save failed: ${err.message}`, false);
      }
    });
  }

  if (elements.verifyBtn) {
    elements.verifyBtn.addEventListener('click', async () => {
      try {
        await verifySetupWithPat();
      } catch (err) {
        setStatus(`Verification failed: ${err.message}`, false);
      }
    });
  }

  if (elements.verifyOnlyBtn) {
    elements.verifyOnlyBtn.addEventListener('click', async () => {
      try {
        await verifyStoredPat();
      } catch (err) {
        setStatus(`Stored PAT verify failed: ${err.message}`, false);
      }
    });
  }

  if (elements.clearPatBtn) {
    elements.clearPatBtn.addEventListener('click', () => {
      if (elements.patToken) elements.patToken.value = '';
      setStatus('PAT input cleared.', true);
    });
  }

  if (elements.refreshAuthStatusBtn) {
    elements.refreshAuthStatusBtn.addEventListener('click', () => {
      loadAuthStatus();
    });
  }

  if (elements.exportSetupBtn) {
    elements.exportSetupBtn.addEventListener('click', async () => {
      try {
        await exportSetup();
      } catch (err) {
        setStatus(`Export failed: ${err.message}`, false);
      }
    });
  }

  if (elements.importSetupBtn && elements.importSetupFile) {
    elements.importSetupBtn.addEventListener('click', () => {
      elements.importSetupFile.click();
    });
    elements.importSetupFile.addEventListener('change', async (event) => {
      try {
        const file = event.target.files?.[0];
        if (!file) return;
        await importSetupFromFile(file);
      } catch (err) {
        setStatus(`Import failed: ${err.message}`, false);
      } finally {
        elements.importSetupFile.value = '';
      }
    });
  }
}

function bindSharedButtons() {
  if (elements.reloadBtn) {
    elements.reloadBtn.addEventListener('click', async () => {
      try {
        await loadSetup();
        if (hasDashboard) {
          await loadSnapshot();
        }
        setStatus(hasSetupForm ? 'Setup loaded.' : 'Snapshot loaded.', true);
      } catch (err) {
        setStatus(`Load failed: ${err.message}`, false);
      }
    });
  }

  if (elements.syncBtn) {
    elements.syncBtn.addEventListener('click', async () => {
      try {
        await syncNow();
      } catch (err) {
        setStatus(`Sync failed: ${err.message}`, false);
      }
    });
  }
}

async function init() {
  try {
    bindSharedButtons();
    if (hasSetupForm) {
      bindSetupControls();
      await loadCountryDropdown();
      await loadSetup();
      await loadAuthStatus();
    }
    if (hasDashboard) {
      bindDashboardControls();
      await loadSnapshot();
      connectWS();
    }
    setStatus('Ready.', true);
  } catch (err) {
    setStatus(`Initialization failed: ${err.message}`, false);
  }
}

init();
