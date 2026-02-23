const widgetCardEl = document.getElementById('widgetCard');
const headToggleEl = document.getElementById('headToggle');
const deviceIconEl = document.getElementById('deviceIcon');
const deviceTitleEl = document.getElementById('deviceTitle');
const deviceSubtitleEl = document.getElementById('deviceSubtitle');
const rtInfoEl = document.getElementById('rtInfo');
const menuBtnEl = document.getElementById('menuBtn');
const menuIconEl = document.getElementById('menuIcon');
const deviceMenuEl = document.getElementById('deviceMenu');
const deviceSelectEl = document.getElementById('deviceSelect');
const statusIconsEl = document.getElementById('statusIcons');
const controlsEl = document.getElementById('controls');
const stateValuesEl = document.getElementById('stateValues');
const statusEl = document.getElementById('status');

const basePath = (() => {
  const p = window.location.pathname || '';
  const idx = p.indexOf('/widgets/');
  return idx >= 0 ? p.slice(0, idx) : '';
})();

const state = {
  devices: {},
  deviceIds: [],
  selectedId: '',
  menuOpen: false,
  realtimeEnabled: true,
  realtimeTransport: '',
  commandPendingUntil: 0,
  commandGraceUntil: 0,
  commandPendingDeviceId: '',
  commandBaseline: '',
  commandObservedFingerprint: '',
  commandDisableUntil: 0,
  // When we first observe a non-baseline state after a command,
  // keep the pending gate for a short stability window to avoid
  // delayed old realtime snapshots from overriding the new state.
  commandStableUntil: 0,
  // Realtime (WS) suppression: after issuing a REST command, do not accept WS snapshots
  // until after the next REST snapshot completes + a short grace period.
  realtimeSuppressUntil: 0,
  realtimeAwaitingRest: false,
  commandLockUntil: 0,
  commandLockDeviceId: '',
  commandLockedFingerprint: '',
  commandLastBaseline: '',
};

const REALTIME_RESUME_GRACE_MS = 1500;
const COMMAND_DISABLE_MS = 2000;
const COMMAND_ROLLBACK_LOCK_MS = 5000;

let uiWakeTimer = null;
let pendingClearTimer = null;

function getCookie(name) {
  const parts = (document.cookie || '').split(';').map((value) => value.trim());
  const key = `${name}=`;
  const hit = parts.find((item) => item.startsWith(key));
  return hit ? decodeURIComponent(hit.slice(key.length)) : '';
}

function authHeaders() {
  const token = getCookie('auth_token');
  return token ? { Authorization: `Bearer ${token}` } : {};
}

function suppressRealtimeUntilAfterNextRest() {
  state.realtimeAwaitingRest = false;
  state.realtimeSuppressUntil = Date.now() + REALTIME_RESUME_GRACE_MS;
  state.commandStableUntil = 0;
  state.commandObservedFingerprint = '';
  state.commandDisableUntil = 0;
  state.commandLockUntil = 0;
  state.commandLockDeviceId = '';
  state.commandLockedFingerprint = '';
  state.commandLastBaseline = '';
  if (pendingClearTimer) {
    clearTimeout(pendingClearTimer);
    pendingClearTimer = null;
  }
}

function scheduleUIWakeAt(ts) {
  const at = Number(ts) || 0;
  if (!at) return;
  const delay = Math.max(0, at - Date.now());
  if (uiWakeTimer) {
    clearTimeout(uiWakeTimer);
    uiWakeTimer = null;
  }
  uiWakeTimer = setTimeout(() => {
    uiWakeTimer = null;
    renderSelected();
  }, delay);
}

