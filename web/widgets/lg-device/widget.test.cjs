const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, 'widget.js'), 'utf8');

function renderWidget(search, devices) {
  const elements = new Map();
  function element() {
    return { style: {}, textContent: '', innerHTML: '', value: '', appendChild() {}, addEventListener() {}, querySelectorAll() { return []; } };
  }
  const document = {
    cookie: '',
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, element());
      return elements.get(id);
    },
    createElement: element,
  };
  const window = { location: { pathname: '/widgets/lg-device/', search, protocol: 'http:', host: 'localhost' }, addEventListener() {} };
  const context = vm.createContext({ document, window, URLSearchParams, setTimeout() {}, clearTimeout() {}, WebSocket: class { addEventListener() {} } });
  vm.runInContext(script, context);
  context.testDevices = devices;
  vm.runInContext('applySnapshot({ devices: testDevices })', context);
  elements.context = context;
  return elements;
}

const devices = {
  'lg/washer': { name: 'Washer', type: 'washer', online: true, mapped_state: { power: 'off' } },
  'lg/tv': { name: 'Living room TV', type: 'tv', online: true, mapped_state: { power: 'on' } },
};

test('uses the configured device and retains the alphabetical default', () => {
  assert.equal(renderWidget('?device_id=lg%2Fwasher', devices).get('deviceTitle').textContent, 'Washer');
  assert.equal(renderWidget('', devices).get('deviceTitle').textContent, 'Living room TV');
});

test('power-saving toggle reflects state and sends boolean values', async () => {
  const input = { id: 'set_thinq_property', type: 'toggle', property: 'powerSave.powerSaveEnabled', label: 'Power saving' };
  const elements = renderWidget('', { fridge: { online: true, mapped_state: { [input.property]: true }, inputs: [input] } });
  assert.match(elements.get('controls').innerHTML, /class="cmd-toggle"[^>]*checked/);
  const attrs = { 'data-device-id': 'fridge', 'data-command': input.id, 'data-prop': input.property };
  const toggle = { checked: false, disabled: false, getAttribute(name) { return attrs[name]; }, addEventListener(event, handler) { this[event] = handler; } };
  elements.get('controls').querySelectorAll = (selector) => selector === '.cmd-toggle' ? [toggle] : [];
  vm.runInContext('capturedToggleCommands = []; sendCommand = async (...args) => capturedToggleCommands.push(args)', elements.context);
  elements.context.attachControlHandlers();
  await toggle.change();
  assert.equal(JSON.stringify(elements.context.capturedToggleCommands), '[["fridge","set_thinq_property",{"powerSave.powerSaveEnabled":false}]]');
});

test('does not silently control a different device when the configured one is missing', () => {
  assert.equal(renderWidget('?device_id=missing', devices).get('deviceTitle').textContent, 'No device');
});

test('shows actual readings without an invented power control for a read-only device', () => {
  const elements = renderWidget('', {
    purifier: { name: 'Water purifier', type: 'water_purifier', online: true, mapped_state: { 'runState.cockState': 'NORMAL' }, inputs: [] },
  });
  assert.match(elements.get('controls').innerHTML, /No controls exposed/);
  assert.match(elements.get('stateValues').innerHTML, /NORMAL/);
  assert.doesNotMatch(elements.get('controls').innerHTML, /cmd-power/);
});

test('shows dishwasher readings instead of treating it as a laundry washer', () => {
  const elements = renderWidget('', {
    dishwasher: { name: 'Dishwasher', type: 'dish_washer', online: true, mapped_state: { 'runState.currentState': 'PAUSE' }, inputs: [] },
  });
  assert.match(elements.get('stateValues').innerHTML, /PAUSE/);
});

test('select controls reflect the reported profile value', () => {
  const elements = renderWidget('', {
    purifier: { name: 'Purifier', type: 'air_purifier', online: true, mapped_state: { 'operation.airPurifierOperationMode': 'POWER_OFF' }, inputs: [
      { id: 'set_thinq_property', type: 'select', property: 'operation.airPurifierOperationMode', options: [{ value: 'POWER_ON' }, { value: 'POWER_OFF' }] },
    ] },
  });
  assert.match(elements.get('controls').innerHTML, /value="POWER_OFF" selected/);
});

test('range controls do not invent a current value', () => {
  const elements = renderWidget('', {
    fridge: { name: 'Fridge', type: 'refrigerator', online: true, mapped_state: {}, inputs: [
      { id: 'set_thinq_property', type: 'range', property: 'temperature.FRIDGE.C.targetTemperature', range: { min: 0, max: 7, step: 1 } },
    ] },
  });
  assert.match(elements.get('controls').innerHTML, /disabled/);
});

