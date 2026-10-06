const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const turn = () => new Promise(setImmediate);

class Node extends EventTarget {
  constructor() {
    super();
    this.textContent = '';
    this.innerHTML = '';
    this.hidden = false;
    this.open = false;
    this.returnValue = '';
    this.opens = 0;
    this.classList = {toggle(){}};
  }
  setAttribute() {}
  querySelector() { return null; }
  querySelectorAll() { return []; }
  focus() { if(this.ownerDocument)this.ownerDocument.activeElement=this; }
  contains(node){return node?.ownedBy===this;}
  showModal() { assert.equal(this.open, false, 'A second modal must not open.'); this.open = true; this.opens++; }
  close(value) {
    if (!this.open) return;
    if (value !== undefined) this.returnValue = value;
    this.open = false;
    this.dispatchEvent(new Event('close'));
  }
  escape() {
    const event = new Event('cancel', {cancelable:true});
    this.dispatchEvent(event);
    if (!event.defaultPrevented) this.close();
  }
}

async function harness(role = 'cashier', clockStart = Date.now()) {
  let currentTime = clockStart;
  class HarnessDate extends Date { static now(){return currentTime;} }
  const app = new Node(), dialog = new Node(), toast = new Node();
  const filters = {elements:{}, closest(selector){return selector === '#booking-filters' ? this : null;}};
  const startInput = new Node();
  app.querySelector = selector => selector === '#booking-filters' ? filters : selector === '#book-start' ? startInput : null;
  const fields = new Map(['title', 'description', 'total', 'amount-label', 'amount', 'details', 'approve', 'cancel']
    .map(name => ['#confirmation-' + name, new Node()]));
  dialog.querySelector = selector => fields.get(selector) || null;
  const handlers = new Map();
  app.addEventListener = (name, handler) => handlers.set(name, handler);
  const users = [
    {id:1, name:'Player', role:'customer'},
    {id:2, name:'Cashier', role:'cashier'},
    {id:3, name:'Admin', role:'admin'}
  ];
  const user = users.find(u => u.role === role);
  const iso = offset => new Date(currentTime + offset).toISOString();
  const data = {
    user, users, tiers:[{id:1, name:'Standard', price_pesos:'30.55'}],
    pcs:[{id:1, code:'NS-01', tier_id:1, enabled:true, specs:'Gaming PC'}],
    bookings:[
      {id:7, pc_id:1, reference:'NS-TEST', user_id:1, status:'held', start_at:iso(3600000), end_at:iso(5400000), hold_until:iso(600000)},
      {id:8, pc_id:1, reference:'NS-PAST', user_id:1, status:'completed', start_at:iso(-7200000), end_at:iso(-5400000)}
    ],
    items:[{id:11, booking_id:7, kind:'base', status:'held', amount_pesos:'30.55', slots:1, hold_until:iso(600000)}],
    payments:[
      {id:31, booking_id:8, amount_pesos:'30.55', method:'cash', reference:'PAY-31'},
      {id:32, booking_id:8, amount_pesos:'45.75', method:'cash', reference:'PAY-32'},
      {id:33, booking_id:8, amount_pesos:'20.25', method:'cash', reference:'PAY-33'}
    ],
    refunds:[
      {id:41, booking_id:8, payment_id:31, amount_pesos:'30.55', status:'pending', reason:'Station issue'},
      {id:42, booking_id:8, payment_id:33, amount_pesos:'20.25', status:'approved', reason:'Approved return'}
    ],
    receipts:[], plays:[]
  };
  const requests = [], availabilityRequests = [], errors = [];
  let nextState, nextAvailability, nativePrompts = 0;
  const context = {
    Date:HarnessDate,
    document: {
      querySelector: selector => selector === '#app' ? app : selector === '#toast' ? toast : selector === '#confirmation-dialog' ? dialog : null,
      querySelectorAll: () => [], addEventListener(){}, hidden:false
    },
    window:{addEventListener(){}, scrollTo(){}},
    localStorage:{getItem(){return null;}},
    crypto:{randomUUID:()=>'test-key'},
    FormData: class { constructor(form){this.values=form.values;} get(name){return this.values[name];} },
    setTimeout:()=>0, clearTimeout(){}, setInterval:()=>0,
    confirm(){nativePrompts++; return false;},
    fetch: async (url, options) => {
      if (options.method === 'POST') requests.push({url, body:JSON.parse(options.body)});
      if (url.startsWith('/api/availability?')) {
        availabilityRequests.push(url);
        if (nextAvailability) {
          const response = nextAvailability;
          nextAvailability = null;
          return response;
        }
      }
      if (url === '/api/state' && nextState) {
        const response = nextState;
        nextState = null;
        return {ok:true, json:()=>response};
      }
      return {ok:true, json:async()=>url === '/api/me' ? {user, csrf:'test-csrf'} : url === '/api/state' ? data : url.startsWith('/api/availability?') ? {pcs:data.pcs} : {}};
    }
  };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8'), context);
  await turn();
  function invoke(name, target) {
    return Promise.resolve(handlers.get(name)({target, preventDefault(){}})).catch(error => { errors.push(error); });
  }
  async function openView(name) {
    const view = {dataset:{view:name}, closest(selector){return selector === 'button' ? this : null;}};
    await invoke('click', view);
    await turn();
  }
  async function changeBooking(start, slots = 2) {
    filters.elements = {
      start:{value:new Date(new Date(start).getTime() + 8*3600000).toISOString().slice(0,16)},
      slots:{value:String(slots)}, tier:{value:'all'}
    };
    await invoke('change', filters);
    await turn();
  }
  function renderedStart() {
    const input = app.innerHTML.match(/<input\b(?=[^>]*\bid="book-start")[^>]*>/)?.[0];
    return input?.match(/\bvalue="([^"]*)"/)?.[1];
  }
  return {
    app, dialog, fields, data, requests, availabilityRequests, errors, openView, changeBooking, renderedStart,
    advance(milliseconds){currentTime += milliseconds;},
    get nativePrompts(){return nativePrompts;},
    cashierTab(tab){const button={dataset:{cashierTab:tab},closest(selector){return selector==='button'?this:null;}};return invoke('click',button);},
    click(action, id) {
      const button = {dataset:{action, id:String(id)}, closest(selector){return selector === 'button' ? this : null;}};
      return invoke('click', button);
    },
    submit(type, id, values) {
      const form = {dataset:{form:type, [type === 'payment' ? 'item' : 'payment']:String(id)}, values,
        closest(selector){return selector === 'form[data-form]' ? this : null;}};
      return invoke('submit', form);
    },
    changePaymentMethod(value, reference, help) {
      const form = {
        dataset:{form:'payment', item:'11'}, elements:{reference},
        querySelector(selector){return selector.includes('help') ? help : selector.includes('reference') ? reference : null;}
      };
      const target = {name:'method', value, closest(selector){return selector === 'form[data-form="payment"]' ? form : null;}};
      return invoke('change', target);
    },
    async chooseBooking(start, slots = 2, selectSeat = true) {
      await openView('book');
      await changeBooking(start, slots);
      if (!selectSeat) return;
      const seat = {dataset:{seat:'1'}, closest(selector){return selector === 'button' ? this : null;}};
      await invoke('click', seat);
    },
    async selectBooking(name, value) {
      const start = renderedStart();
      assert.ok(start, 'The canonical hidden start must be available.');
      filters.elements = {
        start:{value:start}, date:{name:'date', value:start.slice(0,10)}, time:{name:'time', value:start.slice(11,16)},
        slots:{value:'2'}, tier:{value:'all'}
      };
      const target = filters.elements[name];
      target.id = 'book-' + name;
      target.value = value;
      target.closest = selector => selector === '#booking-filters' ? filters : null;
      await invoke('change', target);
      await turn();
    },
    deferAvailability() {
      let resolve;
      nextAvailability = new Promise(done => {resolve = done;});
      return pcs => resolve({ok:true, json:async()=>({pcs})});
    },
    failNextAvailability() {
      nextAvailability = Promise.resolve({ok:false, status:503, json:async()=>({error:'Availability service temporarily unavailable'})});
    },
    deferState() {
      let resolve;
      nextState = new Promise(done => {resolve = done;});
      return resolve;
    }
  };
}