function clearPendingState({ statusMessage = 'Updated.' } = {}) {
  const pendingDeviceId = state.commandPendingDeviceId;
  const baseline = state.commandBaseline;
  const observed = state.commandObservedFingerprint;

  state.commandPendingUntil = 0;
  state.commandGraceUntil = 0;
  state.commandPendingDeviceId = '';
  state.commandBaseline = '';
  state.commandStableUntil = 0;
  state.commandObservedFingerprint = '';
  state.commandDisableUntil = 0;

  if (pendingDeviceId && observed) {
    state.commandLockUntil = Date.now() + COMMAND_ROLLBACK_LOCK_MS;
    state.commandLockDeviceId = pendingDeviceId;
    state.commandLockedFingerprint = observed;
    state.commandLastBaseline = baseline || '';
  } else {
    state.commandLockUntil = 0;
    state.commandLockDeviceId = '';
    state.commandLockedFingerprint = '';
    state.commandLastBaseline = '';
  }

  if (pendingClearTimer) {
    clearTimeout(pendingClearTimer);
    pendingClearTimer = null;
  }
  setStatus(statusMessage, true);
  setTimeout(() => setStatus('Ready.', null), 900);
}

function schedulePendingAutoClear(atTs) {
  const at = Number(atTs) || 0;
  if (!at) return;
  const delay = Math.max(0, at - Date.now());
  if (pendingClearTimer) return;
  pendingClearTimer = setTimeout(() => {
    pendingClearTimer = null;
    const pendingActive = Date.now() < state.commandPendingUntil && state.commandPendingDeviceId;
    if (!pendingActive) return;

    const dev = state.devices?.[state.commandPendingDeviceId];
    const current = deviceStateFingerprint(dev);
    if (state.commandBaseline && current && current !== state.commandBaseline) {
      clearPendingState({ statusMessage: 'Updated.' });
      renderSelected();
    }
  }, delay);
}

function resumeRealtimeAfterRestSnapshot() {
  // WS-only feedback mode.
}

function realtimeAllowed() {
  return Date.now() >= (state.realtimeSuppressUntil || 0);
}

function deviceStateFingerprint(device) {
  if (!device || typeof device !== 'object') return '';
  const mapped = device?.mapped_state && typeof device.mapped_state === 'object' ? device.mapped_state : {};
  const power = normalizePower(device);
  const online = device?.online ? '1' : '0';
  let mappedJson = '';
  try {
    mappedJson = JSON.stringify(mapped);
  } catch {
    mappedJson = '';
  }
  return `${online}|${power}|${mappedJson}`;
}

