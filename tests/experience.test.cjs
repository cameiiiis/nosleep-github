const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const turn = () => new Promise(setImmediate);

test('night discount preview and hold confirmation charge only eligible Philippine minutes', async () => {
  const h=await harness('customer',Date.parse('2026-10-06T15:00:00Z'));
  h.data.tiers[0].price_pesos='30.00';
  for(const [start,slots,total,saving] of [
    ['2026-10-07T01:00:00+08:00',2,'₱48.00','₱12.00'],
    ['2026-10-07T00:30:00+08:00',2,'₱54.00','₱6.00'],
    ['2026-10-07T05:45:00+08:00',2,'₱57.00','₱3.00'],
    ['2026-10-07T06:00:00+08:00',2,'₱60.00',null]
  ]){
    await h.chooseBooking(start,slots);
    assert.ok(h.app.innerHTML.includes(`<strong>${total}</strong>`),`Preview total should be ${total}`);
    if(saving)assert.ok(h.app.innerHTML.includes(`−${saving}`));
    const submission=h.click('hold');await turn();
    assert.equal(h.fields.get('#confirmation-amount').textContent,total);
    h.dialog.close('cancel');await submission;
  }
  h.data.tiers[0].price_pesos='30.55';
  await h.chooseBooking('2026-10-07T00:45:00+08:00',1);
  assert.match(h.app.innerHTML,/<strong>₱27\.50<\/strong>/);
  assert.match(h.app.innerHTML,/20% off/);
  for(const [rate,total] of [['45.00','₱72.00'],['60.00','₱96.00']]){
    h.data.tiers[0].price_pesos=rate;
    await h.chooseBooking('2026-10-07T02:00:00+08:00',2);
    assert.ok(h.app.innerHTML.includes(`<strong>${total}</strong>`));
  }
  assert.equal(h.requests.length,0);
});

for (const [refundStatus, label] of [
  ['pending', 'Refund pending review'],
  ['approved', 'Refund approved · awaiting return'],
  ['rejected', 'Refund rejected'],
  ['completed', 'Refunded']
]) {
  test(`player history shows ${refundStatus} refund with its amount`, async () => {
    const h = await harness('customer', Date.parse('2026-10-07T12:00:00Z'));
    h.data.bookings.find(b => b.id === 8).status = 'cancelled';
    h.data.refunds = [{id:41, booking_id:8, payment_id:31, amount_pesos:'30.55', status:refundStatus,
      reason:'Station issue', completed_at:refundStatus === 'completed' ? '2026-10-07T12:00:00Z' : null}];
    await h.openView('history');
    const row = h.app.innerHTML.match(/<article class="item-row">[\s\S]*?<\/article>/)?.[0];
    assert.match(row, /NS-PAST/);
    assert.match(row, /cancelled/);
    assert.ok(row.includes(label), `Expected visible refund state: ${label}`);
    assert.match(row, /₱30\.55/);
    if (refundStatus === 'completed') assert.match(row, /Returned Oct 7, 2026, 8:00 PM/);
    else assert.doesNotMatch(row, />Refunded<|Returned /);
  });
}