test('paired controls include both values in widget commands', () => {
  const elements = renderWidget('', {
    hood: { type: 'hood', online: true, mapped_state: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 }, inputs: [
      { id: 'set_thinq_pair', type: 'range', property: 'lamp.lampBrightness', paired_property: 'ventilation.fanSpeed', range: { min: 0, max: 2, step: 1 } },
    ] },
  });
  assert.match(elements.get('controls').innerHTML, /data-paired-prop="ventilation.fanSpeed"/);
  assert.match(elements.get('controls').innerHTML, /data-paired-value="0"/);
  const args = elements.context.rangeCommandArgs('lamp.lampBrightness', 2, 'ventilation.fanSpeed', '0');
  assert.equal(JSON.stringify(args), '{"lamp.lampBrightness":2,"ventilation.fanSpeed":0}');
  assert.equal(elements.context.rangeCommandArgs('lamp.lampBrightness', 2, 'ventilation.fanSpeed', ''), null);
});

test('range controls show their labels and current values', () => {
  const elements = renderWidget('', {
    hood: { type: 'hood', online: true, mapped_state: { 'lamp.lampBrightness': 1 }, inputs: [
      { id: 'set_thinq_property', label: 'Lamp brightness', type: 'range', property: 'lamp.lampBrightness', range: { min: 0, max: 2, step: 1 } },
    ] },
  });
  assert.match(elements.get('controls').innerHTML, /Lamp brightness/);
  assert.match(elements.get('controls').innerHTML, /<output[^>]*>1<\/output>/);
});

test('paired drafts survive unchanged snapshots but not changed device state', () => {
  const inputs = [
    { id: 'set_thinq_pair', type: 'range', property: 'lamp.lampBrightness', paired_property: 'ventilation.fanSpeed', range: { min: 0, max: 3, step: 1 } },
    { id: 'set_thinq_pair', type: 'range', property: 'ventilation.fanSpeed', paired_property: 'lamp.lampBrightness', range: { min: 0, max: 3, step: 1 } },
  ];
  const device = { type: 'hood', online: true, mapped_state: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 }, inputs };
  const elements = renderWidget('', { hood: device });
  elements.context.testPair = { baseline: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 }, values: { 'lamp.lampBrightness': 2, 'ventilation.fanSpeed': 3 } };
  vm.runInContext('pairDrafts.set("hood", testPair); renderSelected()', elements.context);
  assert.match(elements.get('controls').innerHTML, /value="2"[^>]*data-prop="lamp\.lampBrightness"/);
  assert.match(elements.get('controls').innerHTML, /value="3"[^>]*data-prop="ventilation\.fanSpeed"/);
  device.mapped_state['ventilation.fanSpeed'] = 1;
  elements.context.renderSelected();
  assert.match(elements.get('controls').innerHTML, /value="1"[^>]*data-prop="lamp\.lampBrightness"/);
  assert.doesNotMatch(elements.get('controls').innerHTML, /value="3"[^>]*data-prop="ventilation\.fanSpeed"/);
});

test('select and staged paired controls send their actual command arguments', async () => {
  const elements = renderWidget('', { hood: { type: 'hood', online: true, mapped_state: {}, inputs: [] } });
  function control(attributes, value) {
    return { value, disabled: false, getAttribute(name) { return attributes[name]; }, addEventListener(event, handler) { this[event] = handler; } };
  }
  const select = control({ 'data-device-id': 'hood', 'data-command': 'set_thinq_property', 'data-prop': 'operation.mode' }, 'POWER_ON');
  const lamp = control({ 'data-device-id': 'hood', 'data-command': 'set_thinq_pair', 'data-prop': 'lamp.lampBrightness', 'data-paired-prop': 'ventilation.fanSpeed' }, '2');
  const fan = control({ 'data-device-id': 'hood', 'data-command': 'set_thinq_pair', 'data-prop': 'ventilation.fanSpeed', 'data-paired-prop': 'lamp.lampBrightness' }, '3');
  const button = control({ 'data-device-id': 'hood' }, '');
  elements.get('controls').querySelectorAll = (selector) => selector === '.cmd-select' ? [select] : selector === '.cmd-range' ? [lamp, fan] : selector === '.cmd-pair-apply' ? [button] : [];
  vm.runInContext('capturedCommands = []; sendCommand = async (...args) => capturedCommands.push(args)', elements.context);
  elements.context.attachControlHandlers();
  await select.change();
  await lamp.change();
  assert.equal(elements.context.capturedCommands.length, 1);
  await button.click();
  await button.click();
  assert.equal(JSON.stringify(elements.context.capturedCommands), '[["hood","set_thinq_property",{"operation.mode":"POWER_ON"}],["hood","set_thinq_pair",{"lamp.lampBrightness":2,"ventilation.fanSpeed":3}]]');
});