function escapeHtml(value) {
  return String(value || '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function iconHtml(name, className = 'hn-icon') {
  const icon = window.hnIcons?.icon;
  if (!icon) return '';
  return icon(name, className);
}

function setStatus(message, ok = null) {
  if (!statusEl) return;
  const msg = String(message || '').trim();

  if (ok == null && msg.toLowerCase() === 'ready.') {
    statusEl.style.display = 'none';
    return;
  }

  statusEl.style.display = '';
  const base = ['hn-alert', 'hn-small'];
  if (ok === true) base.push('hn-alert--ok');
  if (ok === false) base.push('hn-alert--err');
  if (ok == null) base.push('hn-muted');
  statusEl.className = base.join(' ');
  statusEl.textContent = msg;
}

function deviceKind(device) {
  const t = String(device?.type || '').trim();
  return t ? t.toLowerCase() : 'device';
}

function isWasher(device) {
  const t = deviceKind(device);
  return t.includes('washer') || t.includes('wash') || t.includes('dryer');
}

function isTV(device) {
  const t = deviceKind(device);
  return t.includes('tv');
}

function humanizeType(type) {
  const raw = String(type || '').trim();
  if (!raw) return 'Device';
  return raw
    .replaceAll('-', ' ')
    .replaceAll('_', ' ')
    .split(' ')
    .filter(Boolean)
    .map((w) => w.slice(0, 1).toUpperCase() + w.slice(1))
    .join(' ');
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

function normalizePower(device) {
  const power = String(device?.mapped_state?.power || '').trim().toLowerCase();
  if (power === 'on' || power === 'off') return power;
  return 'unknown';
}

function firstString(obj, keys) {
  for (const key of keys) {
    const v = obj?.[key];
    if (v == null) continue;
    const s = String(v).trim();
    if (s) return s;
  }
  return '';
}

function findWasherRunState(mapped) {
  return firstString(mapped, [
    'run_state',
    'runState',
    'current_state',
    'currentState',
    'state',
    'course_state',
    'process',
    'status',
  ]);
}

function findRemainingTime(mapped) {
  return firstString(mapped, [
    'remaining_time',
    'remain_time',
    'remainTime',
    'remaining',
    'remaining_minutes',
    'remainMinutes',
    'time_remaining',
  ]);
}

function populateDeviceSelect() {
  if (!deviceSelectEl) return;
  deviceSelectEl.innerHTML = '';
  state.deviceIds.forEach((id) => {
    const dev = state.devices[id];
    const opt = document.createElement('option');
    opt.value = id;
    const kind = deviceKind(dev);
    const name = String(dev?.name || '').trim();
    opt.textContent = name || humanizeType(kind);
    deviceSelectEl.appendChild(opt);
  });

  if (!state.selectedId || !state.deviceIds.includes(state.selectedId)) {
    state.selectedId = state.deviceIds[0] || '';
  }
  deviceSelectEl.value = state.selectedId;

  deviceSelectEl.disabled = state.deviceIds.length <= 1;
}

function setMenuOpen(open) {
  const next = Boolean(open);
  state.menuOpen = next;
  if (deviceMenuEl) deviceMenuEl.style.display = next ? '' : 'none';
  if (menuIconEl) {
    menuIconEl.innerHTML = next ? iconHtml('chevronUp') : iconHtml('chevronDown');
  }
}

function toggleMenu() {
  setMenuOpen(!state.menuOpen);
  if (state.menuOpen && deviceSelectEl) {
    try {
      deviceSelectEl.focus();
    } catch {
    }
  }
}

function shouldIgnoreToggleClick(target) {
  const el = target instanceof Element ? target : null;
  if (!el) return false;
  if (el.closest('button, a, input, select, option, textarea, label')) return true;
  if (deviceMenuEl && el.closest('#deviceMenu')) return true;
  return false;
}

function commandToIconName(command) {
  const cmd = String(command || '').toLowerCase().trim();
  if (cmd === 'start') return 'play';
  if (cmd === 'stop') return 'stop';
  if (cmd.includes('mute')) return 'mute';
  if (cmd.includes('volume')) return 'volume';
  if (cmd.includes('input')) return 'tv';
  if (cmd.includes('power')) return 'power';
  return 'bolt';
}

function renderSelected() {
  const id = state.selectedId;
  const dev = id ? state.devices[id] : null;
  if (!dev) {
    if (deviceTitleEl) deviceTitleEl.textContent = 'No device';
    if (deviceSubtitleEl) deviceSubtitleEl.textContent = state.deviceIds.length ? `${state.deviceIds.length} mapped` : 'No mapped devices.';
    if (deviceIconEl) deviceIconEl.innerHTML = iconHtml('plug', 'hn-icon');
    if (statusIconsEl) statusIconsEl.innerHTML = '';
    if (controlsEl) controlsEl.innerHTML = '';
    if (stateValuesEl) stateValuesEl.innerHTML = '';
    return;
  }

  const mapped = dev?.mapped_state || {};
  // Treat missing online info as online so controls aren't disabled by default.
  const online = dev?.online !== false;
  const power = normalizePower(dev);
  const actionAllowed = dev?.mapped_state?.remote_control_enabled !== false;

  const pendingForThis = Date.now() < state.commandPendingUntil && state.commandPendingDeviceId === id;
  const baselineSame = pendingForThis && state.commandBaseline && deviceStateFingerprint(state.devices[id]) === state.commandBaseline;
  const graceActive = pendingForThis && Date.now() < state.commandGraceUntil;
  const suppressState = graceActive && baselineSame;
  const controlsDisabled = pendingForThis && Date.now() < (state.commandDisableUntil || 0);
  const powerForUi = suppressState ? 'unknown' : power;
  const powerActive = powerForUi === 'on';

  const kind = deviceKind(dev);
  const washer = isWasher(dev);
  const tv = isTV(dev);

  const commandAllowed = online && !controlsDisabled;
  const powerAllowed = commandAllowed;
  const remoteActionAllowed = washer ? (commandAllowed && actionAllowed) : commandAllowed;

  let remainingText = '';
  let showRemaining = false;

  if (washer) {
    const run = (findWasherRunState(mapped) || '').toLowerCase();
    const running = run === 'running' || run.includes('run');
    const remainingMin = mapped?.remaining_min;
    const remainingRaw = typeof remainingMin === 'number' ? String(remainingMin) : findRemainingTime(mapped);
    if (running && remainingRaw) {
      const numeric = Number(remainingRaw);
      remainingText = Number.isFinite(numeric) ? `${Math.max(0, Math.round(numeric))}m` : String(remainingRaw);
      showRemaining = true;
    }
  }

  if (deviceTitleEl) deviceTitleEl.textContent = humanizeType(kind);
  if (deviceSubtitleEl) {
    const name = String(dev?.name || '').trim();
    const mappedCount = state.deviceIds.length;
    deviceSubtitleEl.textContent = name ? `${name}${mappedCount > 1 ? ` • ${mappedCount} mapped` : ''}` : `${mappedCount} mapped`;
  }
  if (deviceIconEl) deviceIconEl.innerHTML = iconHtml(deviceTypeToIconName(kind), 'hn-icon');

  if (statusIconsEl) {
    const items = [];
    items.push(
      `<span class="lgw-sicon ${online ? 'lgw-sicon--ok' : 'lgw-sicon--err'}" title="${online ? 'Online' : 'Offline'}" aria-label="${online ? 'Online' : 'Offline'}">${iconHtml(online ? 'wifi' : 'wifiOff')}</span>`,
    );
    items.push(
      `<span class="lgw-sicon ${suppressState ? '' : power === 'on' ? 'lgw-sicon--ok' : power === 'off' ? 'lgw-sicon--err' : ''}" title="Power: ${escapeHtml(suppressState ? 'updating' : power)}" aria-label="Power">${iconHtml('power')}${suppressState ? '<span class="lgw-badge">…</span>' : ''}</span>`,
    );
    if (dev?.mapped_state && 'remote_control_enabled' in dev.mapped_state) {
      items.push(
        `<span class="lgw-sicon ${actionAllowed ? 'lgw-sicon--ok' : 'lgw-sicon--err'}" title="${actionAllowed ? 'Remote enabled' : 'Remote locked'}" aria-label="Remote">${iconHtml(actionAllowed ? 'unlock' : 'lock')}</span>`,
      );
    }
    if (!suppressState && showRemaining && remainingText) {
      items.push(
        `<span class="lgw-sicon" title="Remaining: ${escapeHtml(remainingText)}" aria-label="Remaining">${iconHtml('clock')}<span class="lgw-badge">${escapeHtml(remainingText)}</span></span>`,
      );
    }
    if (!suppressState && tv) {
      const muted = mapped?.muted === true;
      if (typeof mapped?.volume === 'number') {
        items.push(`<span class="lgw-sicon" title="Volume" aria-label="Volume">${iconHtml('volume')}<span class="lgw-badge">${escapeHtml(String(mapped.volume))}</span></span>`);
      }
      if (muted) {
        items.push(`<span class="lgw-sicon lgw-sicon--err" title="Muted" aria-label="Muted">${iconHtml('mute')}</span>`);
      }
    }
    statusIconsEl.innerHTML = items.join('');
  }

  if (controlsEl) {
    const controls = [];

    if (online) {
      controls.push(
        `<button class="hn-btn lgw-btn cmd-power ${powerActive ? 'hn-btn--primary' : ''}" type="button" data-device-id="${escapeHtml(id)}" data-target="${powerActive ? 'off' : 'on'}" ${powerAllowed ? '' : 'disabled'} title="${powerActive ? 'Power off' : 'Power on'}" aria-label="${powerActive ? 'Power off' : 'Power on'}">${iconHtml('power')}</button>`,
      );
    } else {
      controls.push(
        `<button class="hn-btn lgw-btn" type="button" disabled title="Offline">${iconHtml('power')}</button>`,
      );
    }

    const inputs = Array.isArray(dev?.inputs) ? dev.inputs : [];
    inputs.forEach((input) => {
      const cmd = String(input?.id || '').trim();
      if (!cmd || cmd === 'set_power' || cmd === 'power') return;
      const type = String(input?.type || '').toLowerCase();
      const label = String(input?.label || cmd);

      // ThinQ's remote_control_enabled maps to remote-start permission for washers.
      // Power should remain usable even if remote-start is locked.
      const allowed = washer ? remoteActionAllowed : commandAllowed;

      if (type === 'button') {
        controls.push(
          `<button class="hn-btn lgw-btn cmd-btn" type="button" data-device-id="${escapeHtml(id)}" data-command="${escapeHtml(cmd)}" ${allowed ? '' : 'disabled'} title="${escapeHtml(label)}" aria-label="${escapeHtml(label)}">${iconHtml(commandToIconName(cmd))}</button>`,
        );
      } else if (type === 'select' && Array.isArray(input?.options) && input.options.length) {
        const opts = input.options
          .map((opt) => `<option value="${escapeHtml(opt.value)}">${escapeHtml(opt.label || opt.value)}</option>`)
          .join('');
        controls.push(
          `<select class="hn-select cmd-select" data-device-id="${escapeHtml(id)}" data-command="${escapeHtml(cmd)}" data-prop="${escapeHtml(input?.property || '')}" ${allowed ? '' : 'disabled'} aria-label="${escapeHtml(label)}" title="${escapeHtml(label)}" style="min-width: 160px;">${opts}</select>`,
        );
      } else if (type === 'range' && input?.range) {
        const min = Number(input?.range?.min ?? 0);
        const max = Number(input?.range?.max ?? 100);
        const step = Number(input?.range?.step ?? 1);
        const prop = String(input?.property || '').trim();
        const current = prop && mapped && mapped[prop] != null ? Number(mapped[prop]) : min;
        const value = Number.isFinite(current) ? current : min;
        controls.push(
          `<div class="lgw-slider" title="${escapeHtml(label)}" aria-label="${escapeHtml(label)}">${iconHtml(commandToIconName(cmd), 'hn-icon hn-icon--sm')}<input class="cmd-range" type="range" min="${escapeHtml(min)}" max="${escapeHtml(max)}" step="${escapeHtml(step)}" value="${escapeHtml(value)}" data-device-id="${escapeHtml(id)}" data-command="${escapeHtml(cmd)}" data-prop="${escapeHtml(prop)}" ${allowed ? '' : 'disabled'} /></div>`,
        );
      }
    });

    controlsEl.innerHTML = controls.length ? controls.join('') : '<span class="hn-muted hn-small">No controls exposed for this device.</span>';
    attachControlHandlers();
  }

  if (stateValuesEl) {
    if (suppressState) {
      stateValuesEl.innerHTML = `<span class="hn-chip hn-small">${iconHtml('clock')}Updating…</span>`;
      return;
    }

    const chips = [];
    chips.push(
      `<span class="hn-chip hn-small ${online ? 'hn-chip--ok' : 'hn-chip--err'}">${iconHtml(online ? 'wifi' : 'wifiOff')}${online ? 'online' : 'offline'}</span>`,
    );
    chips.push(
      `<span class="hn-chip hn-small ${power === 'on' ? 'hn-chip--ok' : power === 'off' ? 'hn-chip--err' : ''}">${iconHtml('power')}power: ${escapeHtml(power)}</span>`,
    );
    if (dev?.mapped_state && 'remote_control_enabled' in dev.mapped_state) {
      chips.push(
        `<span class="hn-chip hn-small ${actionAllowed ? 'hn-chip--ok' : 'hn-chip--err'}">${iconHtml(actionAllowed ? 'unlock' : 'lock')}${actionAllowed ? 'remote: enabled' : 'remote: locked'}</span>`,
      );
    }

    if (washer) {
      const run = findWasherRunState(mapped);
      if (run) {
        chips.push(`<span class="hn-chip hn-small">${iconHtml('washer')}run: ${escapeHtml(run)}</span>`);
      }
      const remaining = mapped?.remaining_min;
      if (typeof remaining === 'number' && Number.isFinite(remaining)) {
        chips.push(`<span class="hn-chip hn-small">${iconHtml('clock')}remaining: ${escapeHtml(String(Math.max(0, Math.round(remaining))))}m</span>`);
      } else if (showRemaining && remainingText) {
        chips.push(`<span class="hn-chip hn-small">${iconHtml('clock')}remaining: ${escapeHtml(remainingText)}</span>`);
      }
    }

    if (tv) {
      if (typeof mapped?.volume === 'number') {
        chips.push(`<span class="hn-chip hn-small">${iconHtml('volume')}vol: ${escapeHtml(String(mapped.volume))}</span>`);
      }
      if (typeof mapped?.muted === 'boolean') {
        chips.push(`<span class="hn-chip hn-small ${mapped.muted ? 'hn-chip--err' : 'hn-chip--ok'}">${iconHtml(mapped.muted ? 'mute' : 'volume')}${mapped.muted ? 'muted' : 'unmuted'}</span>`);
      }
    }

    stateValuesEl.innerHTML = chips.join('') || '<span class="hn-muted hn-small">No state values.</span>';
  }
}

async function sendCommand(deviceId, command, args) {
  const res = await fetch(`${basePath}/api/admin/device-command`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ device_id: deviceId, command, args: args || {} }),
  });

  if (!res.ok) {
    let msg = `Command failed (${res.status})`;
    try {
      const payload = await res.json();
      if (payload?.error) msg = payload.error;
    } catch {
    }
    throw new Error(msg);
  }
}