function bookingSeat(h, id = 1) {
  const seat = h.app.innerHTML.match(new RegExp('<button\\b(?=[^>]*\\bdata-seat="' + id + '")[^>]*>'))?.[0];
  assert.ok(seat, 'The PC must remain visible.');
  return seat;
}

test('legacy hardware lists GPU and RAM while leaving the unknown CPU unspecified', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:00:00Z'));
  h.data.pcs[0].specs = 'RTX 4060 · 16 GB';
  await h.openView('book');
  const card = h.app.innerHTML.match(/<button\b(?=[^>]*\bdata-seat="1")[\s\S]*?<\/button>/)?.[0];
  assert.ok(card);
  const text = card.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');
  assert.match(text, /CPU\s*:?\s*Not specified/i, 'Unknown hardware must not be invented.');
  assert.match(text, /RAM\s*:?\s*16 GB/i);
  assert.match(text, /GPU\s*:?\s*RTX 4060/i);
  const table = h.app.innerHTML.match(/<table\b[\s\S]*?<\/table>/)?.[0];
  assert.ok(table);
  const listText = table.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');
  for (const field of ['CPU', 'RAM', 'GPU', 'Not specified', '16 GB', 'RTX 4060']) {
    assert.ok(listText.includes(field), `The seat list must preserve ${field}.`);
  }
  assert.deepEqual(h.errors, []);
});

