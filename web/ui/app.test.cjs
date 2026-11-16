const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const context = vm.createContext({
  document: { getElementById() { return null; } },
  window: { location: { pathname: '/ui/' } },
});
vm.runInContext(script, context);

test('power readings do not imply power commands', () => {
  assert.equal(context.hasPowerCapability({ mapped_state: { power: 'on' }, inputs: [] }), false);
  assert.equal(context.hasPowerCapability({ inputs: [{ id: 'set_power', property: 'power' }] }), true);
});

test('power-saving toggle reflects state and sends boolean values', async () => {
  const input = { id: 'set_thinq_property', type: 'toggle', property: 'powerSave.powerSaveEnabled', label: 'Power saving' };
  const html = context.buildDeviceHTML('fridge', { online: true, mapped_state: { [input.property]: true }, inputs: [input] });
  assert.match(html, /class="cmd-toggle"[^>]*checked/);
  const unknown = context.buildDeviceHTML('fridge', { online: true, mapped_state: {}, inputs: [input] });
  assert.match(unknown, /class="cmd-toggle"[^>]*disabled/);
  const attrs = { 'data-device-id': 'fridge', 'data-command': input.id, 'data-prop': input.property };
  const toggle = { checked: true, disabled: false, getAttribute(name) { return attrs[name]; }, addEventListener(event, handler) { this[event] = handler; } };
  context.testList = { querySelectorAll(selector) { return selector === '.cmd-toggle' ? [toggle] : []; } };
  vm.runInContext('elements.deviceList = testList; capturedToggleRequests = []; api = async (path, options) => capturedToggleRequests.push(JSON.parse(options.body)); scheduleCommandRefresh = () => {}; setStatus = () => {}', context);
  context.attachDeviceActionHandlers();
  await toggle.change();
  assert.equal(JSON.stringify(context.capturedToggleRequests[0]), '{"device_id":"fridge","command":"set_thinq_property","args":{"powerSave.powerSaveEnabled":true}}');
  vm.runInContext('api = async () => { throw new Error("offline"); }', context);
  toggle.checked = false;
  await toggle.change();
  assert.equal(toggle.checked, true);
  assert.equal(toggle.disabled, false);
});

test('setup defaults use the SDK country regions and respect an explicit region', () => {
  context.navigator = { language: 'en-AU' };
  assert.equal(context.detectRegionWithoutGps(), 'kr');
  assert.equal(context.detectCountryWithoutGps('kr'), 'AU');
  context.navigator = { language: 'en-CA' };
  assert.equal(context.detectRegionWithoutGps(), 'us');
  context.navigator = { language: 'hu-HU' };
  assert.equal(context.detectCountryWithoutGps('us'), 'US');
  assert.equal(context.detectCountryWithoutGps('eu'), 'HU');
  const countries = context.sdkFallbackCountries();
  assert.equal(countries.length, 162);
  assert.ok(countries.some((country) => country.code === 'AU'));
  assert.ok(countries.some((country) => country.code === 'CA'));
});

test('select controls reflect the reported value and escape profile labels', () => {
  const html = context.buildDeviceHTML('purifier', {
    type: 'air_purifier', online: true,
    mapped_state: { 'operation.airPurifierOperationMode': 'POWER_OFF' },
    inputs: [{ id: 'set_thinq_property', type: 'select', property: 'operation.airPurifierOperationMode', label: 'Power mode', options: [
      { value: 'POWER_ON', label: '<On>' }, { value: 'POWER_OFF', label: 'Off' },
    ] }],
  });
  assert.match(html, /value="POWER_OFF" selected/);
  assert.match(html, /&lt;On&gt;/);
});

test('range controls show the reported value and bounds', () => {
  const html = context.buildDeviceHTML('fridge', {
    type: 'refrigerator', online: true,
    mapped_state: { 'temperature.FRIDGE.C.targetTemperature': 4 },
    inputs: [{ id: 'set_thinq_property', type: 'range', property: 'temperature.FRIDGE.C.targetTemperature', label: 'Fridge temperature', range: { min: 1, max: 7, step: 1 } }],
  });
  assert.match(html, /class="cmd-range"/);
  assert.match(html, /min="1" max="7" step="1" value="4"/);
  assert.doesNotMatch(html, /remote ready/);
});

test('paired range controls include both reported values in commands', () => {
  const html = context.buildDeviceHTML('hood', {
    type: 'hood', online: true,
    mapped_state: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 },
    inputs: [{ id: 'set_thinq_pair', type: 'range', property: 'lamp.lampBrightness', paired_property: 'ventilation.fanSpeed', range: { min: 0, max: 2, step: 1 } }],
  });
  assert.match(html, /data-paired-prop="ventilation.fanSpeed"/);
  assert.match(html, /data-paired-value="0"/);
  const args = context.rangeCommandArgs('lamp.lampBrightness', 2, 'ventilation.fanSpeed', '0');
  assert.equal(JSON.stringify(args), '{"lamp.lampBrightness":2,"ventilation.fanSpeed":0}');
  assert.equal(context.rangeCommandArgs('lamp.lampBrightness', 2, 'ventilation.fanSpeed', ''), null);
});