function beginCommandPending(deviceId) {
  const baseline = deviceStateFingerprint(state.devices?.[deviceId]);
  suppressRealtimeUntilAfterNextRest();
  state.commandPendingUntil = Date.now() + 9000;
  state.commandGraceUntil = Date.now() + 1000;
  state.commandPendingDeviceId = deviceId;
  state.commandBaseline = baseline;
  state.commandStableUntil = 0;
  state.commandObservedFingerprint = '';
  state.commandDisableUntil = Date.now() + COMMAND_DISABLE_MS;
  scheduleUIWakeAt(state.commandDisableUntil + 20);
}

function cancelCommandPendingAfterFailure() {
  state.commandPendingUntil = 0;
  state.commandGraceUntil = 0;
  state.commandPendingDeviceId = '';
  state.commandBaseline = '';
  state.commandStableUntil = 0;
  state.commandObservedFingerprint = '';
  state.commandDisableUntil = 0;
  state.commandLockUntil = 0;
  state.commandLockDeviceId = '';
  state.commandLockedFingerprint = '';
  state.commandLastBaseline = '';
  state.realtimeAwaitingRest = false;
  state.realtimeSuppressUntil = 0;
  if (pendingClearTimer) {
    clearTimeout(pendingClearTimer);
    pendingClearTimer = null;
  }
}