test('labeled CPU RAM and GPU render safely in the selected PC and booking review', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:00:00Z'));
  h.data.pcs[0].specs = 'CPU: AMD Ryzen 7 7800X3D <cache> · RAM: 32 GB DDR5 · GPU: NVIDIA RTX 4070 <OC>';
  await h.chooseBooking('2030-01-01T14:15:00Z');
  const card = h.app.innerHTML.match(/<button\b(?=[^>]*\bdata-seat="1")[\s\S]*?<\/button>/)?.[0];
  const review = h.app.innerHTML.match(/<aside\b[^>]*\bclass="booking-side"[^>]*>[\s\S]*?<\/aside>/)?.[0];
  for (const region of [card, review]) {
    assert.ok(region);
    assert.ok(region.includes('AMD Ryzen 7 7800X3D &lt;cache&gt;'));
    assert.ok(region.includes('32 GB DDR5'));
    assert.ok(region.includes('NVIDIA RTX 4070 &lt;OC&gt;'));
    assert.equal(region.includes('<cache>') || region.includes('<OC>'), false, 'Hardware details must remain escaped text.');
    const text = region.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');
    assert.match(text, /CPU\s*:?\s*AMD Ryzen 7 7800X3D/);
    assert.match(text, /RAM\s*:?\s*32 GB DDR5/);
    assert.match(text, /GPU\s*:?\s*NVIDIA RTX 4070/);
  }
  assert.deepEqual(h.requests, []);
  assert.deepEqual(h.errors, []);
});