test('history lists each payment refund without attaching another booking refund', async () => {
  const h = await harness('customer', Date.parse('2026-10-07T12:00:00Z'));
  h.data.refunds[0].status = 'completed';
  h.data.refunds[0].completed_at = '2026-10-07T12:00:00Z';
  h.data.refunds.push({id:43, booking_id:7, payment_id:99, amount_pesos:'999.99', status:'completed'});
  await h.openView('history');
  assert.match(h.app.innerHTML, />Refunded</);
  assert.match(h.app.innerHTML, /₱30\.55/);
  assert.match(h.app.innerHTML, /awaiting return/);
  assert.match(h.app.innerHTML, /₱20\.25/);
  assert.doesNotMatch(h.app.innerHTML, /₱999\.99/);
  h.data.refunds = [];
  await h.openView('history');
  assert.doesNotMatch(h.app.innerHTML, /booking-refunds|>Refunded</);
});

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
  closest(){return null;}
  setSelectionRange(start,end,direction){this.selectionStart=start;this.selectionEnd=end;this.selectionDirection=direction;}
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
  const focusControls=new Map();
  app.querySelector = selector => focusControls.get(selector.slice(1))|| (selector === '#booking-filters' ? filters : selector === '#book-start' ? startInput : null);
  app.contains=node=>node?.ownedBy===app;
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
  const requests = [], availabilityRequests = [], errors = [], reportRequests=[],holdNodes=[];
  let nextState, nextAvailability, nextReference, nativePrompts = 0;
  const context = {
    Date:HarnessDate,
    document: {
      querySelector: selector => selector === '#app' ? app : selector === '#toast' ? toast : selector === '#confirmation-dialog' ? dialog : null,
      querySelectorAll: selector => selector==='[data-hold-timer]'?holdNodes:[], addEventListener(){}, hidden:false
    },
    window:{addEventListener(){}, scrollTo(){}},
    localStorage:{getItem(){return null;}},
    crypto:{randomUUID:()=>'test-key'},
    FormData: class { constructor(form){this.values=form.values;} get(name){return this.values[name];} },
    setTimeout:()=>0, clearTimeout(){}, setInterval:()=>0,
    confirm(){nativePrompts++; return false;},
    fetch: async (url, options) => {
      if (options.method === 'POST') requests.push({url, body:JSON.parse(options.body)});
      if(url==='/api/walk-ins/start-now'&&data.startNowError)return {ok:false,status:409,json:async()=>({error:'The rate changed. Review the updated amount.'})};
      if(url.startsWith('/api/reports')){reportRequests.push(url);if(data.reportError)return {ok:false,status:503,json:async()=>({error:'Report unavailable. Try again.'})};return {ok:true,json:async()=>({from:'2026-10-07',to:'2026-10-07',report:{payments_pesos:61.1,refunds_pesos:20.25,net_pesos:40.85},payments:data.payments,refunds:data.refunds.filter(r=>r.status==='completed')})};}
      if(url.startsWith('/api/payment-reference?')){
        if(nextReference){const response=nextReference;nextReference=null;return response;}
        if(data.referenceError)return {ok:false,status:503,json:async()=>({error:'Reference check unavailable'})};
        return {ok:true,json:async()=>({count:data.duplicateCount||0,payments:[]})};
      }
      if (url.startsWith('/api/availability?')||url.startsWith('/api/walk-ins/availability?')) {
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
      return {ok:true, json:async()=>url === '/api/me' ? {user, csrf:'test-csrf'} : url === '/api/state' ? data : url.includes('/availability?') ? {pcs:data.available||data.pcs,statuses:data.statuses||[]} : {}};
    }
  };
  context.document.body=new Node();
  let renderedHTML='';
  Object.defineProperty(app,'innerHTML',{configurable:true,get:()=>renderedHTML,set:value=>{
    if(app.contains(context.document.activeElement))context.document.activeElement=context.document.body;
    renderedHTML=value;focusControls.clear();
    for(const match of value.matchAll(/<(input|select|textarea)\b([^>]*)>/g)){
      const id=/\bid="([^"]+)"/.exec(match[2])?.[1];if(!id)continue;
      const node=new Node();node.id=id;node.name=/\bname="([^"]+)"/.exec(match[2])?.[1]||'';node.type=match[1]==='input'?/\btype="([^"]+)"/.exec(match[2])?.[1]||'text':match[1];node.value=/\bvalue="([^"]*)"/.exec(match[2])?.[1]||'';node.options=[];node.ownerDocument=context.document;node.ownedBy=app;
      if(match[1]==='select'){
        const html=value.slice(match.index,value.indexOf('</select>',match.index));node.options=Array.from(html.matchAll(/<option\b([^>]*)>/g)).map(option=>({value:/\bvalue="([^"]*)"/.exec(option[1])?.[1]||'',selected:/\bselected\b/.test(option[1])}));node.value=(node.options.find(option=>option.selected)||node.options[0])?.value||'';
      }
      node.closest=selector=>id.startsWith('book-')&&['form','#booking-filters'].includes(selector)?filters:null;
      focusControls.set(id,node);
    }
    if(Array.isArray(filters.elements)){const controls=Array.from(focusControls.values()).filter(node=>node.id.startsWith('book-'));controls.namedItem=name=>controls.find(node=>node.name===name);for(const node of controls)controls[node.name]=node;filters.elements=controls;}
  }});
  let source=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8').replace('  boot();','  globalThis.testing={state,booking,reportFilter,render,updateTimers,bookView,cashierView,detailView,syncState,refreshAvailability};boot();');
  vm.runInNewContext(source, context);
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
    app, dialog, fields, data, requests, availabilityRequests, reportRequests, holdNodes, errors, openView, changeBooking, renderedStart, document:context.document, testing:context.testing, invoke, focusControls, filters,
    advance(milliseconds){currentTime += milliseconds;},
    get nativePrompts(){return nativePrompts;},
    nextSlot(next){const button={dataset:{action:'next-start',nextStart:next},closest(selector){return selector==='button'?this:null;}};return invoke('click',button);},
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
      const target = focusControls.get('book-'+name)||filters.elements[name];
      target.id = 'book-' + name;target.name=name;filters.elements[name]=target;
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
    },
    deferReference(){
      let resolve;nextReference=new Promise(done=>resolve=done);return resolve;
    }
  };
}