function attachControlHandlers() {
  if (!controlsEl) return;

  controlsEl.querySelectorAll('.cmd-power').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const deviceId = String(btn.getAttribute('data-device-id') || '');
      const target = String(btn.getAttribute('data-target') || '');
      try {
        setStatus(`Sending power=${target}…`, null);
        beginCommandPending(deviceId);
        await sendCommand(deviceId, 'set_power', { power: target });
        setStatus('Command queued. Updating…', true);
      } catch (err) {
        cancelCommandPendingAfterFailure();
        setStatus(err.message || 'Command failed.', false);
        renderSelected();
      }
    });
  });

  controlsEl.querySelectorAll('.cmd-btn').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const deviceId = String(btn.getAttribute('data-device-id') || '');
      const command = String(btn.getAttribute('data-command') || '');
      try {
        setStatus(`Sending ${command}…`, null);
        beginCommandPending(deviceId);
        await sendCommand(deviceId, command, {});
        setStatus('Command queued. Updating…', true);
      } catch (err) {
        cancelCommandPendingAfterFailure();
        setStatus(err.message || 'Command failed.', false);
        renderSelected();
      }
    });
  });

  controlsEl.querySelectorAll('.cmd-select').forEach((select) => {
    select.addEventListener('change', async () => {
      const deviceId = String(select.getAttribute('data-device-id') || '');
      const command = String(select.getAttribute('data-command') || '');
      const prop = String(select.getAttribute('data-prop') || '');
      const value = String(select.value || '');
      const args = prop ? { [prop]: value } : { value };
      try {
        setStatus(`Sending ${command}…`, null);
        beginCommandPending(deviceId);
        await sendCommand(deviceId, command, args);
        setStatus('Command queued. Updating…', true);
      } catch (err) {
        cancelCommandPendingAfterFailure();
        setStatus(err.message || 'Command failed.', false);
        renderSelected();
      }
    });
  });

  controlsEl.querySelectorAll('.cmd-range').forEach((slider) => {
    slider.addEventListener('change', async () => {
      const deviceId = String(slider.getAttribute('data-device-id') || '');
      const command = String(slider.getAttribute('data-command') || '');
      const prop = String(slider.getAttribute('data-prop') || '');
      const raw = String(slider.value || '');
      const n = Number(raw);
      const value = Number.isFinite(n) ? n : raw;
      const args = prop ? { [prop]: value } : { value };
      try {
        setStatus(`Sending ${command}…`, null);
        beginCommandPending(deviceId);
        await sendCommand(deviceId, command, args);
        setStatus('Command queued. Updating…', true);
      } catch (err) {
        cancelCommandPendingAfterFailure();
        setStatus(err.message || 'Command failed.', false);
        renderSelected();
      }
    });
  });
}

