import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const source = readFileSync(new URL('../internal/web/static/app.js', import.meta.url), 'utf8');
const html = readFileSync(new URL('../internal/web/static/index.html', import.meta.url), 'utf8');
const response = (status, data) => ({ok:status >= 200 && status < 300, status, json:async () => data});
const missing = () => response(404, {error:'No migration job'});
const job = (extra = {}) => ({ID:'analysis-1', Phase:'cloning', Complete:false, Progress:50, Started:new Date().toISOString(), Updated:new Date().toISOString(), Message:'Cloning', ...extra});

// Run the shipped script with a small DOM/fetch fixture. No browser library or
// build step is needed to exercise request failures and page restoration.
async function page(reply = () => response(401, {error:'Sign in'})) {
 const elements = new Map(), controls = [], modes = [], calls = [], timers = new Map();
 let nextTimer = 0;
 const element = () => ({
  hidden:false, disabled:false, checked:false, value:'', textContent:'', dataset:{}, listeners:{},
  addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); },
  replaceChildren() {}, append() {}, scrollIntoView() {},
  removeAttribute(name) { delete this[name]; }
 });
 for (const match of html.matchAll(/<(\w+)\b([^>]*)>/g)) {
  const [, tag, attrs] = match;
  const id = attrs.match(/\bid="([^"]+)"/)?.[1];
  const el = id && elements.has(id) ? elements.get(id) : element();
  if (id) elements.set(id, el);
  el.disabled = /\bdisabled\b/.test(attrs);
  el.hidden = /\bhidden\b/.test(attrs);
  el.checked = /\bchecked\b/.test(attrs);
  el.value = attrs.match(/\bvalue="([^"]*)"/)?.[1] || '';
  if (['button', 'input', 'select', 'textarea'].includes(tag) && id !== 'theme') controls.push(el);
  if (attrs.includes('name="mode"')) modes.push(el);
 }
 const context = vm.createContext({
  document:{
   getElementById:id => elements.get(id), createElement:element, documentElement:{dataset:{}},
   querySelector:() => modes.find(el => el.checked),
   querySelectorAll:selector => selector === 'input[name=mode]' ? modes : controls
  },
  fetch:async (url, options) => { calls.push({path:url.slice(5), ...options}); return reply(url.slice(5), options); },
  matchMedia:() => ({matches:false}), confirm:() => true,
  setTimeout:fn => { timers.set(++nextTimer, fn); return nextTimer; }, clearTimeout:id => timers.delete(id),
  setInterval:() => 1, clearInterval:() => {},
 });
 vm.runInContext(source, context);
 await new Promise(resolve => setImmediate(resolve));
 return {
  elements, controls, calls, timers,
  reply(fn) { reply = fn; },
  run(code) { return vm.runInContext(code, context); },
  async fire(id, event) {
   const el = elements.get(id);
   if (!el.disabled) await Promise.all((el.listeners[event] || []).map(fn => fn({preventDefault() {}})));
  },
  ready() { vm.runInContext("report = {ID:'analysis-1', Ready:true}; $('maintenance').checked = true; updateStart();", context); }
 };
}

test('a rejected start restores the button without retrying the POST', async () => {
 const p = await page();
 p.ready();
 p.reply(path => path === 'start' ? response(400, {error:'Start rejected'}) : missing());
 await p.fire('start', 'click');
 assert.equal(p.elements.get('start').disabled, false);
 assert.equal(p.elements.get('error').textContent, 'Start rejected');
 assert.equal(p.calls.filter(c => c.path === 'start').length, 1);
});

test('a lost start reply recovers the same job instead of starting again', async () => {
 const p = await page();
 p.ready();
 p.reply(path => {
  if (path === 'start') throw new TypeError('Network error');
  assert.equal(path, 'job');
  return response(200, job());
 });
 await p.fire('start', 'click');
 assert.equal(p.elements.get('job').hidden, false);
 assert.equal(p.elements.get('selection').hidden, true);
 assert.equal(p.elements.get('error').hidden, true);
 assert.equal(p.timers.size, 1);
 assert.equal(p.calls.filter(c => c.path === 'start').length, 1);
});

test('an unrelated old job is not mistaken for the requested migration', async () => {
 const p = await page();
 p.ready();
 p.elements.get('job').hidden = true;
 p.reply(path => path === 'start' ? response(400, {error:'Start rejected'}) : response(200, job({ID:'old-job', Complete:true})));
 await p.fire('start', 'click');
 assert.equal(p.elements.get('job').hidden, true);
 assert.equal(p.elements.get('error').textContent, 'Start rejected');
 assert.equal(p.elements.get('start').disabled, false);
});

test('an unconfirmed start requires status recovery before another start', async () => {
 const p = await page();
 p.ready();
 p.reply(() => { throw new TypeError('Network error'); });
 await p.fire('start', 'click');
 assert.equal(p.elements.get('start').disabled, true);
 assert.match(p.elements.get('error').textContent, /Reload this page/);
 await p.fire('start', 'click');
 assert.equal(p.calls.filter(c => c.path === 'start').length, 1);
});

test('analysis locks its inputs and ignores a second submission', async () => {
 const p = await page();
 let finish;
 p.reply(() => new Promise(resolve => { finish = resolve; }));
 p.elements.get('vm').value = '7';
 p.elements.get('datastore').value = 'target';
 const pending = p.fire('analyzeForm', 'submit');
 assert.equal(p.controls.every(el => el.disabled), true);
 await p.fire('analyzeForm', 'submit');
 assert.equal(p.calls.filter(c => c.path === 'analyze').length, 1);
 finish(response(200, {ID:'analysis-1', Ready:true, Request:{Mode:'COPY', TargetUUID:'target'}, VM:{Name:'lab'}, Checks:[], Disks:[]}));
 await pending;
 await new Promise(resolve => setImmediate(resolve));
 assert.equal(p.elements.get('error').hidden, true, p.elements.get('error').textContent);
 assert.equal(p.elements.get('vm').disabled, false);
 assert.equal(p.elements.get('analysis').hidden, false);
 assert.equal(p.elements.get('start').disabled, true);
 p.elements.get('maintenance').checked = true;
 await p.fire('maintenance', 'change');
 assert.equal(p.elements.get('start').disabled, false);
});

for (const phase of ['cloning', 'unknown']) {
 test(`reload restores a ${phase} job with the correct outcome`, async () => {
  const p = await page(path => path === 'session'
   ? response(200, {csrf:'fixture', connected:true, address:'fixture', hasJob:true})
   : response(200, job({Phase:phase, Complete:phase === 'unknown', Message:'The clone may still be running on ESXi.'})));
  assert.equal(p.elements.get('login').hidden, true);
  assert.equal(p.elements.get('connect').hidden, true);
  assert.equal(p.elements.get('job').hidden, false);
  if (phase === 'unknown') {
   assert.equal(p.elements.get('jobTitle').textContent, 'Outcome unknown');
   assert.equal(p.elements.get('job').dataset.state, 'warn');
   assert.equal(p.elements.get('percent').textContent, '50% last reported');
   assert.equal(p.timers.size, 0);
  } else {
   assert.equal(p.elements.get('jobTitle').textContent, 'Migration in progress');
   assert.equal(p.timers.size, 1);
  }
 });
}