const fixed=new Date('2026-10-06T16:30:00Z').getTime();
test('seat cards show PHP rate and explain maintenance with a separate next-start action',async()=>{
 const h=await harness('customer',fixed);h.data.available=[];h.data.statuses=[{pc_id:1,status:'maintenance',next_start:'2026-10-06T18:30:00Z'}];
 await h.chooseBooking(new Date(fixed+3600000).toISOString(),2,false);
 assert.match(h.app.innerHTML,/class="seat-rate"[^>]*>₱30.55 [/] 30 min/);
 assert.match(h.app.innerHTML,/Under maintenance/);assert.match(h.app.innerHTML,/data-action="next-start"/);
});
test('a hold countdown shows remaining payment time and expires without enabling payment',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='payments';await h.openView('cashier');
 assert.match(h.app.innerHTML,/Pay within/);assert.match(h.app.innerHTML,/data-hold-timer=/);
 h.advance(600001);h.testing.render();assert.match(h.app.innerHTML,/Hold expired|No payments waiting/);assert.doesNotMatch(h.app.innerHTML,/data-form="payment"/);
});
test('Today includes future Philippine arrivals and overnight active sessions',async()=>{
 const h=await harness('cashier',fixed);h.data.bookings.push({id:9,pc_id:1,reference:'ARRIVE-TODAY',user_id:1,status:'confirmed',start_at:new Date(fixed+4*3600000).toISOString(),end_at:new Date(fixed+5*3600000).toISOString()},{id:10,pc_id:1,reference:'OVERNIGHT',user_id:1,status:'active',start_at:new Date(fixed-7200000).toISOString(),end_at:new Date(fixed+600000).toISOString()});h.data.plays=[{booking_id:10}];
 await h.openView('cashier');assert.match(h.app.innerHTML,/Today/);assert.match(h.app.innerHTML,/ARRIVE-TODAY/);assert.match(h.app.innerHTML,/OVERNIGHT/);assert.match(h.app.innerHTML,/Ending soon/);
});
test('walk-in confirmation escapes guest names and Cancel makes no booking request',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';await h.openView('cashier');await h.changeBooking(new Date(fixed+3600000).toISOString(),2);h.testing.booking.pcId=1;
 const submission=h.submit('walkin',0,{guest_name:'Guest <friend>'});await turn();assert.equal(h.dialog.open,true);assert.match(h.fields.get('#confirmation-details').innerHTML,/Guest &lt;friend&gt;/);
 h.dialog.close('cancel');await submission;assert.equal(h.requests.length,0);
});
test('walk-in approval sends a frozen quote and guest name to the server',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';await h.openView('cashier');await h.changeBooking(new Date(fixed+3600000).toISOString(),2);h.testing.booking.pcId=1;
 const submission=h.submit('walkin',0,{guest_name:'  Visitor  '});await turn();assert.equal(h.dialog.open,true);h.testing.booking.pcId=null;h.dialog.close('confirm');await submission;
 assert.equal(h.requests.length,1);assert.equal(h.requests[0].url,'/api/walk-ins');assert.equal(h.requests[0].body.guest_name,'Visitor');assert.equal(h.requests[0].body.pc_id,1);assert.equal(h.requests[0].body.slots,2);assert.equal(h.requests[0].body.key,'test-key');assert.equal(h.errors.length,0);
});
test('reports request explicit Philippine dates and include completed refunds',async()=>{
 const h=await harness('cashier',fixed);h.data.refunds[1].status='completed';h.data.refunds[1].completed_at=new Date(fixed).toISOString();
 h.testing.state.cashierTab='reports';await h.openView('cashier');await turn();assert.ok(h.reportRequests.includes('/api/reports?from=2026-10-07&to=2026-10-07'));assert.match(h.app.innerHTML,/Completed refunds/);assert.match(h.app.innerHTML,/2026-10-07/);assert.match(h.app.innerHTML,/Approved return/);
});