function applySnapshot(payload) {
  const setup = payload?.setup || {};
  state.realtimeEnabled = setup?.realtime_enabled !== false;
  state.realtimeTransport = String(setup?.realtime_transport || '').trim().toLowerCase();
  if (rtInfoEl) rtInfoEl.style.display = 'none';

  const devices = payload?.devices || {};

  const lockActive = Date.now() < (state.commandLockUntil || 0) && state.commandLockDeviceId;
  if (lockActive) {
    const lockId = state.commandLockDeviceId;
    const incoming = devices?.[lockId];
    const incomingFp = deviceStateFingerprint(incoming);
    const prevDev = state.devices?.[lockId];
    const prevFp = deviceStateFingerprint(prevDev);

    if (prevDev && state.commandLockedFingerprint && prevFp === state.commandLockedFingerprint) {
      if (!incoming) {
        devices[lockId] = prevDev;
      } else if (state.commandLastBaseline && incomingFp === state.commandLastBaseline) {
        // Ignore delayed rollback to pre-command baseline for a short period.
        devices[lockId] = prevDev;
      }
    }
  } else if (state.commandLockUntil) {
    state.commandLockUntil = 0;
    state.commandLockDeviceId = '';
    state.commandLockedFingerprint = '';
    state.commandLastBaseline = '';
  }

  // During a pending command, avoid applying snapshots that regress the pending device
  // back to the original baseline after we've already seen a post-command change.
  const pendingActive = Date.now() < state.commandPendingUntil && state.commandPendingDeviceId;
  if (pendingActive) {
    const pendingId = state.commandPendingDeviceId;
    const incoming = devices?.[pendingId];
    const incomingFp = deviceStateFingerprint(incoming);
    const prevDev = state.devices?.[pendingId];
    const prevFp = deviceStateFingerprint(prevDev);
    const baseline = state.commandBaseline;

    if (baseline && prevFp && prevFp !== baseline && !state.commandObservedFingerprint) {
      state.commandObservedFingerprint = prevFp;
    }

    if (baseline && incomingFp && incomingFp !== baseline) {
      // Record that we've seen a non-baseline snapshot for this command.
      state.commandObservedFingerprint = incomingFp;
    }

    if (baseline && incomingFp === baseline && prevDev && prevFp && prevFp !== baseline) {
      // Avoid a quick rollback to baseline while a command is still pending.
      devices[pendingId] = prevDev;
    }

    if (baseline && state.commandObservedFingerprint && incomingFp === baseline && prevDev) {
      // Ignore regression to baseline; keep last known (newer) device state.
      devices[pendingId] = prevDev;
    } else if (baseline && state.commandObservedFingerprint && !incoming && prevDev) {
      // Defensive: if payload omits the device during a command, keep prior state.
      devices[pendingId] = prevDev;
    } else if (baseline && state.commandObservedFingerprint && incomingFp && prevFp && incomingFp !== prevFp && incomingFp !== baseline) {
      // If we get another non-baseline update, keep tracking it.
      state.commandObservedFingerprint = incomingFp;
    }
  }

  state.devices = devices;
  state.deviceIds = Object.keys(devices)
    .filter((id) => devices[id] && typeof devices[id] === 'object')
    .sort((a, b) => String(devices[a]?.name || a).localeCompare(String(devices[b]?.name || b)));

  if (!state.deviceIds.length) {
    if (deviceSelectEl) deviceSelectEl.innerHTML = '';
    if (controlsEl) controlsEl.innerHTML = '';
    if (deviceSelectEl) deviceSelectEl.disabled = true;
    if (deviceTitleEl) deviceTitleEl.textContent = 'No devices';
    if (deviceSubtitleEl) deviceSubtitleEl.textContent = 'No mapped devices yet.';
    if (deviceIconEl) deviceIconEl.innerHTML = iconHtml('plug', 'hn-icon');
    if (statusIconsEl) statusIconsEl.innerHTML = '';
    if (stateValuesEl) stateValuesEl.innerHTML = '';
    setMenuOpen(false);
    if (menuBtnEl) menuBtnEl.style.display = 'none';
    setStatus('No devices found.', null);
    return;
  }

  if (menuBtnEl) menuBtnEl.style.display = 'none';
  if (menuIconEl) menuIconEl.innerHTML = '';

  populateDeviceSelect();
  renderSelected();

  if (pendingActive) {
    const pendingDev = state.devices[state.commandPendingDeviceId];
    const current = deviceStateFingerprint(pendingDev);
    if (state.commandBaseline && current && current !== state.commandBaseline) {
      // First observed change: keep gate for a short stability window.
      if (!state.commandStableUntil) {
        state.commandStableUntil = Date.now() + REALTIME_RESUME_GRACE_MS;
        schedulePendingAutoClear(state.commandStableUntil + 30);
        scheduleUIWakeAt(state.commandStableUntil + 30);
        return;
      }
      if (Date.now() < state.commandStableUntil) {
        return;
      }
      clearPendingState({ statusMessage: 'Updated.' });
    }
    return;
  }

  setStatus('Ready.', null);
}