test('cash reference preview is readonly and switching payment methods resets reference requirements', async () => {
  const h = await harness();
  await h.openView('cashier');
  // Select the Payments tab after the Today overview default.
  await h.cashierTab('payments');
  const input = h.app.innerHTML.match(/<input\b(?=[^>]*\bid="reference-11")[^>]*>/)?.[0];
  assert.ok(input);
  assert.match(input, /\breadonly\b/i, 'Cash references must be assigned by the server.');
  assert.doesNotMatch(input, /\brequired\b/i);
  assert.match(input, /\bvalue="CASH-000004"/, 'Three legacy cash payments advance the displayed preview.');
  const reference = new Node(), help = new Node();
  reference.value = 'CASH-000004';
  reference.readOnly = true;
  reference.required = false;
  await h.changePaymentMethod('external', reference, help);
  assert.equal(reference.readOnly, false);
  assert.equal(reference.required, true);
  assert.equal(reference.value, '', 'A cash preview cannot become an external transaction reference.');
  assert.match(reference.placeholder, /GCash|Maya|bank/i);
  reference.value = 'EXTERNAL-REFERENCE';
  h.data.payments[0].reference = 'CASH-000010';
  await h.changePaymentMethod('cash', reference, help);
  assert.equal(reference.readOnly, true);
  assert.equal(reference.required, false);
  assert.equal(reference.value, 'CASH-000011', 'Cash must replace manual text and advance beyond existing numbered references.');
  assert.match(help.textContent, /preview/i);
  assert.deepEqual(h.requests, []);
  assert.deepEqual(h.errors, []);
});

test('cash confirmation displays a preview while approval sends no client-assigned reference', async () => {
  const h = await harness();
  const submit = () => h.submit('payment', 11, {method:'cash', reference:'CLIENT-CASH-999999'});
  let pending = submit();
  await turn();
  assert.equal(h.dialog.open, true);
  assert.equal(h.requests.length, 0);
  const details = h.fields.get('#confirmation-details').innerHTML;
  assert.ok(details.includes('CASH-000004'));
  assert.equal(details.includes('CLIENT-CASH-999999'), false);
  assert.match(details, /preview/i, 'The reviewed reference must be identified as a preview.');
  h.dialog.close('cancel');
  await pending;
  assert.deepEqual(h.requests, []);

  pending = submit();
  await turn();
  h.dialog.escape();
  await pending;
  assert.deepEqual(h.requests, []);

  pending = submit();
  await turn();
  h.dialog.close('confirm');
  await pending;
  assert.deepEqual(h.requests, [{url:'/api/payments', body:{item_id:11, method:'cash', reference:'', key:'test-key'}}]);
  assert.deepEqual(h.errors, []);
});

test('blank external transaction references cannot open confirmation or POST', async () => {
  const h = await harness();
  await h.submit('payment', 11, {method:'external', reference:'   '});
  assert.equal(h.dialog.open, false);
  assert.deepEqual(h.requests, []);
  assert.deepEqual(h.errors, []);
});

test('explicit date and quarter-hour selects clamp a time that crosses the midnight lead boundary', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T15:50:00Z'));
  await h.openView('book');
  assert.match(h.app.innerHTML, /<select\b[^>]*\bid="book-date"/, 'The date must use an explicit selectable day.');
  assert.match(h.app.innerHTML, /<select\b[^>]*\bid="book-time"/, 'The time must use available quarter-hour choices.');
  assert.doesNotMatch(h.app.innerHTML, /type="datetime-local"/);
  const dateOptions = h.app.innerHTML.match(/<select\b[^>]*\bid="book-date"[^>]*>([\s\S]*?)<\/select>/)?.[1].match(/<option\b[^>]*>[^<]*<\/option>/g);
  assert.equal(dateOptions?.length, 30, 'After the midnight lead cutoff, only days with valid starts belong in the picker.');
  assert.match(dateOptions[0], /value="2030-01-02"/);
  assert.match(dateOptions.at(-1), /value="2030-01-31"/, 'The final day must remain within the rolling 30-day limit.');
  for (const option of dateOptions) {
    assert.match(option, /\b(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday|Mon|Tue|Wed|Thu|Fri|Sat|Sun)\b/);
    assert.match(option, /\bJan(?:uary)?\b/);
  }
  const timeOptions = h.app.innerHTML.match(/<select\b[^>]*\bid="book-time"[^>]*>([\s\S]*?)<\/select>/)?.[1].match(/<option\b[^>]*>[^<]*<\/option>/g);
  assert.ok(timeOptions?.length);
  for (const option of timeOptions) {
    assert.match(option, /\bvalue="\d{2}:(?:00|15|30|45)"/);
    assert.match(option, /\d{1,2}:\d{2}\s*(?:AM|PM)/i);
  }
  assert.equal(timeOptions.some(option => option.includes('value="00:00"')), false, 'The new day starts after the minimum lead boundary.');
  await h.selectBooking('date', '2030-01-03');
  await h.selectBooking('time', '00:00');
  assert.equal(h.renderedStart(), '2030-01-03T00:00');
  await h.selectBooking('date', '2030-01-02');
  assert.equal(h.renderedStart(), '2030-01-02T00:15', 'Moving back to the earliest day clamps its invalid midnight time.');
  const query = new URL(h.availabilityRequests.at(-1), 'http://localhost').searchParams;
  assert.equal(query.get('start'), '2030-01-01T16:15:00.000Z');
  assert.deepEqual(h.requests, []);
  assert.deepEqual(h.errors, []);
});