test('choosing an alternative rechecks the full requested duration and clears selection',async()=>{
 const h=await harness('customer',fixed);await h.chooseBooking(new Date(fixed+3600000).toISOString(),3);const before=h.availabilityRequests.length;
 await h.nextSlot(new Date(fixed+3*3600000).toISOString());assert.equal(h.testing.booking.pcId,null);assert.equal(h.availabilityRequests.length,before+1);assert.match(h.availabilityRequests.at(-1),/slots=3/);assert.equal(h.testing.booking.start,'2026-10-07T03:30');assert.equal(h.requests.length,0);
});
test('live hold countdown reaches expiry using server time without changing input forms',async()=>{
 const h=await harness('cashier',fixed);const node=new Node();node.dataset={holdTimer:h.data.items[0].hold_until};h.holdNodes.push(node);
 h.testing.updateTimers();assert.equal(node.textContent,'Pay within 10:00');h.advance(590000);h.testing.updateTimers();assert.equal(node.textContent,'Pay within 00:10');h.advance(11000);h.testing.updateTimers();assert.equal(node.textContent,'Hold expired');await turn();assert.equal(h.requests.length,0);
});
test('payment approval after hold expiry cannot send a payment POST',async()=>{
 const h=await harness('cashier',fixed);const submission=h.submit('payment',11,{method:'cash',reference:''});await turn();assert.equal(h.dialog.open,true);h.advance(600001);h.dialog.close('confirm');await submission;assert.equal(h.requests.length,0);
});
test('report fetch failures show an inline recovery message and no totals',async()=>{
 const h=await harness('cashier',fixed);h.data.reportError=true;h.testing.state.cashierTab='reports';await h.openView('cashier');await turn();assert.match(h.app.innerHTML,/role="alert"[^>]*>Report unavailable. Try again./);assert.doesNotMatch(h.app.innerHTML,/class="report-totals"/);assert.equal(h.errors.length,0);
});
test('Today retains a confirmed arrival whose reserved interval crossed midnight',async()=>{
 const h=await harness('cashier',fixed);h.data.bookings.push({id:12,pc_id:1,user_id:1,reference:'LATE-ARRIVAL',status:'confirmed',start_at:new Date(fixed-3600000).toISOString(),end_at:new Date(fixed+3600000).toISOString()});await h.openView('cashier');assert.match(h.app.innerHTML,/LATE-ARRIVAL/);
});

test('Today updates ending-soon membership and check-in actions as time crosses their boundaries',async()=>{
 const h=await harness('cashier',fixed);
 h.data.bookings.push({id:20,pc_id:1,user_id:1,reference:'LIVE-TICK',status:'confirmed',start_at:new Date(fixed-600000).toISOString(),end_at:new Date(fixed+16*60000).toISOString()},{id:21,pc_id:1,user_id:1,reference:'ARRIVAL-TICK',status:'confirmed',start_at:new Date(fixed+60000).toISOString(),end_at:new Date(fixed+31*60000).toISOString()});
 h.data.plays=[{booking_id:20}];await h.openView('cashier');
 assert.match(h.app.innerHTML,/Ending soon · 0/);assert.doesNotMatch(h.app.innerHTML,/data-action="start" data-id="21"/);
 h.advance(60000);h.testing.updateTimers();
 assert.match(h.app.innerHTML,/Ending soon · 1/);assert.match(h.app.innerHTML,/data-action="start" data-id="21"/);
 h.advance(30*60000);h.testing.updateTimers();assert.doesNotMatch(h.app.innerHTML,/data-action="start" data-id="21"/);
 assert.equal(h.errors.length,0);assert.equal(h.requests.length,0);
});