test('paired sliders stage both visible values and apply once', async () => {
  function slider(property, pairedProperty, value) {
    const attributes = { 'data-device-id': 'hood', 'data-command': 'set_thinq_pair', 'data-prop': property, 'data-paired-prop': pairedProperty };
    return { value, disabled: false, getAttribute(name) { return attributes[name]; }, addEventListener(event, handler) { this[event] = handler; } };
  }
  const lamp = slider('lamp.lampBrightness', 'ventilation.fanSpeed', '2');
  const fan = slider('ventilation.fanSpeed', 'lamp.lampBrightness', '3');
  const button = { disabled: false, getAttribute() { return 'hood'; }, closest() { return { querySelectorAll() { return [lamp, fan]; } }; }, addEventListener(event, handler) { this[event] = handler; } };
  context.testList = { querySelectorAll(selector) { return selector === '.cmd-range' ? [lamp, fan] : selector === '.cmd-pair-apply' ? [button] : []; } };
  vm.runInContext('elements.deviceList = testList; capturedRequests = []; api = (path, options) => { capturedRequests.push(JSON.parse(options.body)); return new Promise(resolve => finishRequest = resolve); }; scheduleCommandRefresh = () => {}; setStatus = () => {}', context);
  context.attachDeviceActionHandlers();
  await lamp.change();
  assert.equal(context.capturedRequests.length, 0);
  const pending = button.click();
  await button.click();
  assert.equal(context.capturedRequests.length, 1);
  assert.equal(JSON.stringify(context.capturedRequests[0]), '{"device_id":"hood","command":"set_thinq_pair","args":{"lamp.lampBrightness":2,"ventilation.fanSpeed":3}}');
  context.finishRequest();
  await pending;
  assert.equal(button.disabled, true);
});

test('paired drafts survive unchanged snapshots but not changed device state', () => {
  const inputs = [
    { id: 'set_thinq_pair', type: 'range', property: 'lamp.lampBrightness', paired_property: 'ventilation.fanSpeed', range: { min: 0, max: 3, step: 1 } },
    { id: 'set_thinq_pair', type: 'range', property: 'ventilation.fanSpeed', paired_property: 'lamp.lampBrightness', range: { min: 0, max: 3, step: 1 } },
  ];
  const device = { type: 'hood', online: true, mapped_state: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 }, inputs };
  context.testPair = { baseline: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 }, values: { 'lamp.lampBrightness': 2, 'ventilation.fanSpeed': 3 } };
  vm.runInContext('pairDrafts.set("hood-draft", testPair)', context);
  let html = context.buildDeviceHTML('hood-draft', device);
  assert.match(html, /value="2"[^>]*data-prop="lamp\.lampBrightness"/);
  assert.match(html, /value="3"[^>]*data-prop="ventilation\.fanSpeed"/);
  device.mapped_state['ventilation.fanSpeed'] = 1;
  html = context.buildDeviceHTML('hood-draft', device);
  assert.match(html, /value="1"[^>]*data-prop="lamp\.lampBrightness"/);
  assert.doesNotMatch(html, /value="3"[^>]*data-prop="ventilation\.fanSpeed"/);
});

test('paired controls are disabled when a value or companion input is missing', () => {
  const lamp = { id: 'set_thinq_pair', type: 'range', property: 'lamp.lampBrightness', paired_property: 'ventilation.fanSpeed', range: { min: 0, max: 3, step: 1 } };
  const fan = { id: 'set_thinq_pair', type: 'range', property: 'ventilation.fanSpeed', paired_property: 'lamp.lampBrightness', range: { min: 0, max: 3, step: 1 } };
  for (const device of [
    { online: true, mapped_state: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 }, inputs: [lamp] },
    { online: true, mapped_state: { 'lamp.lampBrightness': 1 }, inputs: [lamp, fan] },
    { online: false, mapped_state: { 'lamp.lampBrightness': 1, 'ventilation.fanSpeed': 0 }, inputs: [lamp, fan] },
  ]) {
    const html = context.buildDeviceHTML('invalid-hood', device);
    assert.match(html, /class="hn-btn cmd-pair-apply"[^>]*disabled/);
  }
});

test('failed paired request re-enables controls for retry', async () => {
  function slider(property, pairedProperty, value) {
    const attributes = { 'data-device-id': 'failure', 'data-command': 'set_thinq_pair', 'data-prop': property, 'data-paired-prop': pairedProperty };
    return { value, disabled: false, getAttribute(name) { return attributes[name]; }, addEventListener(event, handler) { this[event] = handler; } };
  }
  const sliders = [slider('lamp.lampBrightness', 'ventilation.fanSpeed', '2'), slider('ventilation.fanSpeed', 'lamp.lampBrightness', '3')];
  const button = { disabled: false, getAttribute() { return 'failure'; }, closest() { return { querySelectorAll() { return sliders; } }; }, addEventListener(event, handler) { this[event] = handler; } };
  context.testList = { querySelectorAll(selector) { return selector === '.cmd-range' ? sliders : selector === '.cmd-pair-apply' ? [button] : []; } };
  vm.runInContext('elements.deviceList = testList; api = async () => { throw new Error("offline"); }; setStatus = () => {}; renderCount = 0; renderDevices = () => { renderCount++; }', context);
  context.attachDeviceActionHandlers();
  await button.click();
  assert.equal(button.disabled, false);
  assert.ok(sliders.every((item) => !item.disabled));
  assert.equal(context.renderCount, 1);
});