test('a late time on a future date preserves that day in the availability request', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T04:00:00Z'));
  await h.openView('book');
  await h.selectBooking('date', '2030-01-15');
  await h.selectBooking('time', '23:45');
  assert.equal(h.renderedStart(), '2030-01-15T23:45');
  const query = new URL(h.availabilityRequests.at(-1), 'http://localhost').searchParams;
  assert.equal(query.get('start'), '2030-01-15T15:45:00.000Z');
  assert.equal(query.get('slots'), '2');
  assert.doesNotMatch(bookingSeat(h), /\bdisabled\b|unavailable/i);
  assert.deepEqual(h.requests, []);
  assert.deepEqual(h.errors, []);
});

test('booking shortcuts choose valid quarter hours without placing a hold', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T04:07:00Z'));
  await h.openView('book');
  for (const [action, local, utc] of [
    ['booking-earliest', '2030-01-01T12:30', '2030-01-01T04:30:00.000Z'],
    ['booking-plus-two', '2030-01-01T14:15', '2030-01-01T06:15:00.000Z'],
    ['booking-plus-three', '2030-01-01T15:15', '2030-01-01T07:15:00.000Z']
  ]) {
    await h.click(action);
    await turn();
    assert.equal(h.renderedStart(), local);
    const query = new URL(h.availabilityRequests.at(-1), 'http://localhost').searchParams;
    assert.equal(query.get('start'), utc);
    assert.deepEqual(h.requests, [], 'A time shortcut must only refresh availability.');
  }
  assert.deepEqual(h.errors, []);
});

function assertUncheckedSeat(h, checking = false) {
  const seat = bookingSeat(h);
  assert.doesNotMatch(seat, /unavailable/i, 'An unchecked time is not an occupied station.');
  assert.match(seat, checking ? /checking availability/i : /availability not checked/i);
  assert.match(seat, /\bdisabled\b/, 'An unchecked seat cannot be selected.');
  const row = h.app.innerHTML.match(/<tr><td>NS-01<\/td>[\s\S]*?<\/tr>/)?.[0];
  assert.ok(row);
  assert.doesNotMatch(row, />Unavailable</i, 'The list must use the same neutral status.');
}

test('invalid, loading and failed checks do not mark enabled PCs unavailable', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:05:00Z'));
  await h.chooseBooking('2030-01-01T12:15:00Z', 1, false);
  assertUncheckedSeat(h);

  const resolveAvailability = h.deferAvailability();
  await h.changeBooking('2030-01-01T14:15:00Z', 1);
  assertUncheckedSeat(h, true);
  resolveAvailability(h.data.pcs);
  await turn();
  assert.doesNotMatch(bookingSeat(h), /\bdisabled\b/);

  h.failNextAvailability();
  await h.changeBooking('2030-01-01T15:45:00Z', 1);
  assertUncheckedSeat(h);
  assert.match(h.app.innerHTML, /role="alert"/, 'Failed checks must show their error.');
  assert.deepEqual(h.requests, []);
  assert.deepEqual(h.errors, []);
});