test('Today waits through modal, active form, busy, and hidden guards before rolling to the next Philippine day',async()=>{
 const start=new Date('2026-10-06T15:59:00Z').getTime(),h=await harness('cashier',start);
 h.data.bookings.push({id:22,pc_id:1,user_id:1,reference:'NEXT-DAY-TICK',status:'confirmed',start_at:new Date(start+3600000).toISOString(),end_at:new Date(start+5400000).toISOString()});
 await h.openView('cashier');assert.doesNotMatch(h.app.innerHTML,/NEXT-DAY-TICK/);const original=h.app.innerHTML;
 h.dialog.showModal();h.advance(60000);h.testing.updateTimers();assert.equal(h.app.innerHTML,original);
 h.dialog.close('cancel');h.document.activeElement={closest:selector=>selector==='form'?{}:null};h.testing.updateTimers();assert.equal(h.app.innerHTML,original);
 h.document.activeElement=null;h.testing.state.busy=true;h.testing.updateTimers();assert.equal(h.app.innerHTML,original);
 h.testing.state.busy=false;h.document.hidden=true;h.testing.updateTimers();assert.equal(h.app.innerHTML,original);
 h.document.hidden=false;h.testing.updateTimers();assert.match(h.app.innerHTML,/NEXT-DAY-TICK/);
 h.advance(10*60000);h.testing.updateTimers();assert.match(h.app.innerHTML,/Pending payments · 0/);
});

test('manual refresh rechecks seats, preserves walk-in draft and clears an occupied selection',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';await h.openView('cashier');await h.changeBooking('2026-10-06T18:30:00Z',3);h.testing.booking.pcId=1;h.testing.booking.guestName='Visitor';const start=h.testing.booking.start;
 const count=h.availabilityRequests.length;h.data.available=[];await h.click('refresh');assert.equal(h.availabilityRequests.length,count+1);assert.equal(h.testing.booking.pcId,null);assert.equal(h.testing.booking.start,start);assert.equal(h.testing.booking.guestName,'Visitor');
});
test('idle availability refresh preserves selection, and a focused form or modal blocks polling',async()=>{
 const h=await harness('customer',fixed);await h.chooseBooking('2026-10-06T18:30:00Z',2);const count=h.availabilityRequests.length;
 h.document.activeElement={closest:()=>({})};await h.testing.syncState();assert.equal(h.availabilityRequests.length,count);
 h.document.activeElement=null;h.dialog.showModal();await h.testing.syncState();assert.equal(h.availabilityRequests.length,count);h.dialog.close('cancel');await h.testing.syncState();assert.equal(h.availabilityRequests.length,count+1);assert.equal(h.testing.booking.pcId,1);
});
test('cashier resolves customers without admin user profiles and can cancel before the start',async()=>{
 const h=await harness('cashier',fixed);delete h.data.users;h.data.customers=[{id:1,name:'Booked customer'}];await h.openView('cashier');assert.match(h.app.innerHTML,/Booked customer/);
 h.testing.state.detailId=7;await h.openView('detail');assert.match(h.app.innerHTML,/data-action="cancel"/);assert.match(h.app.innerHTML,/data-view="cashier"[^>]*>.*Cashier/);
});
test('Check-in updates at start and end time while form guards preserve inputs',async()=>{
 const h=await harness('cashier',fixed);h.data.bookings[0].status='confirmed';h.data.bookings[0].start_at=new Date(fixed+60000).toISOString();h.data.bookings[0].end_at=new Date(fixed+120000).toISOString();h.testing.state.cashierTab='checkin';await h.openView('cashier');assert.doesNotMatch(h.app.innerHTML,/data-action="start"/);
 h.advance(60000);h.testing.updateTimers();assert.match(h.app.innerHTML,/data-action="start"/);h.advance(60000);h.testing.updateTimers();assert.doesNotMatch(h.app.innerHTML,/data-action="start"/);
});
test('reports download applied dates and disable CSV when range changes',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='reports';await h.openView('cashier');await turn();assert.match(h.app.innerHTML,/href="\/api\/reports.csv\?from=2026-10-07&(?:amp;)?to=2026-10-07"/);
 h.testing.reportFilter.from='2026-10-01';h.testing.render();assert.doesNotMatch(h.app.innerHTML,/href="\/api\/reports.csv/);assert.match(h.app.innerHTML,/Download CSV/);
});
test('admin backup download and separate maintenance explanations are available',async()=>{
 const h=await harness('admin',fixed);await h.openView('admin');assert.match(h.app.innerHTML,/href="\/api\/admin\/backup"/);h.testing.state.adminTab='maintenance';h.testing.render();assert.match(h.app.innerHTML,/Private staff notes/);assert.match(h.app.innerHTML,/name="public_reason"/);
 await h.submit('maintenance',0,{pc_id:'1',reason:'Private repair',public_reason:'Station unavailable',start_at:'2026-10-07T03:00',end_at:'2026-10-07T04:00'});assert.equal(h.requests[0].body.public_reason,'Station unavailable');
});
test('start now checks a full session then requires verified-money approval before one atomic POST',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';h.testing.booking.walkinMode='now';await h.openView('cashier');assert.match(h.availabilityRequests.at(-1),/^\/api\/walk-ins\/availability\?slots=2$/);h.testing.booking.pcId=1;h.testing.render();assert.match(h.app.innerHTML,/Review payment and start/);assert.doesNotMatch(h.app.innerHTML,/Your reservation is held for 10 minutes/);
 const submission=h.submit('walkin',0,{guest_name:'Visitor',method:'cash',reference:'CASH-PREVIEW'});await turn();assert.equal(h.dialog.open,true);assert.match(h.fields.get('#confirmation-description').textContent,/verified.*payment/);assert.equal(h.requests.length,0);h.dialog.close('confirm');await submission;
 assert.equal(h.requests.length,1);assert.equal(h.requests[0].url,'/api/walk-ins/start-now');assert.equal(h.requests[0].body.reference,'');assert.equal(h.requests[0].body.start_at,undefined);assert.equal(h.errors.length,0);
});
test('duplicate external payment warning is authoritative and cancel prevents payment POST',async()=>{
 const h=await harness('cashier',fixed);h.data.duplicateCount=2;const submission=h.submit('payment',11,{method:'external',reference:'DUPLICATE'});await turn();assert.equal(h.dialog.open,true);assert.match(h.fields.get('#confirmation-description').textContent,/already.*2|2.*already/);assert.match(h.fields.get('#confirmation-description').textContent,/verify|verified/i);h.dialog.close('cancel');await submission;assert.equal(h.requests.length,0);
});
test('start now duplicate reference warning can cancel without creating a session',async()=>{
 const h=await harness('cashier',fixed);h.data.duplicateCount=1;h.testing.state.cashierTab='walkin';h.testing.booking.walkinMode='now';await h.openView('cashier');h.testing.booking.pcId=1;
 const submission=h.submit('walkin',0,{guest_name:'Visitor',method:'external',reference:'DUPLICATE'});await turn();assert.equal(h.dialog.open,true);assert.match(h.fields.get('#confirmation-description').textContent,/already/);h.dialog.close('cancel');await submission;assert.equal(h.requests.length,0);
});

