const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, 'widget.js'), 'utf8');
const context = vm.createContext({
  document: { getElementById() { return {}; } },
  window: { location: { pathname: '/widgets/lg-overview/', protocol: 'http:', host: 'localhost' } },
  setTimeout() {},
  clearTimeout() {},
  WebSocket: class { addEventListener() {} },
});
vm.runInContext(script, context);

test('summarizes dishwasher readings rather than a laundry cycle', () => {
  assert.match(context.summarizeState({ type: 'dish_washer', mapped_state: { 'runState.currentState': 'PAUSE' } }), /PAUSE/);
});

test('summarizes generic dryer readings when no laundry mapping exists', () => {
  assert.match(context.summarizeState({ type: 'dryer', mapped_state: { 'runState.currentState': 'PAUSE' } }), /PAUSE/);
});