test('two and three hours ahead are valid; only a successful occupied result marks a PC unavailable', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:15:00Z'));
  for (const start of ['2030-01-01T14:15:00Z', '2030-01-01T15:15:00Z']) {
    await h.chooseBooking(start, 2, false);
    const query = new URL(h.availabilityRequests.at(-1), 'http://localhost').searchParams;
    assert.equal(query.get('start'), new Date(start).toISOString());
    assert.equal(query.get('slots'), '2');
    assert.doesNotMatch(bookingSeat(h), /\bdisabled\b|unavailable/i, 'Available PCs must remain selectable several hours ahead.');
    const row = h.app.innerHTML.match(/<tr><td>NS-01<\/td>[\s\S]*?<\/tr>/)?.[0];
    assert.match(row, />Available</);
  }
  const resolveAvailability = h.deferAvailability();
  await h.changeBooking('2030-01-01T15:45:00Z', 2);
  resolveAvailability([]);
  await turn();
  assert.match(bookingSeat(h), /unavailable/i, 'A completed availability check may report an occupied PC.');
  assert.match(bookingSeat(h), /\bdisabled\b/);
  assert.deepEqual(h.errors, []);
});

test('entering booking refreshes an expired default to the earliest valid quarter hour', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:00:00Z'));
  h.advance(20*60000);
  await h.openView('book');
  const input = h.app.innerHTML.match(/<input\b(?=[^>]*\bid="book-start")[^>]*>/)?.[0];
  assert.ok(input);
  assert.match(input, /\bvalue="2030-01-01T20:45"/, 'The stale 20:15 default must advance to 20:45 Philippine time.');
  const query = new URL(h.availabilityRequests.at(-1), 'http://localhost').searchParams;
  assert.equal(query.get('start'), '2030-01-01T12:45:00.000Z');
  assert.doesNotMatch(bookingSeat(h), /\bdisabled\b|unavailable/i);
  assert.deepEqual(h.errors, []);
});

test('placing a hold reviews details and only POSTs once after explicit approval', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:00:00Z'));
  await h.chooseBooking('2030-01-01T12:45:00Z');
  let pending = h.click('hold');
  await turn();
  assert.equal(h.requests.length, 0, 'The booking must not POST before hold confirmation.');
  assert.equal(h.dialog.open, true);
  assert.equal(h.fields.get('#confirmation-amount').textContent, '₱61.10');
  assert.equal(h.fields.get('#confirmation-total').hidden, false);
  const details = h.fields.get('#confirmation-details').innerHTML;
  assert.ok(details.includes('NS-01') && details.includes('Standard'));
  assert.match(details, /8:45\s*PM/i, 'The reviewed start must be shown in Philippine time.');
  assert.match(details, /9:45\s*PM/i, 'The reviewed end must match the one-hour duration.');
  assert.ok(details.includes('60 minutes') && details.includes('₱30.55'));
  await h.click('hold');
  assert.equal(h.dialog.opens, 1, 'Duplicate clicks must preserve one pending confirmation.');
  assert.equal(h.requests.length, 0);
  h.dialog.close('cancel');
  await pending;
  assert.equal(h.requests.length, 0, 'Cancel must not reserve the seat.');

  pending = h.click('hold');
  await turn();
  h.dialog.close('confirm');
  h.dialog.close('confirm');
  await pending;
  assert.deepEqual(h.requests, [{url:'/api/bookings', body:{pc_id:1, start_at:'2030-01-01T12:45:00.000Z', slots:2, key:'test-key'}}]);

  pending = h.click('hold');
  await turn();
  assert.equal(h.dialog.open, true);
  h.dialog.escape();
  await pending;
  assert.equal(h.requests.length, 1, 'Escape must not reuse earlier hold approval.');
  assert.equal(h.nativePrompts, 0);
  assert.deepEqual(h.errors, []);
});