test('maintenance explains the selected interval publicly without exposing private notes',async()=>{
 const h=await harness('customer',fixed);h.data.available=[];h.data.statuses=[{pc_id:1,status:'maintenance'}];h.data.maintenance=[{pc_id:1,start_at:'2026-10-06T17:30:00Z',end_at:'2026-10-06T19:30:00Z',reason:'Private password <secret>',public_reason:'GPU repair <soon>'},{pc_id:1,start_at:'2026-10-06T20:30:00Z',end_at:'2026-10-06T21:30:00Z',public_reason:'Different interval'}];
 await h.chooseBooking('2026-10-06T18:30:00Z',2,false);assert.match(h.app.innerHTML,/GPU repair &lt;soon&gt;/);assert.doesNotMatch(h.app.innerHTML,/Private password|Different interval/);
});
test('manual refresh preserves external payment input and its editable required state',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='payments';await h.openView('cashier');
 const controls=[{name:'method',value:'external',type:'select-one'},{name:'reference',value:'REF-RECEIVED',type:'text',readOnly:false,required:true,placeholder:'External receipt'}];controls.namedItem=name=>controls.find(x=>x.name===name);const help=new Node();help.textContent='Enter actual transaction reference';
 const form={dataset:{form:'payment',item:'11'},elements:controls,querySelector:()=>help};h.app.querySelectorAll=selector=>selector==='form'?[form]:[];
 let html=h.app.innerHTML;Object.defineProperty(h.app,'innerHTML',{get:()=>html,set:value=>{html=value;controls[0].value='cash';controls[1].value='CASH-000004';controls[1].readOnly=true;controls[1].required=false;help.textContent='Cash reference preview';}});
 await h.click('refresh');assert.equal(controls[0].value,'external');assert.equal(controls[1].value,'REF-RECEIVED');assert.equal(controls[1].readOnly,false);assert.equal(controls[1].required,true);assert.equal(help.textContent,'Enter actual transaction reference');
});
test('reference lookup failure prevents payment and session posts',async()=>{
 const h=await harness('cashier',fixed);h.data.referenceError=true;await h.submit('payment',11,{method:'external',reference:'REF'});assert.equal(h.requests.length,0);assert.equal(h.dialog.open,false);
 h.testing.state.cashierTab='walkin';h.testing.booking.walkinMode='now';await h.openView('cashier');h.testing.booking.pcId=1;await h.submit('walkin',0,{guest_name:'Visitor',method:'external',reference:'REF'});assert.equal(h.requests.length,0);assert.equal(h.dialog.open,false);
});
test('identity changes during reference lookup discard the pending confirmation',async()=>{
 const h=await harness('cashier',fixed);const resolve=h.deferReference();const submission=h.submit('payment',11,{method:'external',reference:'REF'});await turn();assert.equal(h.testing.state.busy,true);h.testing.state.user={id:99,role:'cashier',name:'Other staff'};resolve({ok:true,json:async()=>({count:1,payments:[]})});await submission;assert.equal(h.dialog.open,false);assert.equal(h.requests.length,0);
});
test('a stale scheduled availability response cannot override Start now results',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';await h.openView('cashier');const resolve=h.deferAvailability();const scheduled=h.testing.refreshAvailability();await turn();h.data.available=[];await h.click('walkin-now');resolve(h.data.pcs);await scheduled;assert.equal(h.testing.booking.available.length,0);assert.equal(h.testing.booking.pcId,null);assert.match(h.availabilityRequests.at(-1),/walk-ins\/availability/);
});
test('Start now draft survives manual and idle refresh and approval after identity change makes no POST',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';h.testing.booking.walkinMode='now';await h.openView('cashier');Object.assign(h.testing.booking,{pcId:1,guestName:'Visitor',method:'external',reference:'REF-KEPT'});await h.click('refresh');await h.testing.syncState();assert.equal(h.testing.booking.pcId,1);assert.match(h.app.innerHTML,/value="REF-KEPT"/);assert.match(h.app.innerHTML,/value="Visitor"/);
 const submission=h.submit('walkin',0,{guest_name:'Visitor',method:'external',reference:'REF-KEPT'});await turn();assert.equal(h.dialog.open,true);h.testing.state.user={id:99,role:'cashier',name:'Other staff'};h.dialog.close('confirm');await submission;assert.equal(h.requests.length,0);
});