function wsURL() {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${basePath}/api/realtime/ws`;
}

let ws = null;
let wsReconnectTimer = null;
let wsConnected = false;
let wsHasMessage = false;

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
        if (!realtimeAllowed()) {
          return;
        }
        applySnapshot(payload);
      }
    });

    ws.addEventListener('close', () => {
      wsConnected = false;
      scheduleWSReconnect();
    });

    ws.addEventListener('error', () => {
      wsConnected = false;
      scheduleWSReconnect();
    });
  } catch {
    wsConnected = false;
    scheduleWSReconnect();
  }
}

if (deviceSelectEl) {
  deviceSelectEl.addEventListener('change', () => {
    state.selectedId = String(deviceSelectEl.value || '');
    renderSelected();
    setMenuOpen(false);
  });
}

function attachPickerToggle(el) {
  if (!el) return;
  el.addEventListener('click', (ev) => {
    ev.preventDefault();
    ev.stopPropagation();
    toggleMenu();
  });
  el.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter' || ev.key === ' ') {
      ev.preventDefault();
      toggleMenu();
    }
  });
}

attachPickerToggle(deviceTitleEl);
attachPickerToggle(deviceSubtitleEl);

window.addEventListener('DOMContentLoaded', () => {
  // no-op
});

connectWS();