test('hold approval cannot POST after an in-flight refresh changes identity', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:00:00Z'));
  await h.chooseBooking('2030-01-01T12:45:00Z');
  const resolveState = h.deferState();
  const refreshing = h.click('refresh');
  const pending = h.click('hold');
  await turn();
  assert.equal(h.dialog.open, true);
  resolveState({...h.data, user:{...h.data.user, id:99}});
  await refreshing;
  h.dialog.close('confirm');
  await pending;
  assert.equal(h.requests.length, 0);
  assert.deepEqual(h.errors, []);
});

test('hold approval rechecks the minimum lead time after the dialog is opened', async () => {
  const h = await harness('customer', Date.parse('2030-01-01T12:00:00Z'));
  await h.chooseBooking('2030-01-01T12:15:00Z', 1);
  const pending = h.click('hold');
  await turn();
  assert.equal(h.dialog.open, true, 'Exactly 15 minutes ahead is valid when opening.');
  assert.equal(h.requests.length, 0);
  h.advance(1);
  h.dialog.close('confirm');
  await pending;
  assert.equal(h.requests.length, 0, 'A start now less than 15 minutes ahead must not POST.');
  assert.deepEqual(h.errors, []);
});

test('payment recording waits for explicit approval and preserves reviewed details safely', async () => {
  const h = await harness();
  const submit = () => h.submit('payment', 11, {method:'external', reference:'  REF-<verified>  '});
  let pending = submit();
  await turn();
  assert.equal(h.requests.length, 0, 'Opening the dialog must not record money.');
  assert.equal(h.dialog.open, true);
  assert.equal(h.fields.get('#confirmation-amount').textContent, '₱30.55');
  const details = h.fields.get('#confirmation-details').innerHTML;
  assert.ok(details.includes('NS-01') && details.includes('NS-TEST'), 'The reviewed station and booking must be displayed.');
  assert.ok(details.includes('REF-&lt;verified&gt;'), 'User-provided references must be HTML escaped.');
  assert.equal(details.includes('REF-<verified>'), false);
  await submit();
  assert.equal(h.dialog.opens, 1, 'A duplicate submission must not open another dialog.');
  h.dialog.close('cancel');
  await pending;
  assert.equal(h.requests.length, 0, 'Cancel must leave the payment unrecorded.');

  pending = submit();
  await turn();
  h.dialog.close('confirm');
  h.dialog.close('confirm');
  await pending;
  assert.deepEqual(h.requests, [{url:'/api/payments', body:{item_id:11, method:'external', reference:'REF-<verified>', key:'test-key'}}]);

  pending = submit();
  await turn();
  h.dialog.escape();
  await pending;
  assert.equal(h.requests.length, 1, 'Escape must not reuse an earlier approval.');
  assert.equal(h.nativePrompts, 0);
  assert.deepEqual(h.errors, []);
});

test('payment approval is discarded when an in-flight refresh changes identity', async () => {
  const h = await harness();
  const resolveState = h.deferState();
  const refreshing = h.click('refresh');
  const pending = h.submit('payment', 11, {method:'cash', reference:'TILL-11'});
  await turn();
  assert.equal(h.dialog.open, true);
  assert.equal(h.requests.length, 0);
  resolveState({...h.data, user:{...h.data.user, id:99}});
  await refreshing;
  h.dialog.close('confirm');
  await pending;
  assert.equal(h.requests.length, 0);
  assert.deepEqual(h.errors, []);
});