test('choosing a later suggestion from Start now switches to a scheduled full-session check',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';h.testing.booking.walkinMode='now';await h.openView('cashier');await h.nextSlot('2026-10-06T19:30:00Z');assert.equal(h.testing.booking.walkinMode,'scheduled');assert.match(h.availabilityRequests.at(-1),/^\/api\/availability\?start=2026-10-06T19%3A30%3A00.000Z&slots=2$/);
});
test('a missing staff booking returns to the cashier workspace',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.detailId=999;await h.openView('detail');assert.match(h.app.innerHTML,/data-view="cashier">Cashier workspace/);assert.doesNotMatch(h.app.innerHTML,/data-view="bookings"/);
});
test('expiry during duplicate lookup prevents a stale money approval dialog',async()=>{
 const h=await harness('cashier',fixed);const resolve=h.deferReference();const submission=h.submit('payment',11,{method:'external',reference:'REF'});await turn();h.advance(600001);resolve({ok:true,json:async()=>({count:1,payments:[]})});await turn();assert.equal(h.dialog.open,false);await submission;assert.equal(h.requests.length,0);
});

test('temporary availability failures preserve the selected draft while disabling checkout',async()=>{
 const h=await harness('customer',fixed);await h.chooseBooking('2026-10-06T18:30:00Z',2);h.failNextAvailability();await h.click('refresh');assert.equal(h.testing.booking.pcId,1);assert.equal(h.testing.booking.checked,false);assert.match(h.app.innerHTML,/data-action="hold" disabled/);assert.match(h.app.innerHTML,/Availability service temporarily unavailable/);
});