test('reusing the dialog for an unpaid booking clears and hides a prior refund total', async () => {
  const h = await harness('admin');
  let pending = h.click('refund-approve', 41);
  await turn();
  assert.equal(h.fields.get('#confirmation-total').hidden, false);
  assert.equal(h.fields.get('#confirmation-amount').textContent, '₱30.55');
  h.dialog.close('cancel');
  await pending;
  pending = h.click('cancel', 7);
  await turn();
  assert.equal(h.dialog.open, true);
  assert.equal(h.fields.get('#confirmation-total').hidden, true);
  assert.equal(h.fields.get('#confirmation-amount').textContent, '');
  assert.ok(h.fields.get('#confirmation-details').innerHTML.includes('NS-TEST'));
  h.dialog.close('cancel');
  await pending;
  assert.equal(h.requests.length, 0);
  assert.deepEqual(h.errors, []);
});

const cases = [
  {name:'booking cancellation', role:'customer', open:h=>h.click('cancel', 7), url:'/api/bookings/cancel', body:{booking_id:7}},
  {name:'refund approval', role:'admin', open:h=>h.click('refund-approve', 41), url:'/api/refunds/action', body:{refund_id:41, action:'approve'}, amount:'₱30.55'},
  {name:'refund rejection', role:'admin', open:h=>h.click('refund-reject', 41), url:'/api/refunds/action', body:{refund_id:41, action:'reject'}, amount:'₱30.55'},
  {name:'refund completion', role:'cashier', open:h=>h.click('refund-complete', 42), url:'/api/refunds/action', body:{refund_id:42, action:'complete'}, amount:'₱20.25'},
  {name:'refund request', role:'customer', open:h=>h.submit('refund', 32, {reason:'  Screen <fault>  '}), url:'/api/refunds', body:{payment_id:32, reason:'Screen <fault>'}, amount:'₱45.75'}
];

for (const scenario of cases) {
  test(`${scenario.name} requires approval; Cancel, Escape and duplicate attempts cannot POST`, async () => {
    const h = await harness(scenario.role);
    let pending = scenario.open(h);
    await turn();
    assert.equal(h.requests.length, 0, 'No action may POST before approval.');
    assert.equal(h.dialog.open, true, 'The action must open the NO SLEEP confirmation dialog.');
    assert.equal(h.nativePrompts, 0, 'The action must not use a browser confirmation prompt.');
    if (scenario.amount) assert.equal(h.fields.get('#confirmation-amount').textContent, scenario.amount);
    if (scenario.name === 'refund request') {
      assert.ok(h.fields.get('#confirmation-details').innerHTML.includes('Screen &lt;fault&gt;'));
    }
    await scenario.open(h);
    assert.equal(h.dialog.opens, 1, 'Repeated actions must preserve the pending dialog.');
    assert.equal(h.requests.length, 0);
    h.dialog.close('cancel');
    await pending;
    assert.equal(h.requests.length, 0, 'Cancel cannot POST.');

    pending = scenario.open(h);
    await turn();
    assert.equal(h.dialog.open, true);
    h.dialog.close('confirm');
    h.dialog.close('confirm');
    await pending;
    assert.deepEqual(h.requests, [{url:scenario.url, body:scenario.body}], 'Approval must POST the correct payload exactly once.');

    pending = scenario.open(h);
    await turn();
    assert.equal(h.dialog.open, true);
    h.dialog.escape();
    await pending;
    assert.equal(h.requests.length, 1, 'Escape after reopening cannot reuse prior approval.');
    assert.deepEqual(h.errors, []);
  });

  test(`${scenario.name} cannot POST if identity changes while awaiting approval`, async () => {
    const h = await harness(scenario.role);
    const resolveState = h.deferState();
    const refreshing = h.click('refresh'); // Request already in flight when the dialog opens.
    const pending = scenario.open(h);
    await turn();
    assert.equal(h.requests.length, 0);
    assert.equal(h.dialog.open, true);
    resolveState({...h.data, user:{...h.data.user, id:99, name:'Different account'}});
    await refreshing;
    h.dialog.close('confirm');
    await pending;
    assert.equal(h.requests.length, 0, 'An approval by a different identity must be discarded.');
    assert.deepEqual(h.errors, []);
  });
}