test('keyboard filter changes keep focus on the replacement date time duration and tier control',async()=>{
 for(const [name,value] of [['date','2026-10-08'],['time','02:30'],['slots','3'],['tier','1']]){
  const h=await harness('customer',fixed);await h.chooseBooking('2026-10-06T18:30:00Z',2,false);const original=h.focusControls.get('book-'+name);original.focus();assert.equal(h.document.activeElement,original);await h.selectBooking(name,value);
  assert.equal(h.errors.length,0);assert.equal(h.document.activeElement.id,'book-'+name);assert.notEqual(h.document.activeElement,original);assert.equal(h.document.activeElement,h.focusControls.get('book-'+name));
 }
});
test('preserved rendering restores text selection without taking focus from a modal or another view',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';await h.openView('cashier');h.testing.booking.pcId=1;h.testing.render();const original=h.focusControls.get('walkin-name');original.value='Visitor';original.setSelectionRange(1,4,'backward');original.focus();h.testing.render(true);
 const replacement=h.focusControls.get('walkin-name');assert.equal(h.document.activeElement,replacement);assert.equal(replacement.selectionStart,1);assert.equal(replacement.selectionEnd,4);assert.equal(replacement.selectionDirection,'backward');
 h.dialog.showModal();const modalInput=new Node();modalInput.ownerDocument=h.document;modalInput.focus();h.testing.render(true);assert.equal(h.document.activeElement,modalInput);h.dialog.close('cancel');replacement.focus();h.testing.state.view='dashboard';h.testing.render(true);assert.equal(h.document.activeElement,h.document.body);
});
test('Start now sends the exact reviewed amount even if rates change while confirmation is open',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';h.testing.booking.walkinMode='now';await h.openView('cashier');h.testing.booking.pcId=1;h.data.tiers[0].price_pesos='0.29';h.testing.booking.slots=3;
 const submission=h.submit('walkin',0,{guest_name:'Visitor',method:'cash',reference:''});await turn();assert.equal(h.dialog.open,true);assert.equal(h.fields.get('#confirmation-amount').textContent,'₱0.75');assert.equal(h.fields.get('#confirmation-amount-label').textContent,'Amount received');h.data.tiers[0].price_pesos='60.00';h.dialog.close('confirm');await submission;assert.equal(h.requests[0].body.expected_amount_pesos,'0.75');assert.equal(h.errors.length,0);
});

test('changing from a future early time to today keeps the valid controlled start visible',async()=>{
 const h=await harness('customer',fixed);await h.chooseBooking('2026-10-07T16:30:00Z',2,false);const controls=['start','date','time','slots','tier'].map(name=>h.focusControls.get('book-'+name));controls.namedItem=name=>controls.find(node=>node.name===name);for(const node of controls)controls[node.name]=node;h.filters.id='booking-filters';h.filters.dataset={};h.filters.elements=controls;h.filters.querySelector=()=>null;h.app.querySelectorAll=selector=>selector==='form'?[h.filters]:[];controls.date.value='2026-10-07';controls.date.focus();await h.invoke('change',controls.date);await turn();assert.equal(h.testing.booking.start,'2026-10-07T00:45');assert.equal(h.focusControls.get('book-time').value,'00:45');assert.equal(h.document.activeElement.id,'book-date');
});

test('a rejected Start now price refreshes rates and requires a fresh review',async()=>{
 const h=await harness('cashier',fixed);h.testing.state.cashierTab='walkin';h.testing.booking.walkinMode='now';await h.openView('cashier');Object.assign(h.testing.booking,{pcId:1,guestName:'Visitor',method:'external',reference:'REF-REVIEW'});h.testing.state.data={...h.data,tiers:[{...h.data.tiers[0]}]};
 const submission=h.submit('walkin',0,{guest_name:'Visitor',method:'external',reference:'REF-REVIEW'});await turn();assert.equal(h.fields.get('#confirmation-amount').textContent,'₱54.99');h.data.tiers[0].price_pesos='60.00';h.data.startNowError=true;h.dialog.close('confirm');await submission;assert.equal(h.requests.length,1);assert.equal(h.dialog.open,false);assert.match(h.app.innerHTML,/₱108.00/);assert.match(h.app.innerHTML,/value="Visitor"/);assert.match(h.app.innerHTML,/value="REF-REVIEW"/);assert.equal(h.testing.state.busy,false);assert.equal(h.errors.length,0);
});
