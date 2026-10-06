(() => {
  'use strict';
  const app = document.querySelector('#app');
  const toastNode = document.querySelector('#toast');
  const confirmationDialog = document.querySelector('#confirmation-dialog');
  const zone = 'Asia/Manila';
  const fmt = new Intl.DateTimeFormat('en-PH', {timeZone:zone,year:'numeric',month:'short',day:'numeric',hour:'numeric',minute:'2-digit',hour12:true});
  const fmtDay = new Intl.DateTimeFormat('en-PH', {timeZone:zone,weekday:'short',year:'numeric',month:'short',day:'numeric'});
  const fmtBookingDay = new Intl.DateTimeFormat('en-PH', {timeZone:zone,weekday:'long',year:'numeric',month:'long',day:'numeric'});
  const fmtBookingTime = new Intl.DateTimeFormat('en-PH', {timeZone:zone,hour:'numeric',minute:'2-digit',hour12:true});
  const safe = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const peso = value => Number(value || 0).toLocaleString('en-PH',{style:'currency',currency:'PHP',minimumFractionDigits:2,maximumFractionDigits:2});
  const when = iso => iso ? fmt.format(new Date(iso)) : '—';
  const day = iso => iso ? fmtDay.format(new Date(iso)) : '—';
  const status = value => `<span class="status ${safe(value)}">${safe(String(value || '').replaceAll('_',' '))}</span>`;
  const uid = () => (crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`);
  const entries = x => Array.isArray(x) ? x : [];
  const state = {user:null,csrf:'',data:null,view:'home',authMode:'login',adminTab:'overview',bookingTab:'upcoming',cashierTab:'today',detailId:null,quiet:localStorage.getItem('nosleep-quiet')==='1',busy:false,error:'',serverDelta:0,demo:false};
  const booking = {start:initialStart(),slots:2,tier:'all',pcId:null,available:[],checked:false,loading:false,error:'',checkoutKey:null,walkinMode:'scheduled',guestName:'',method:'cash',reference:''};
  const reportFilter={from:localStart(now()).slice(0,10),to:localStart(now()).slice(0,10),data:null,loading:false,error:'',token:0};
  let expiredHoldRefresh=false;
  const expiredHoldDeadlines=new Set();
  let toastTimer, refreshTimer, availabilityToken=0, stateRequest=0, polling=false, renderedData='', renderedToday='', renderedSurface='';
  function surfaceVersion(){return JSON.stringify([state.user?.id,state.view,state.cashierTab,state.adminTab]);}
  function nightMinutes(start,slots){
    const local=Math.floor(new Date(start).getTime()/60000)*60000+8*3600000;
    let minutes=0;
    for(let i=0;i<slots*30;i++){const hour=new Date(local+i*60000).getUTCHours();if(hour>=1&&hour<6)minutes++;}
    return minutes;
  }
  function sessionAmount(price,slots,start){
    const [whole,fraction='']=String(price).split('.'),rate=BigInt(whole)*100n+BigInt(fraction.padEnd(2,'0'));
    const minutes=BigInt(start?nightMinutes(start,slots):0),cents=rate*BigInt(slots)-(rate*minutes+74n)/150n;
    return String(cents/100n)+'.'+String(cents%100n).padStart(2,'0');
  }
  function dataVersion(){return JSON.stringify(state.data ? {...state.data,server_time:null} : null);}
  function todayTimeVersion(){
    const current=now();
    return JSON.stringify([localStart(current).slice(0,10),entries(state.data?.bookings).map(b=>[b.id,new Date(b.start_at).getTime()<=current,new Date(b.end_at).getTime()>current,new Date(b.end_at).getTime()<=current+15*60000]),entries(state.data?.items).filter(i=>i.status==='held').map(i=>[i.id,holdSeconds(i.hold_until||bookingForItem(i.id)?.hold_until)>0])]);
  }

  function initialStart(){
    return localStart(Math.ceil((now()+15*60000)/900000)*900000);
  }
  function localStart(time){return new Date(time+8*3600000).toISOString().slice(0,16);}
  function startTimes(date){
    const earliest=now()+15*60000, latest=now()+30*86400000;
    return Array.from({length:96},(_,i)=>`${String(Math.floor(i/4)).padStart(2,'0')}:${String(i%4*15).padStart(2,'0')}`).filter(time=>{
      const value=new Date(`${date}T${time}+08:00`).getTime();
      return value>=earliest&&value<=latest;
    });
  }
  function bookingPicker(){
    const date=booking.start.slice(0,10), time=booking.start.slice(11,16);
    const first=initialStart().slice(0,10), last=localStart(Math.floor((now()+30*86400000)/900000)*900000).slice(0,10);
    const days=[];
    for(let value=new Date(first+'T00:00:00Z').getTime();value<=new Date(last+'T00:00:00Z').getTime();value+=86400000)days.push(new Date(value).toISOString().slice(0,10));
    const times=startTimes(date);
    const dateLabel=value=>fmtBookingDay.format(new Date(`${value}T12:00:00+08:00`));
    const timeLabel=value=>fmtBookingTime.format(new Date(`${date}T${value}+08:00`));
    return `<div class="booking-start-fields"><input id="book-start" name="start" type="hidden" value="${safe(booking.start)}"><div><label for="book-date">Date</label><select id="book-date" name="date" aria-describedby="book-start-help">${days.includes(date)?'':`<option value="${safe(date)}" selected disabled>${date?dateLabel(date):'Choose a date'}</option>`}${days.map(value=>`<option value="${value}" ${value===date?'selected':''}>${safe(dateLabel(value))}</option>`).join('')}</select></div><div><label for="book-time">Start time</label><select id="book-time" name="time" aria-describedby="book-start-help">${times.includes(time)?'':`<option value="${safe(time)}" selected disabled>${time?timeLabel(time)+' · choose a later time':'Choose a time'}</option>`}${times.map(value=>`<option value="${value}" ${value===time?'selected':''}>${safe(timeLabel(value))}</option>`).join('')}</select></div></div><div class="booking-shortcuts" aria-label="Quick start times"><button type="button" data-action="booking-earliest">Earliest start</button><button type="button" data-action="booking-plus-two">+2 hours</button><button type="button" data-action="booking-plus-three">+3 hours</button></div><p id="book-start-help" class="fine">Philippine time · Reserve at least 15 minutes ahead, or choose a later date and time.</p>`;
  }
  function validBookingStart(start){
    return bookingStartError(start)==='';
  }
  function bookingStartError(start){
    const time=new Date(start).getTime(), current=now();
    if(!Number.isFinite(time))return 'Choose a start date and time to check availability.';
    if(time%900000!==0)return 'Choose a start ending in :00, :15, :30, or :45.';
    if(time<current+15*60000)return `The earliest start is ${when(new Date(initialStart()+'+08:00').toISOString())}. You can also reserve hours or days ahead.`;
    if(time>current+30*86400000)return 'Choose a start within the next 30 days.';
    return '';
  }
  function immediateWalkin(){return isStaff()&&state.view==='cashier'&&state.cashierTab==='walkin'&&booking.walkinMode==='now';}
  function bookingSurface(){return state.view==='book'||(state.view==='cashier'&&state.cashierTab==='walkin');}
  function idleUI(){return !state.busy&&!document.hidden&&!confirmationDialog.open&&!document.activeElement?.closest('form');}
  function startISO(){return immediateWalkin()?new Date(now()).toISOString():booking.start ? new Date(`${booking.start}+08:00`).toISOString() : '';}
  function endISO(){return new Date(new Date(startISO()).getTime()+Number(booking.slots)*1800000).toISOString();}
  function now(){return Date.now()+state.serverDelta;}
  function tier(id){return entries(state.data?.tiers).find(t=>t.id===id);}
  function pc(id){return entries(state.data?.pcs).find(p=>p.id===id);}
  function pcHardware(p){
    const values={CPU:'Not specified',RAM:'Not specified',GPU:'Not specified'}, other=[];
    for(const part of String(p?.specs||'').split(/[·|\n]/).map(value=>value.trim()).filter(Boolean)){
      const labeled=/^(CPU|RAM|GPU)\s*:\s*(.+)$/i.exec(part);
      let label=labeled?.[1].toUpperCase(), value=labeled?.[2].trim()||part;
      if(!label){
        if(/\b(?:RTX|GTX|Radeon|RX\s*\d|GeForce|Quadro|Arc\s*[AB]\d)/i.test(part))label='GPU';
        else if(/^(?:\d+(?:\.\d+)?\s*(?:GB|TB)(?:\s+DDR\d)?|\d+(?:\.\d+)?\s*(?:GB|TB)\s+RAM)$/i.test(part))label='RAM';
        else if(/\b(?:Ryzen|Intel\s+Core|Core\s+i[3579]|i[3579]-\d|Xeon|Threadripper)/i.test(part))label='CPU';
      }
      if(label&&values[label]==='Not specified')values[label]=value;
      else other.push(part);
    }
    return {values,other};
  }
  function hardwareSpecs(p,id=''){
    const {values,other}=pcHardware(p);
    return `<span class="pc-specs" ${id?`id="${safe(id)}"`:''}>${Object.entries(values).map(([label,value])=>`<span class="pc-spec-row"><b>${label}</b><span>${safe(value)}</span></span>`).join('')}${other.length?`<span class="pc-extra">${safe(other.join(' · '))}</span>`:''}</span>`;
  }
  function user(id){return entries(state.data?.users).find(u=>u.id===id) || (state.user?.id===id ? state.user : null);}
  function items(id){return entries(state.data?.items).filter(x=>x.booking_id===id);}
  function payments(id){return entries(state.data?.payments).filter(x=>x.booking_id===id);}
  function cashReferencePreview(){
    const recorded=entries(state.data?.payments), cashCount=recorded.filter(p=>p.method==='cash').length;
    const latest=recorded.reduce((number,p)=>Math.max(number,Number(/^CASH-(\d+)$/.exec(p.reference)?.[1]||0)),cashCount);
    return 'CASH-'+String(latest+1).padStart(6,'0');
  }
  function receipts(id){return entries(state.data?.receipts).filter(x=>x.booking_id===id);}
  function refunds(id){return entries(state.data?.refunds).filter(x=>x.booking_id===id);}
  function play(id){return entries(state.data?.plays).find(x=>x.booking_id===id && !x.ended_at);}
  function bookingForItem(id){const item=entries(state.data?.items).find(x=>x.id===id);return item && entries(state.data?.bookings).find(b=>b.id===item.booking_id);}
  function isStaff(){return state.user?.role==='cashier'||state.user?.role==='admin';}
  function isAdmin(){return state.user?.role==='admin';}
  function toast(message,isError=false){toastNode.textContent=message;toastNode.className=`show${isError?' error':''}`;clearTimeout(toastTimer);toastTimer=setTimeout(()=>toastNode.className='',5000);}
  function errorText(err){return err?.message || 'Something went wrong. Please try again.';}
  async function api(path,body){
    const opts={credentials:'same-origin',headers:{Accept:'application/json'}};
    if(body!==undefined){opts.method='POST';opts.headers['Content-Type']='application/json';opts.headers['X-CSRF-Token']=state.csrf;opts.body=JSON.stringify(body);}
    const response=await fetch(path,opts);
    let result;try{result=await response.json();}catch{throw new Error('The server returned an unreadable response.');}
    if(!response.ok){
      if(response.status===401 && state.user){if(confirmationDialog.open)confirmationDialog.close('cancel');stateRequest++;state.user=null;state.data=null;state.view='auth';render();}
      throw new Error(result?.error || `Request failed (${response.status}).`);
    }
    return result;
  }
  async function loadState(){
    if(!state.user){state.data=null;return;}
    const request=++stateRequest, userId=state.user.id;
    const data=await api('/api/state');
    if(request!==stateRequest || state.user?.id!==userId)return false;
    state.data=data;
    state.user=data.user;
    if(data.server_time) state.serverDelta=new Date(data.server_time).getTime()-Date.now();
    return true;
  }
  async function reload(keepView=true){
    await loadState();
    render(true);
  }
  function navItems(){
    if(!state.user) return [['home','Home'],['book','Book a PC'],['auth','Sign in']];
    if(isStaff()) return isAdmin()?[['dashboard','Dashboard'],['cashier','Cashier'],['admin','Admin']]:[['dashboard','Dashboard'],['cashier','Cashier']];
    const base=[['dashboard','Dashboard'],['book','Book a PC'],['bookings','My bookings'],['history','History']];
    return base;
  }
  function header(){
    const links=navItems().map(([v,label])=>`<button type="button" class="nav-link ${state.view===v?'active':''}" data-view="${v}" ${state.view===v?'aria-current="page"':''}>${label}</button>`).join('');
    return `<header class="site-header"><div class="header-inner"><button type="button" class="brand-button" data-view="home" aria-label="NO SLEEP home"><span class="wordmark">NO <span>SLEEP</span><i></i></span></button><nav class="header-nav" aria-label="Main navigation">${links}</nav><div class="header-user">${state.user?`<b>${safe(state.user.name)}</b><button class="btn-link" type="button" data-action="refresh">Refresh</button><button class="btn-link" type="button" data-action="logout">Log out</button>`:''}</div></div></header>`;
  }
  function footer(){return `<footer class="footer"><div class="container"><span>NO SLEEP · Your seat. Your session. Your night.</span><span>Times shown in Philippine time (UTC+8) · Payments verified by staff</span></div></footer>`;}
  async function setView(view){
    state.view=view;
    if((view==='book'||(view==='cashier'&&state.cashierTab==='walkin')) && state.user){
      if(!validBookingStart(startISO())){booking.start=initialStart();booking.pcId=null;booking.checkoutKey=null;}
      refreshAvailability();
    }
    render();window.scrollTo({top:0,behavior:'instant'});
    const main=app.querySelector('#main');if(main){main.tabIndex=-1;main.focus({preventScroll:true});}
    if((view==='cashier'&&state.cashierTab==='reports')||(view==='admin'&&state.adminTab==='reports'))loadReports();
    if(state.user && view!=='home' && view!=='auth'){
      try{await loadState();if(state.view===view)render();}catch(err){toast(errorText(err),true);}
    }
  }
  function render(preserveForms=false){
    const focused=preserveForms&&!confirmationDialog.open&&renderedSurface===surfaceVersion()&&document.activeElement&&app.contains(document.activeElement)?document.activeElement:null;
    const focusedForm=focused?.closest('form'),focus=focused?{id:focused.id,name:focused.name,formKey:focusedForm&&(focusedForm.id||JSON.stringify(focusedForm.dataset)),start:focused.selectionStart,end:focused.selectionEnd,direction:focused.selectionDirection}:null;
    const drafts=preserveForms?Array.from(app.querySelectorAll('form')).filter(form=>form.id!=='booking-filters').map(form=>({key:form.id||JSON.stringify(form.dataset),help:form.querySelector('[data-reference-help]')?.textContent,fields:Array.from(form.elements||[]).filter(el=>el.name&&el.type!=='hidden'&&el.type!=='submit').map(el=>({name:el.name,value:el.value,checked:el.checked,readOnly:el.readOnly,required:el.required,placeholder:el.placeholder}))})):[];
    let body;
    switch(state.view){
      case 'auth':body=authView();break;
      case 'dashboard':body=state.user?dashboardView():homeView();break;
      case 'book':body=state.user?bookView():guestBookView();break;
      case 'bookings':body=state.user?bookingsView(false):authView();break;
      case 'history':body=state.user?bookingsView(true):authView();break;
      case 'detail':body=state.user?detailView()+refundRequestView():authView();break;
      case 'cashier':body=isStaff()?cashierView():forbiddenView();break;
      case 'admin':body=isAdmin()?adminView():forbiddenView();break;
      default:body=homeView();
    }
    app.innerHTML=header()+`<main id="main">${body}</main>`+footer();
    drafts.forEach(draft=>{const form=Array.from(app.querySelectorAll('form')).find(form=>(form.id||JSON.stringify(form.dataset))===draft.key);draft.fields.forEach(field=>{const el=form?.elements.namedItem(field.name);if(el){el.value=field.value;if(el.type==='checkbox'||el.type==='radio')el.checked=field.checked;['readOnly','required','placeholder'].forEach(key=>{if(field[key]!==undefined)el[key]=field[key];});}});const help=form?.querySelector('[data-reference-help]');if(help&&draft.help!==undefined)help.textContent=draft.help;});
    if(focus&&!confirmationDialog.open){
      const form=focus.formKey&&Array.from(app.querySelectorAll('form')).find(form=>(form.id||JSON.stringify(form.dataset))===focus.formKey),replacement=focus.id?app.querySelector('#'+focus.id):form?.elements.namedItem(focus.name);
      if(replacement&&!replacement.disabled){replacement.focus({preventScroll:true});if(Number.isInteger(focus.start)&&Number.isInteger(focus.end)&&replacement.setSelectionRange){try{replacement.setSelectionRange(focus.start,focus.end,focus.direction);}catch{/* Date and numeric inputs do not support text selection. */}}}
    }
    app.classList.toggle('home-shell', !!app.querySelector('.hero'));
    app.setAttribute('aria-busy','false');
    renderedData=dataVersion();
    renderedToday=todayTimeVersion();
    renderedSurface=surfaceVersion();
    updateTimers();
  }
  function homeView(){
    const destination=isStaff()?'cashier':state.user?'book':'auth';
    const cta=isStaff()?'Open cashier workspace':state.user?'Book a PC':'Get started';
    return `<section class="hero"><div class="container hero-content"><h1>Make room for <em>the night.</em></h1><p>Your seat, your session, your night. Choose a PC, reserve your time, and settle with the cashier before you play.</p><div class="hero-actions"><button type="button" class="btn btn-primary" data-view="${destination}">${cta}<span class="icon-arrow" aria-hidden="true"></span></button><button type="button" class="btn btn-ghost" data-view="${destination}">See how it works</button></div></div></section><section class="subtle-band"><div class="container two-col"><div class="prose"><h2>Settle in, then switch on.</h2><p>Pick a station for a specific date and time. We hold your selected slot for 10 minutes while you arrange payment with the café cashier. Once staff record the verified payment, your booking is confirmed.</p><button type="button" class="btn-link" data-view="${destination}">Explore the workspace →</button></div><ol class="rule-list"><li><span>Choose your PC and time</span><b>01</b></li><li><span>Review your exact price</span><b>02</b></li><li><span>Pay with staff to confirm</span><b>03</b></li><li><span>Check in and play</span><b>04</b></li></ol></div></section>`;
  }
  function guestBookView(){return `<div class="container page"><div class="page-head"><div><h1>Book a PC.</h1><p>Sign in or create an account to reserve a seat.</p></div></div><button class="btn btn-primary" type="button" data-view="auth">Sign in to continue</button></div>`;}
  function forbiddenView(){return `<div class="container page"><div class="empty"><h3>Access unavailable</h3><p>Your account cannot open this workspace.</p><button class="btn btn-primary" type="button" data-view="dashboard">Go to dashboard</button></div></div>`;}
  function authView(){
    const reg=state.authMode==='register';
    return `<div class="container page auth-layout"><div class="auth-copy"><h1>Your next session starts here.</h1><p>Choose a station, reserve your time, and keep every booking and receipt in one place.</p><div class="horizon"></div><p class="site-note">Payment is confirmed by café staff after they verify the money outside this website.</p></div><div class="panel auth-panel"><div class="auth-tabs" role="tablist" aria-label="Account"><button type="button" role="tab" aria-selected="${!reg}" class="${!reg?'active':''}" data-auth-mode="login">Sign in</button><button type="button" role="tab" aria-selected="${reg}" class="${reg?'active':''}" data-auth-mode="register">Create account</button></div><h2>${reg?'Create your account':'Welcome back.'}</h2><p class="muted">${reg?'A few details, then your seat is yours to reserve.':'Sign in to see your sessions and book a PC.'}</p><form data-form="${reg?'register':'login'}" class="stack">${reg?'<div><label for="auth-name">Your name</label><input id="auth-name" name="name" autocomplete="name" required maxlength="100"></div>':''}<div><label for="auth-email">Email address</label><input id="auth-email" type="email" name="email" autocomplete="email" required></div><div><label for="auth-password">Password</label><input id="auth-password" type="password" name="password" autocomplete="${reg?'new-password':'current-password'}" required minlength="8"></div><button class="btn btn-primary" type="submit">${reg?'Create account':'Sign in'}</button></form>${state.demo?`<div class="demo-note"><strong>Local demo accounts</strong><br><code>player@nosleep.local / Player123!</code><br><code>cashier@nosleep.local / Cashier123!</code><br><code>admin@nosleep.local / Admin123!</code><br>These credentials are for the local demonstration only.</div>`:''}</div></div>`;
  }
  function dashboardView(){
    if(isStaff()){
      const pending=entries(state.data?.items).filter(i=>i.status==='held'&&holdSeconds(i.hold_until||bookingForItem(i.id)?.hold_until)>0).length;
      const active=entries(state.data?.plays).filter(p=>!p.ended_at).length;
      return `<div class="container page"><div class="page-head"><div><h1>Good evening, ${safe(state.user.name.split(' ')[0])}.</h1><p>Your café workspace is ready.</p></div></div><div class="dashboard-grid"><section class="feature-panel"><div><h2>Keep the night moving.</h2><p>${pending?`${pending} held ${pending===1?'item needs':'items need'} verified payment.`:'No held payments are waiting right now.'} ${active?`${active} ${active===1?'session is':'sessions are'} in progress.`:'No play sessions are active.'}</p></div><div><div class="horizon"></div><button class="btn btn-primary" type="button" data-view="cashier">Open cashier workspace</button></div></section><div class="quick-actions"><button type="button" data-view="cashier">Payments and check-in <span aria-hidden="true">↗</span></button>${isAdmin()?'<button type="button" data-view="admin">Inventory and reports <span aria-hidden="true">↗</span></button>':''}<button type="button" data-action="refresh">Refresh live data <span aria-hidden="true">↗</span></button></div></div></div>`;
    }
    const bookings=entries(state.data?.bookings);
    const active=bookings.find(b=>!!play(b.id));
    const next=bookings.filter(b=>['held','confirmed'].includes(b.status) && new Date(b.end_at).getTime()>now()).sort((a,b)=>new Date(a.start_at)-new Date(b.start_at))[0];
    const featured=active||next;
    const station=featured?pc(featured.pc_id):null;
    const title=active?'Your session is live.':next?'Your next seat is waiting.':'Your night starts with a seat.';
    const copy=active?`${safe(station?.code||'Your PC')} is ready. Keep an eye on the time or request more when you need it.`:next?`${safe(station?.code||'Your PC')} · ${safe(when(next.start_at))} to ${safe(when(next.end_at))}`:'Check availability and choose your time. Your price is shown before you place a hold.';
    return `<div class="container page"><div class="page-head"><div><h1>Good evening, ${safe(state.user.name.split(' ')[0])}.</h1><p>Manage your time at NO SLEEP.</p></div>${state.quiet?'<button class="btn btn-ghost btn-small" data-action="quiet-off" type="button">Exit Quiet Mode</button>':''}</div>${state.quiet&&active?quietView(active):`<div class="dashboard-grid"><section class="feature-panel"><div><h2>${title}</h2><p>${copy}</p></div><div><div class="horizon"></div><div class="actions">${featured?`<button type="button" class="btn btn-primary" data-detail="${featured.id}">View session</button>`:`<button type="button" class="btn btn-primary" data-view="book">Find a PC</button>`}${active?`<button type="button" class="btn btn-ghost" data-action="quiet-on">Quiet Mode</button>`:''}</div></div></section><div class="quick-actions"><button type="button" data-view="book">Book a PC <span aria-hidden="true">↗</span></button><button type="button" data-view="bookings">My bookings <span aria-hidden="true">↗</span></button><button type="button" data-view="history">Session history <span aria-hidden="true">↗</span></button>${isStaff()?'<button type="button" data-view="cashier">Staff workspace <span aria-hidden="true">↗</span></button>':''}</div></div>${next&&!active?`<div class="section-head"><h2>Coming up</h2></div>${bookingRow(next)}`:''}`}</div>`;
  }
  function quietView(b){
    const p=pc(b.pc_id);
    return `<section class="quiet"><span class="seat-label">Now playing at</span><h1>${safe(p?.code||'Your PC')}</h1><div class="timer" data-timer="${safe(b.end_at)}" aria-label="Time remaining">--:--:--</div><div class="progress-track" aria-hidden="true"><b data-progress="${safe(b.start_at)}|${safe(b.end_at)}"></b></div><p class="muted" style="margin:24px auto">Reserved until ${safe(when(b.end_at))} Philippine time</p><div class="actions"><button class="btn btn-primary" data-detail="${b.id}" type="button">Session details</button><button class="btn btn-ghost" data-action="quiet-off" type="button">Leave Quiet Mode</button></div><div class="panel"><h3>Need more time?</h3><p class="muted">Request an extension here. Your current end time changes only after staff verify and record the additional payment.</p>${extensionForm(b)}</div></section>`;
  }
  function bookView(walkin=false){
    const immediate=walkin&&immediateWalkin();
    const pcList=entries(state.data?.pcs).filter(p=>booking.tier==='all'||String(p.tier_id)===booking.tier);
    const selected=pc(booking.pcId);
    const selectedTier=selected&&tier(selected.tier_id);
    const availableIds=new Set(booking.available.map(p=>p.id));
    const quotedStart=startISO(),price=sessionAmount(selectedTier?.price_pesos||0,Number(booking.slots),quotedStart);
    const regular=sessionAmount(selectedTier?.price_pesos||0,Number(booking.slots));
    const discount=Number(regular)-Number(price);
    const valid=immediate||validBookingStart(startISO());
    const checked=valid&&booking.checked&&!booking.loading&&!booking.error;
    const unchecked=booking.loading?'Checking availability':'Availability not checked';
    return `<div class="container page"><div class="page-head"><div><h1>${walkin?'Book a walk-in.':'Find your seat.'}</h1><p>${immediate?'Verify received payment and start a session now. Availability covers the whole session.':'Reserve for later tonight or another day, starting at least 15 minutes from now. Availability covers your whole session.'}</p></div></div>${walkin?`<div class="filters" aria-label="Walk-in start mode"><button type="button" class="btn ${immediate?'btn-ghost':'btn-primary'}" data-action="walkin-scheduled" aria-pressed="${!immediate}">Schedule for later</button><button type="button" class="btn ${immediate?'btn-primary':'btn-ghost'}" data-action="walkin-now" aria-pressed="${immediate}">Start now</button></div>`:''}<p class="fine night-offer">20% off all PCs daily, 1 AM–6 AM Philippine time. Only session minutes within these hours receive the discount.</p><div class="booking-layout"><div><form class="map-tools" id="booking-filters"><div class="span2">${immediate?'<p class="fine">Starts now. The server sets the actual start after verified payment is confirmed.</p>':bookingPicker()}</div><div><label for="book-slots">Duration</label><select id="book-slots" name="slots">${Array.from({length:16},(_,i)=>`<option value="${i+1}" ${Number(booking.slots)===i+1?'selected':''}>${(i+1)*30} min</option>`).join('')}</select></div><div><label for="book-tier">PC tier</label><select id="book-tier" name="tier"><option value="all">All tiers</option>${entries(state.data?.tiers).map(t=>`<option value="${t.id}" ${booking.tier===String(t.id)?'selected':''}>${safe(t.name)}</option>`).join('')}</select></div></form>${valid?'':`<div class="inline-note warn" role="status">${safe(bookingStartError(startISO()))}</div>`}<div class="map-key">${checked?'<span><i></i>Available</span><span class="busy"><i></i>Unavailable for this time</span>':`<span class="off"><i></i>${unchecked}</span>`}<span class="off"><i></i>Offline</span></div>${booking.error?`<div class="inline-note error" role="alert">${safe(booking.error)}</div>`:''}${booking.loading?'<div class="inline-note" role="status">Checking these seats…</div>':''}<div class="seat-map" role="group" aria-label="PC seat map">${pcList.map(p=>{const t=tier(p.tier_id);const active=p.enabled&&checked&&availableIds.has(p.id),info=seatAvailability(p,checked,availableIds);return `<div class="seat-option"><button type="button" class="seat ${!p.enabled?'disabled':!checked?'unchecked':active?'':'unavailable'} ${booking.pcId===p.id?'selected':''}" data-seat="${p.id}" ${active?'':'disabled'} aria-pressed="${booking.pcId===p.id}" aria-describedby="seat-specs-${p.id}" aria-label="${safe(p.code)}, ${safe(t?.name||'PC')}, ${!p.enabled?'offline':!checked?unchecked:active?'available':'unavailable'}"><strong>${safe(p.code)}</strong><small>${safe(t?.name||'PC')}</small><span class="seat-rate">${peso(t?.price_pesos)} / 30 min</span>${hardwareSpecs(p,`seat-specs-${p.id}`)}<span class="seat-dot" aria-hidden="true"></span><span class="seat-state">${safe(info.label)}</span></button>${nextStartButton(p,info)}</div>`}).join('')}</div>${pcList.length?'':`<div class="empty"><h3>No PCs in this tier</h3><p>Try another tier.</p></div>`}<details class="seat-list"><summary>View seats as a list</summary><div class="table-wrap"><table><thead><tr><th>PC</th><th>Tier and specs</th><th>Rate / 30 min</th><th>Status</th></tr></thead><tbody>${pcList.map(p=>`<tr><td>${safe(p.code)}</td><td>${safe(tier(p.tier_id)?.name)}${hardwareSpecs(p)}</td><td>${peso(tier(p.tier_id)?.price_pesos)}</td><td>${safe(seatAvailability(p,checked,availableIds).label)}${nextStartButton(p,seatAvailability(p,checked,availableIds))}</td></tr>`).join('')}</tbody></table></div></details></div><aside class="booking-side"><div class="panel review-panel"><h3>Review your session</h3>${selected?`<div class="review-row"><span>Station</span><strong>${safe(selected.code)} · ${safe(selectedTier?.name)}</strong></div><div class="review-specs">${hardwareSpecs(selected)}</div><div class="review-row"><span>Starts</span><strong>${immediate?'Now · estimated until confirmed':safe(when(startISO()))}</strong></div><div class="review-row"><span>Ends</span><strong>${safe(when(endISO()))}</strong></div><div class="review-row"><span>Regular rate</span><strong>${peso(selectedTier?.price_pesos)} / 30 min</strong></div><div class="review-row"><span>Duration</span><strong>${Number(booking.slots)*30} minutes</strong></div>${discount>0?`<div class="review-row"><span>Regular total</span><strong>${peso(regular)}</strong></div><div class="review-row"><span>Night discount · 20%</span><strong>−${peso(discount)}</strong></div>`:''}<div class="review-total"><span>Total</span><strong>${peso(price)}</strong></div><p class="fine">${immediate?'Confirm only after verifying the displayed amount received. The server checks this price, sets the actual start, records payment, issues a receipt and starts play. If the rate changes, review the new price first.':'This is a preview. The server sets the final amount when you place a hold. Your reservation is held for 10 minutes; staff must verify and record payment before it expires.'}</p>${walkin?`<form data-form="walkin"><label for="walkin-name">Guest name</label><input id="walkin-name" name="guest_name" required minlength="2" maxlength="80" value="${safe(booking.guestName||'')}" autocomplete="off">${immediate?walkinPaymentFields():''}<button type="submit" class="btn btn-primary" data-walkin-submit ${state.busy||!checked?'disabled':''}>${immediate?'Review payment and start':'Review walk-in hold'}</button></form>`:`<button type="button" class="btn btn-primary" data-action="hold" ${state.busy||!checked?'disabled':''}>Place 10-minute hold</button>`}`:'<p class="muted">Choose an available PC to see your exact dates and price.</p>'}</div><div class="inline-note" style="margin-top:14px">Payment is handled with café staff. This website does not transfer money.</div></aside></div></div>`;
  }
  function customerName(b){return entries(state.data?.walk_ins).find(w=>w.booking_id===b?.id)?.guest_name||entries(state.data?.customers).find(c=>c.id===b?.user_id)?.name||user(b?.user_id)?.name||(b?'User #'+b.user_id:'—');}
  function holdSeconds(deadline){return Math.max(0,Math.ceil((new Date(deadline).getTime()-now())/1000));}
  function holdClock(deadline){const seconds=holdSeconds(deadline);return String(Math.floor(seconds/60)).padStart(2,'0')+':'+String(seconds%60).padStart(2,'0');}
  function holdNotice(deadline,staff=false){return '<div class="hold-notice" data-hold-deadline="'+safe(deadline)+'"><strong data-hold-timer="'+safe(deadline)+'">'+(holdSeconds(deadline)?'Pay within '+holdClock(deadline):'Hold expired')+'</strong><span>'+ (staff?'Verify and record the received payment before the timer ends.':'Visit the cashier to pay and confirm your booking before the timer ends.')+'</span></div>';}
  function seatAvailability(p,checked,availableIds){
    if(!p.enabled)return {label:'Offline'};if(!checked)return {label:booking.loading?'Checking availability':'Availability not checked'};
    if(availableIds.has(p.id))return {label:'Available'};
    const detail=entries(booking.statuses).find(x=>x.pc_id===p.id);
    const explanations=detail?.status==='maintenance'?entries(state.data?.maintenance).filter(m=>m.pc_id===p.id&&new Date(m.start_at)<new Date(endISO())&&new Date(m.end_at)>new Date(startISO())).map(m=>m.public_reason).filter(Boolean):[];
    return {label:detail?.status==='maintenance'?'Under maintenance'+(explanations.length?' · '+explanations.join(' · '):''):detail?.status==='reserved'?'Reserved for this time':'Unavailable for this time',next:detail?.next_start};
  }
  function nextStartButton(p,info){return info.next?'<button type="button" class="btn-link next-start" data-action="next-start" data-next-start="'+safe(info.next)+'">Next: '+safe(when(info.next))+'</button>':'';}
  function refundHistory(id){
    const list=refunds(id);
    if(!list.length)return '';
    const labels={pending:'Refund pending review',approved:'Refund approved · awaiting return',rejected:'Refund rejected',completed:'Refunded'};
    return `<ul class="booking-refunds" aria-label="Refund history">${list.map(r=>`<li><span class="status ${safe(r.status)}">${safe(labels[r.status]||'Refund '+r.status)}</span><strong>${peso(r.amount_pesos)}</strong>${r.status==='completed'&&r.completed_at?`<span class="fine">Returned ${safe(when(r.completed_at))}</span>`:''}</li>`).join('')}</ul>`;
  }
  function bookingRow(b,showRefunds=false){
    const p=pc(b.pc_id), base=items(b.id).find(i=>i.kind==='base');
    return `<article class="item-row"><div><h3>${safe(p?.code||'PC')} <span class="muted">· ${safe(b.reference)}</span></h3><p>${safe(when(b.start_at))} → ${safe(when(b.end_at))} · ${safe(tier(p?.tier_id)?.name||'')}</p>${showRefunds?refundHistory(b.id):''}</div><div class="item-meta">${status(b.status)}${b.status==='held'?holdNotice(b.hold_until):''}<span class="price">${peso(base?.amount_pesos)}</span><button class="btn btn-secondary btn-small" type="button" data-detail="${b.id}">Details</button></div></article>`;
  }
  function bookingsView(history){
    const all=entries(state.data?.bookings).slice().sort((a,b)=>new Date(b.start_at)-new Date(a.start_at));
    const list=all.filter(b=>history?['completed','cancelled','expired','no_show'].includes(b.status)||new Date(b.end_at).getTime()<now():!['completed','cancelled','expired','no_show'].includes(b.status)&&new Date(b.end_at).getTime()>=now());
    return `<div class="container page"><div class="page-head"><div><h1>${history?'Session history':'My bookings'}</h1><p>${history?'Past, cancelled and expired reservations stay here with their receipts and refund status.':'Upcoming and active sessions, with payment and check-in details.'}</p></div><button class="btn btn-primary" type="button" data-view="book">Book another PC</button></div>${list.length?`<div class="item-list">${list.map(b=>bookingRow(b,history)).join('')}</div>`:`<div class="empty"><h3>${history?'No past sessions yet':'No current bookings'}</h3><p>${history?'Your completed sessions and receipts will appear here.':'Find an available PC and choose a time to begin.'}</p><button type="button" class="btn btn-primary" data-view="book">Find a PC</button></div>`}</div>`;
  }
  function extensionForm(b){
    return `<form data-form="extend" data-booking="${b.id}" class="mini-form"><div><label for="extend-slots-${b.id}">Extra time</label><select id="extend-slots-${b.id}" name="slots">${Array.from({length:8},(_,i)=>`<option value="${i+1}">${(i+1)*30} min</option>`).join('')}</select></div><button class="btn btn-secondary" type="submit">Request extension</button></form><p class="fine">A new 10-minute payment hold is created only if the extra time is available.</p>`;
  }
  function refundRequestView(){
    const b=entries(state.data?.bookings).find(x=>x.id===state.detailId);
    if(!b||!['cancelled','no_show','completed'].includes(b.status))return '';
    const eligible=payments(b.id).filter(p=>!refunds(b.id).some(r=>r.payment_id===p.id && r.status!=='rejected'));
    if(!eligible.length)return '';
    return `<section class="container" style="padding-bottom:60px"><div class="panel"><h3>Request a refund</h3><p class="muted">Tell staff why you are requesting a refund. An admin reviews the request; staff record completion after money has actually been returned.</p>${eligible.map(p=>`<form data-form="refund" data-payment="${p.id}" class="fields" style="margin-top:18px"><div><label for="refund-reason-${p.id}">Reason for ${peso(p.amount_pesos)} payment</label><input id="refund-reason-${p.id}" name="reason" required maxlength="300" placeholder="Briefly describe the issue"></div><div style="align-self:end"><button class="btn btn-secondary" type="submit">Request refund review</button></div></form>`).join('')}</div></section>`;
  }
  function detailView(){
    const b=entries(state.data?.bookings).find(x=>x.id===state.detailId);
    if(!b)return `<div class="container page"><div class="empty"><h3>Booking unavailable</h3><p>It may have changed or been removed.</p><button class="btn btn-primary" type="button" data-view="${isStaff()?'cashier':'bookings'}">${isStaff()?'Cashier workspace':'My bookings'}</button></div></div>`;
    const p=pc(b.pc_id), t=tier(p?.tier_id), bs=items(b.id), pay=payments(b.id), rec=receipts(b.id), ref=refunds(b.id), active=play(b.id);
    const canCancel=['held','confirmed'].includes(b.status)&&new Date(b.start_at).getTime()>now()&&(b.user_id===state.user.id||isStaff());
    const held=bs.filter(i=>i.status==='held');
    return `<div class="container page"><div class="detail-top"><div><button class="btn-link" type="button" data-view="${isStaff()?'cashier':'bookings'}">← ${isStaff()?'Cashier workspace':'My bookings'}</button><h1>${safe(p?.code||'PC')} <span class="muted">session</span></h1><p class="muted">${safe(b.reference)} · ${safe(t?.name||'')}</p></div><div class="actions">${status(b.status)}${canCancel?`<button type="button" class="btn btn-danger btn-small" data-action="cancel" data-id="${b.id}">Cancel booking</button>`:''}</div></div>${held.length?held.map(i=>holdNotice(i.hold_until||b.hold_until,isStaff())).join(''):''}${held.length?`<div class="inline-note warn" role="status">Payment pending. Your held item expires ${safe(when(held[0].hold_until||b.hold_until))}. Visit the cashier to pay; staff verify actual money and record it here. This website does not transfer money.</div>`:''}${active?`<div class="panel-soft" style="margin-top:18px"><h3>Session in progress</h3><div class="timer" data-timer="${safe(b.end_at)}" aria-label="Time remaining">--:--:--</div><div class="progress-track" aria-hidden="true"><b data-progress="${safe(b.start_at)}|${safe(b.end_at)}"></b></div><p style="margin:18px 0 0">Reserved until ${safe(when(b.end_at))}</p><button class="btn-link" type="button" data-action="quiet-on">Enter Quiet Mode</button></div>`:''}<div class="detail-grid" style="margin-top:22px"><section class="panel"><h3>Reservation</h3><dl class="data-list"><div><dt>Reference</dt><dd>${safe(b.reference)}</dd></div><div><dt>Station</dt><dd>${safe(p?.code||'—')}${hardwareSpecs(p)}</dd></div><div><dt>Starts</dt><dd>${safe(when(b.start_at))}</dd></div><div><dt>Ends</dt><dd>${safe(when(b.end_at))}</dd></div><div><dt>Status</dt><dd>${safe(b.status.replaceAll('_',' '))}</dd></div>${isStaff()?`<div><dt>Player</dt><dd>${safe(customerName(b))}</dd></div>`:''}</dl></section><section class="panel"><h3>Charges</h3><dl class="data-list">${bs.map(i=>`<div><dt>${i.kind==='extension'?'Extension':'Original reservation'} · ${i.slots*30} min ${status(i.status)}</dt><dd>${peso(i.amount_pesos)}</dd></div>`).join('')}</dl>${held.length?`<p class="fine">Held amounts must be settled with staff by their listed deadline.</p>`:''}</section><section class="panel"><h3>Receipts</h3>${rec.length?`<div class="receipt-list">${rec.map(r=>`<div class="receipt-card"><div><strong>${safe(r.number)}</strong><br><small>${safe(when(r.created_at))} · ${safe(r.description)}</small></div><div class="actions"><strong>${peso(r.amount_pesos)}</strong><button class="btn btn-secondary btn-small" type="button" data-print="${r.id}">Print</button></div></div>`).join('')}</div>`:'<p class="muted">A receipt appears after staff record verified payment.</p>'}</section><section class="panel"><h3>Refunds</h3>${ref.length?`<dl class="data-list">${ref.map(r=>`<div><dt>${safe(r.reason)} ${status(r.status)}</dt><dd>${peso(r.amount_pesos)}</dd></div>`).join('')}</dl>`:'<p class="muted">No refund requests for this booking.</p>'}${pay.length&&isStaff()?`<p class="fine">Refunds require admin approval. Staff record completion only after returning money.</p>`:''}</section>${['confirmed','active'].includes(b.status)||active?`<section class="panel full"><h3>More time</h3><p class="muted">Extend your session if the station is free after your current end time.</p>${extensionForm(b)}</section>`:''}</div></div>`;
  }
  function cashierView(){
    const bookings=entries(state.data?.bookings), pending=entries(state.data?.items).filter(i=>i.status==='held'&&holdSeconds(i.hold_until||bookingForItem(i.id)?.hold_until)>0).map(i=>({item:i,booking:bookingForItem(i.id)})).filter(x=>x.booking);
    const arriving=bookings.filter(b=>b.status==='confirmed'&&!play(b.id)&&new Date(b.start_at).getTime()<=now()&&new Date(b.end_at).getTime()>now());
    const active=bookings.filter(b=>!!play(b.id));
    const approved=entries(state.data?.refunds).filter(r=>r.status==='approved');
    const tabs=[['today','Today'],['walkin','Walk-in'],['reports','Reports'],['payments',`Payments${pending.length?` · ${pending.length}`:''}`],['checkin',`Check-in${arriving.length?` · ${arriving.length}`:''}`],['active',`Playing${active.length?` · ${active.length}`:''}`],['refunds',`Refunds${approved.length?` · ${approved.length}`:''}`]];
    let content='';
    if(state.cashierTab==='today') content=todayView();
    if(state.cashierTab==='walkin') content='<div class="cashier-walkin">'+bookView(true)+'</div>';
    if(state.cashierTab==='reports') content=reportsView();
    if(state.cashierTab==='payments') content=pending.length?`<div class="item-list">${pending.map(({item:i,booking:b})=>`<article class="panel"><div class="toolbar"><div><h3>${safe(pc(b.pc_id)?.code||'PC')} · ${safe(b.reference)}</h3><p class="muted">${safe(customerName(b))} · ${safe(when(b.start_at))} → ${safe(when(b.end_at))}</p></div>${status(i.status)}</div><div class="review-row"><span>${i.kind==='extension'?'Extension':'Original reservation'} · ${i.slots*30} minutes</span><strong>${peso(i.amount_pesos)}</strong></div>${holdNotice(i.hold_until||b.hold_until,true)}<p class="fine">Hold expires ${safe(when(i.hold_until||b.hold_until))}. Verify the actual payment with the customer before recording it. Recording payment confirms the booking and creates a receipt.</p><form data-form="payment" data-item="${i.id}" data-hold-deadline="${safe(i.hold_until||b.hold_until)}" class="fields"><div><label for="method-${i.id}">Received by</label><select id="method-${i.id}" name="method" required><option value="cash">Cash</option><option value="external">External payment</option></select></div><div><label for="reference-${i.id}">Payment reference</label><input id="reference-${i.id}" name="reference" value="${safe(cashReferencePreview())}" placeholder="Assigned automatically when recorded" readonly maxlength="100" aria-describedby="reference-help-${i.id}"><p id="reference-help-${i.id}" class="fine" data-reference-help>Cash reference preview. The final number is assigned when payment is recorded.</p></div><div class="field-full"><button class="btn btn-primary" type="submit">Record verified payment · ${peso(i.amount_pesos)}</button></div></form></article>`).join('')}</div>`:emptyBlock('No payments waiting','Held bookings that need staff verification appear here.');
    if(state.cashierTab==='checkin') content=arriving.length?`<div class="item-list">${arriving.map(b=>`<article class="item-row"><div><h3>${safe(pc(b.pc_id)?.code||'PC')} · ${safe(b.reference)}</h3><p>${safe(customerName(b))} · ${safe(when(b.start_at))} → ${safe(when(b.end_at))}</p></div><div class="item-meta"><button class="btn btn-primary btn-small" type="button" data-action="start" data-id="${b.id}">Start play</button><button class="btn btn-secondary btn-small" type="button" data-detail="${b.id}">Details</button></div></article>`).join('')}</div>`:emptyBlock('No players to check in','Confirmed arrivals appear during their reserved time.');
    if(state.cashierTab==='active') content=active.length?`<div class="item-list">${active.map(b=>`<article class="item-row"><div><h3>${safe(pc(b.pc_id)?.code||'PC')} · ${safe(b.reference)}</h3><p>${safe(customerName(b))} · Ends ${safe(when(b.end_at))}</p></div><div class="item-meta"><span class="timer" style="font-size:1.35rem;margin:0" data-timer="${safe(b.end_at)}">--:--:--</span><button class="btn btn-secondary btn-small" type="button" data-detail="${b.id}">Details</button><button class="btn btn-danger btn-small" type="button" data-action="end" data-id="${b.id}">End play</button></div></article>`).join('')}</div>`:emptyBlock('No live sessions','Checked-in players appear here.');
    if(state.cashierTab==='refunds') content=approved.length?`<div class="item-list">${approved.map(r=>`<article class="item-row"><div><h3>Refund #${r.id} · ${peso(r.amount_pesos)}</h3><p>${safe(r.reason)} · ${safe(entries(state.data?.bookings).find(b=>b.id===r.booking_id)?.reference||'')}</p></div><div class="item-meta">${status(r.status)}<button class="btn btn-secondary btn-small" type="button" data-action="refund-complete" data-id="${r.id}">Record money returned</button></div></article>`).join('')}</div>`:emptyBlock('No approved refunds','Admin-approved refunds appear here for staff to complete after returning money.');
    return `<div class="container page"><div class="page-head"><div><h1>Cashier workspace.</h1><p>Verify payments, check players in, and record completed refunds.</p></div></div><div class="inline-note warn" style="margin-bottom:24px">Only record payment after confirming actual cash or an external transfer outside this app. Recording a payment here does not move money.</div><div class="toolbar"><div class="tabs" role="tablist" aria-label="Cashier workflow">${tabs.map(([id,label])=>`<button role="tab" aria-selected="${state.cashierTab===id}" class="${state.cashierTab===id?'active':''}" type="button" data-cashier-tab="${id}">${label}</button>`).join('')}</div><button type="button" class="btn btn-ghost btn-small" data-action="refresh">Refresh</button></div>${content}</div>`;
  }
  function todayView(){
    const bookings=entries(state.data?.bookings),today=localStart(now()).slice(0,10);
    const pending=entries(state.data?.items).filter(i=>i.status==='held'&&holdSeconds(i.hold_until||bookingForItem(i.id)?.hold_until)>0);
    const active=bookings.filter(b=>!!play(b.id)).sort((a,b)=>new Date(a.end_at)-new Date(b.end_at));
    const arriving=bookings.filter(b=>b.status==='confirmed'&&!play(b.id)&&(localStart(new Date(b.start_at).getTime()).slice(0,10)===today||new Date(b.start_at)<=now())&&new Date(b.end_at)>now()).sort((a,b)=>new Date(a.start_at)-new Date(b.start_at));
    const ending=active.filter(b=>new Date(b.end_at).getTime()<=now()+15*60000);
    const row=(b,live)=>'<article class="item-row"><div><h3>'+safe(pc(b.pc_id)?.code||'PC')+' · '+safe(b.reference)+'</h3><p>'+safe(customerName(b))+' · '+safe(when(b.start_at))+' → '+safe(when(b.end_at))+'</p></div><div class="item-meta">'+(live?'<span class="session-countdown" data-timer="'+safe(b.end_at)+'"></span>':'')+'<button type="button" class="btn btn-secondary btn-small" data-detail="'+b.id+'">Details</button>'+(!live&&new Date(b.start_at)<=now()?'<button type="button" class="btn btn-primary btn-small" data-action="start" data-id="'+b.id+'">Start play</button>':'')+(live?'<button type="button" class="btn btn-danger btn-small" data-action="end" data-id="'+b.id+'">End play</button>':'')+'</div></article>';
    const section=(title,list,copy,live)=>'<section class="today-section"><div class="section-head"><h2>'+title+' · '+list.length+'</h2></div>'+ (list.length?'<div class="item-list">'+list.map(b=>row(b,live)).join('')+'</div>':emptyBlock('No '+title.toLowerCase(),copy))+'</section>';
    return '<p class="muted">'+safe(day(new Date(now()).toISOString()))+' · Philippine time</p><section class="today-section"><div class="section-head"><h2>Pending payments · '+pending.length+'</h2><button type="button" class="btn btn-secondary btn-small" data-cashier-tab="payments">Open payments</button></div>'+ (pending.length?'<div class="item-list">'+pending.map(i=>{const b=bookingForItem(i.id);return '<article class="item-row"><div><h3>'+safe(pc(b?.pc_id)?.code||'PC')+' · '+safe(b?.reference)+'</h3><p>'+safe(customerName(b))+' · '+peso(i.amount_pesos)+'</p>'+holdNotice(i.hold_until||b.hold_until,true)+'</div><button type="button" class="btn btn-primary btn-small" data-cashier-tab="payments">Pay</button></article>';}).join('')+'</div>':emptyBlock('No payments waiting','Valid holds appear here until payment is recorded or the hold expires.'))+'</section>'+section('Ending soon',ending,'Sessions with 15 minutes or less remaining appear here.',true)+section('Arriving today',arriving,'Confirmed bookings for today appear here.',false)+section('Active sessions',active,'Live sessions appear here, including sessions that began before midnight.',true);
  }
  function reportsView(){
    const result=reportFilter.data,r=result?.report||{};
    const downloadReady=result&&!reportFilter.loading&&!reportFilter.error&&reportFilter.from===result.from&&reportFilter.to===result.to;
    const download=downloadReady?'href="/api/reports.csv?from='+encodeURIComponent(result.from)+'&amp;to='+encodeURIComponent(result.to)+'" download':'aria-disabled="true"';
    return '<section class="reports"><h2>Transaction report</h2><p class="muted">Payments use their recorded date. Refunds use the date money was returned. Dates are in Philippine time.</p><form data-form="report" class="report-filters"><div><label for="report-from">From</label><input type="date" id="report-from" name="from" required value="'+safe(reportFilter.from)+'"></div><div><label for="report-to">To</label><input type="date" id="report-to" name="to" required value="'+safe(reportFilter.to)+'"></div><button type="submit" class="btn btn-primary">Apply dates</button></form><div class="filters"><button type="button" class="btn btn-ghost btn-small" data-action="report-today">Today</button><button type="button" class="btn btn-ghost btn-small" data-action="report-week">Last 7 days</button><button type="button" class="btn btn-ghost btn-small" data-action="report-all">All time</button><a class="btn btn-secondary btn-small" data-report-download '+download+'>Download CSV</a></div>'+ (reportFilter.loading?'<div class="inline-note" role="status">Loading transaction report…</div>':reportFilter.error?'<div class="inline-note error" role="alert">'+safe(reportFilter.error)+'</div>':!result?'':('<p class="fine">Showing '+safe(result.from||'all dates')+' to '+safe(result.to||'latest recorded date')+'</p><dl class="report-totals"><div><dt>Payments received</dt><dd>'+peso(r.payments_pesos)+'</dd></div><div><dt>Refunds completed</dt><dd>'+peso(r.refunds_pesos)+'</dd></div><div><dt>Net collected</dt><dd>'+peso(r.net_pesos)+'</dd></div></dl><div class="section-head"><h3>Payments</h3></div>'+transactionTable(entries(result.payments),false)+'<div class="section-head"><h3>Completed refunds</h3></div>'+transactionTable(entries(result.refunds),true)))+'</section>';
  }
  function transactionTable(rows,refund){
    if(!rows.length)return emptyBlock(refund?'No completed refunds':'No payments','No transactions match the selected dates.');
    return '<div class="table-wrap"><table><thead><tr><th>'+ (refund?'Returned':'Recorded')+'</th><th>Booking / guest</th><th>'+ (refund?'Reason':'Method / reference')+'</th><th>Amount</th></tr></thead><tbody>'+rows.map(x=>{const b=entries(state.data?.bookings).find(b=>b.id===x.booking_id);return '<tr><td>'+safe(when(refund?x.completed_at:x.created_at))+'</td><td>'+safe(b?.reference||'—')+'<br><small>'+safe(customerName(b))+'</small></td><td>'+safe(refund?x.reason:x.method+' · '+x.reference)+'</td><td>'+peso(x.amount_pesos)+'</td></tr>';}).join('')+'</tbody></table></div>';
  }
  async function loadReports(){
    if(!isStaff())return;const token=++reportFilter.token;reportFilter.loading=true;reportFilter.error='';render();
    try{const data=await api('/api/reports?from='+encodeURIComponent(reportFilter.from)+'&to='+encodeURIComponent(reportFilter.to));if(token!==reportFilter.token)return;reportFilter.data=data;}
    catch(err){if(token!==reportFilter.token)return;reportFilter.error=errorText(err);reportFilter.data=null;}
    finally{if(token===reportFilter.token){reportFilter.loading=false;render();}}
  }
  function emptyBlock(title,message){return `<div class="empty"><h3>${safe(title)}</h3><p>${safe(message)}</p></div>`;}
  function adminView(){
    const sections=[['overview','Overview'],['reports','Reports'],['pcs','PC inventory'],['rates','Rates'],['users','Users'],['maintenance','Maintenance'],['refunds','Refunds'],['audit','Audit']];
    let content='';
    if(state.adminTab==='overview') content=adminOverview();
    if(state.adminTab==='reports') content=reportsView();
    if(state.adminTab==='pcs') content=adminPCs();
    if(state.adminTab==='rates') content=adminRates();
    if(state.adminTab==='users') content=adminUsers();
    if(state.adminTab==='maintenance') content=adminMaintenance();
    if(state.adminTab==='refunds') content=adminRefunds();
    if(state.adminTab==='audit') content=adminAudit();
    return `<div class="container page"><div class="page-head"><div><h1>Operations.</h1><p>Manage the floor and review the transaction record.</p></div><button type="button" class="btn btn-ghost btn-small" data-action="refresh">Refresh data</button></div><div class="admin-layout"><nav class="admin-nav" aria-label="Admin sections">${sections.map(([id,label])=>`<button type="button" data-admin-tab="${id}" class="${state.adminTab===id?'active':''}" ${state.adminTab===id?'aria-current="page"':''}>${label}</button>`).join('')}</nav><div class="admin-content">${content}</div></div></div>`;
  }
  function adminOverview(){
    const r=state.data?.report||{};
    return `<h2>Transaction report</h2><p class="muted">Recorded payments and completed refunds from the persistent ledger.</p><p><a class="btn btn-secondary btn-small" href="/api/admin/backup" download>Download database backup</a></p><div class="metric-grid"><div class="metric"><span>Payments received</span><strong>${peso(r.payments_pesos)}</strong></div><div class="metric"><span>Refunds completed</span><strong>${peso(r.refunds_pesos)}</strong></div><div class="metric"><span>Net collected</span><strong>${peso(r.net_pesos)}</strong></div></div><div class="panel"><h3>Activity</h3><dl class="data-list"><div><dt>Bookings recorded</dt><dd>${Number(r.booking_count||0)}</dd></div><div><dt>Active play sessions</dt><dd>${Number(r.active_sessions||0)}</dd></div></dl></div><div class="section-head"><h2>Recent payments</h2></div>${entries(state.data?.payments).length?`<div class="table-wrap"><table><thead><tr><th>Recorded</th><th>Booking</th><th>Method</th><th>Reference</th><th>Amount</th></tr></thead><tbody>${entries(state.data.payments).slice().sort((a,b)=>new Date(b.created_at)-new Date(a.created_at)).slice(0,12).map(x=>`<tr><td>${safe(when(x.created_at))}</td><td>${safe(entries(state.data.bookings).find(b=>b.id===x.booking_id)?.reference||'—')}</td><td>${safe(x.method)}</td><td>${safe(x.reference)}</td><td>${peso(x.amount_pesos)}</td></tr>`).join('')}</tbody></table></div>`:emptyBlock('No payments yet','Verified payments will appear in this report.')}`;
  }
  function adminPCs(){
    return `<h2>PC inventory</h2><p class="muted">Update station details and availability. Disabling a PC with future bookings may be rejected by the server.</p><div class="table-wrap"><table><thead><tr><th>Station</th><th>Tier</th><th>Specifications</th><th>Status</th><th>Edit</th></tr></thead><tbody>${entries(state.data?.pcs).map(p=>`<tr><td><strong>${safe(p.code)}</strong></td><td>${safe(tier(p.tier_id)?.name||'—')}</td><td>${hardwareSpecs(p)}</td><td>${p.enabled?'Enabled':'Disabled'}</td><td><button class="btn btn-secondary btn-small" type="button" data-edit-pc="${p.id}">Edit</button></td></tr>`).join('')}</tbody></table></div><section class="panel" style="margin-top:22px"><h3 id="pc-form-title">Add a station</h3><form data-form="pc" class="fields"><input type="hidden" name="id" value="0"><div><label for="pc-code">Station code</label><input id="pc-code" name="code" required maxlength="30" placeholder="PC-13"></div><div><label for="pc-tier">Tier</label><select id="pc-tier" name="tier_id">${entries(state.data?.tiers).map(t=>`<option value="${t.id}">${safe(t.name)}</option>`).join('')}</select></div><div class="field-full"><label for="pc-specs">Specifications</label><input id="pc-specs" name="specs" required maxlength="200" placeholder="CPU: processor · RAM: 16 GB · GPU: graphics" aria-describedby="pc-specs-help"><p id="pc-specs-help" class="fine">Label each part CPU, RAM, or GPU and separate with ·. You can also include other specs.</p></div><div class="field-full"><label class="check-label"><input type="checkbox" name="enabled" checked> Enabled for bookings</label></div><div class="field-full actions"><button class="btn btn-primary" type="submit">Save station</button><button class="btn btn-ghost" type="button" data-action="pc-clear">Clear form</button></div></form></section>`;
  }
  function adminRates(){
    return `<h2>Rates</h2><p class="muted">All prices are in Philippine pesos (PHP) per 30-minute slot. Existing booking quotes and receipts keep their original amounts.</p><div class="item-list">${entries(state.data?.tiers).map(t=>`<form data-form="tier" data-id="${t.id}" class="panel fields"><div><label for="tier-name-${t.id}">Tier</label><input id="tier-name-${t.id}" name="name" required maxlength="60" value="${safe(t.name)}"></div><div><label for="tier-price-${t.id}">Price per 30 min · Philippine pesos (₱)</label><input id="tier-price-${t.id}" type="number" name="price_pesos" min="0.01" step="0.01" required value="${Number(t.price_pesos).toFixed(2)}"></div><div class="field-full actions"><strong>Current price ${peso(t.price_pesos)}</strong><button class="btn btn-secondary btn-small" type="submit">Save rate</button></div></form>`).join('')}</div><section class="panel" style="margin-top:22px"><h3>Add a tier</h3><form data-form="tier" data-id="0" class="fields"><div><label for="tier-new-name">Name</label><input id="tier-new-name" name="name" required maxlength="60"></div><div><label for="tier-new-price">Price per 30 min · Philippine pesos (₱)</label><input id="tier-new-price" type="number" name="price_pesos" min="0.01" step="0.01" required></div><div class="field-full"><button class="btn btn-primary" type="submit">Add tier</button></div></form></section>`;
  }
  function adminUsers(){
    const users=entries(state.data?.users);
    return `<h2>Users</h2><p class="muted">Change staff access or deactivate accounts. The last active admin is protected by the server.</p>${users.length?`<div class="table-wrap"><table><thead><tr><th>Name</th><th>Email</th><th>Role</th><th>Active</th><th>Actions</th></tr></thead><tbody>${users.map(u=>`<tr><td><strong>${safe(u.name)}</strong></td><td>${safe(u.email)}</td><td><label class="sr-only" for="role-${u.id}">Role for ${safe(u.name)}</label><select id="role-${u.id}" data-role-select="${u.id}"><option value="customer" ${u.role==='customer'?'selected':''}>Customer</option><option value="cashier" ${u.role==='cashier'?'selected':''}>Cashier</option><option value="admin" ${u.role==='admin'?'selected':''}>Admin</option></select></td><td>${u.active?'Yes':'No'}</td><td><div class="actions"><button class="btn btn-secondary btn-small" type="button" data-action="user-role" data-id="${u.id}">Save role</button><button class="btn ${u.active?'btn-danger':'btn-secondary'} btn-small" type="button" data-action="user-active" data-id="${u.id}" data-active="${!u.active}">${u.active?'Deactivate':'Activate'}</button></div></td></tr>`).join('')}</tbody></table></div>`:emptyBlock('No users','Registered accounts will appear here.')}`;
  }
  function adminMaintenance(){
    const windows=entries(state.data?.maintenance).slice().sort((a,b)=>new Date(b.start_at)-new Date(a.start_at));
    return `<h2>Maintenance</h2><p class="muted">Block a PC for a defined interval. The server rejects windows that conflict with allocated sessions.</p><section class="panel"><h3>Add maintenance window</h3><form data-form="maintenance" class="fields"><div><label for="maint-pc">Station</label><select id="maint-pc" name="pc_id" required>${entries(state.data?.pcs).map(p=>`<option value="${p.id}">${safe(p.code)}</option>`).join('')}</select></div><div><label for="maint-reason">Private staff notes</label><input id="maint-reason" name="reason" required maxlength="200" placeholder="Hardware service"></div><div><label for="maint-public-reason">Public explanation (optional)</label><input id="maint-public-reason" name="public_reason" maxlength="200" placeholder="Station under maintenance"></div><div><label for="maint-start">Starts · Philippine time</label><input id="maint-start" name="start_at" type="datetime-local" required></div><div><label for="maint-end">Ends · Philippine time</label><input id="maint-end" name="end_at" type="datetime-local" required></div><div class="field-full"><button class="btn btn-primary" type="submit">Schedule maintenance</button></div></form></section><div class="section-head"><h2>Scheduled windows</h2></div>${windows.length?`<div class="table-wrap"><table><thead><tr><th>Station</th><th>Starts</th><th>Ends</th><th>Private staff notes</th><th>Public explanation</th></tr></thead><tbody>${windows.map(m=>`<tr><td>${safe(pc(m.pc_id)?.code||'PC')}</td><td>${safe(when(m.start_at))}</td><td>${safe(when(m.end_at))}</td><td>${safe(m.reason)}</td><td>${safe(m.public_reason||'—')}</td></tr>`).join('')}</tbody></table></div>`:emptyBlock('No maintenance windows','Scheduled station downtime will appear here.')}`;
  }
  function adminRefunds(){
    const list=entries(state.data?.refunds).slice().sort((a,b)=>new Date(b.created_at)-new Date(a.created_at));
    return `<h2>Refunds</h2><p class="muted">Approve or reject pending requests. Cashiers complete an approved refund after the money is returned.</p>${list.length?`<div class="item-list">${list.map(r=>`<article class="item-row"><div><h3>Refund #${r.id} · ${peso(r.amount_pesos)}</h3><p>${safe(r.reason)} · ${safe(entries(state.data.bookings).find(b=>b.id===r.booking_id)?.reference||'')} · Requested ${safe(when(r.created_at))}</p></div><div class="item-meta">${status(r.status)}${r.status==='pending'?`<button class="btn btn-secondary btn-small" type="button" data-action="refund-approve" data-id="${r.id}">Approve</button><button class="btn btn-danger btn-small" type="button" data-action="refund-reject" data-id="${r.id}">Reject</button>`:''}</div></article>`).join('')}</div>`:emptyBlock('No refund requests','Customer cancellations and manual refund requests appear here.')}`;
  }
  function adminAudit(){
    const audit=entries(state.data?.audit).slice().sort((a,b)=>new Date(b.created_at)-new Date(a.created_at));
    return `<h2>Audit trail</h2><p class="muted">A record of staff and customer actions.</p>${audit.length?`<div class="table-wrap"><table><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Record</th></tr></thead><tbody>${audit.map(a=>`<tr><td>${safe(when(a.created_at))}</td><td>${safe(user(a.actor_id)?.name||`User #${a.actor_id}`)}</td><td>${safe(a.action.replaceAll('_',' '))}</td><td>#${Number(a.entity_id)}</td></tr>`).join('')}</tbody></table></div>`:emptyBlock('No audit events','Actions will appear once the café starts operating.')}`;
  }
  async function refreshAvailability(quiet=false){
    if(!state.user)return;
    if(quiet&&!idleUI())return;
    const token=++availabilityToken,userId=state.user.id,immediate=immediateWalkin();
    booking.checked=false;
    booking.available=[];booking.statuses=[];
    const start=new Date(`${booking.start}+08:00`);
    if(!immediate&&!validBookingStart(start)){booking.available=[];booking.pcId=null;booking.error='';booking.loading=false;if(!quiet||idleUI())render(true);return;}
    booking.loading=true;booking.error='';if(!quiet)render(true);
    try{
      const result=await api(immediate?`/api/walk-ins/availability?slots=${Number(booking.slots)}`:`/api/availability?start=${encodeURIComponent(start.toISOString())}&slots=${Number(booking.slots)}`);
      if(token!==availabilityToken||state.user?.id!==userId)return;
      booking.available=entries(result.pcs);booking.statuses=entries(result.statuses);
      booking.checked=true;
      if(!booking.available.some(p=>p.id===booking.pcId)) booking.pcId=null;
    }catch(err){if(token!==availabilityToken||state.user?.id!==userId)return;booking.available=[];booking.error=errorText(err);}
    if(token===availabilityToken){booking.loading=false;if(bookingSurface()&&(!quiet||idleUI()))render(true);}
  }
  function updateTimers(){
    // Refresh Today only when a date or action boundary changes, preserving staff inputs.
    if(state.view==='cashier'&&isStaff()&&idleUI()&&todayTimeVersion()!==renderedToday){render(true);return;}
    const startInput=app.querySelector('#book-start');
    if(startInput){
      const timeInput=app.querySelector('#book-time');
      if(timeInput){const validTimes=new Set(startTimes(booking.start.slice(0,10)));Array.from(timeInput.options).forEach(option=>option.disabled=!validTimes.has(option.value));}
      const holdButton=app.querySelector('[data-action="hold"]')||app.querySelector('[data-walkin-submit]');
      if(holdButton)holdButton.disabled=state.busy||!booking.checked||(!immediateWalkin()&&!validBookingStart(startISO()));
    }
    let holdExpired=false;
    document.querySelectorAll('[data-hold-timer]').forEach(el=>{const seconds=holdSeconds(el.dataset.holdTimer);el.textContent=seconds?'Pay within '+holdClock(el.dataset.holdTimer):'Hold expired';if(!seconds&&!expiredHoldDeadlines.has(el.dataset.holdTimer)){expiredHoldDeadlines.add(el.dataset.holdTimer);holdExpired=true;}});
    if(holdExpired&&!expiredHoldRefresh&&!state.busy&&!document.hidden&&!confirmationDialog.open){expiredHoldRefresh=true;loadState().then(()=>{if(!state.busy&&!document.hidden&&!document.activeElement?.closest('form')&&!confirmationDialog.open)render();}).catch(()=>{}).finally(()=>expiredHoldRefresh=false);}
    app.querySelectorAll('form[data-form="payment"][data-hold-deadline]').forEach(form=>{if(holdSeconds(form.dataset.holdDeadline)>0)return;form.querySelectorAll('button[type="submit"]').forEach(button=>button.disabled=true);});
    document.querySelectorAll('[data-timer]').forEach(el=>{
      const end=new Date(el.dataset.timer).getTime();
      const seconds=Math.max(0,Math.ceil((end-now())/1000));
      const hours=Math.floor(seconds/3600),minutes=Math.floor(seconds%3600/60),secs=seconds%60;
      el.textContent=[hours,minutes,secs].map(x=>String(x).padStart(2,'0')).join(':');
    });
    document.querySelectorAll('[data-progress]').forEach(el=>{
      const [start,end]=el.dataset.progress.split('|').map(x=>new Date(x).getTime());
      el.style.width=`${Math.max(0,Math.min(100,(now()-start)/(end-start)*100))}%`;
    });
  }
  function reservationDetails(reservation){
    return [['Station',pc(reservation?.pc_id)?.code||'PC'],['Booking',reservation?.reference||'—']];
  }
  function confirmAction({title,message,confirmLabel,cancelLabel='Cancel',amount=null,amountLabel='Amount',details=[],danger=false}){
    if(state.busy||confirmationDialog.open)return Promise.resolve(false);
    const userId=state.user?.id;
    const set=(name,value)=>confirmationDialog.querySelector('#confirmation-'+name).textContent=value;
    set('title',title);set('description',message);set('approve',confirmLabel);set('cancel',cancelLabel);
    set('amount-label',amountLabel);set('amount',amount===null?'':peso(amount));
    confirmationDialog.querySelector('#confirmation-total').hidden=amount===null;
    const summary=confirmationDialog.querySelector('#confirmation-details');
    summary.hidden=details.length===0;
    summary.innerHTML=details.map(([label,value])=>`<div><dt>${safe(label)}</dt><dd>${safe(value)}</dd></div>`).join('');
    confirmationDialog.querySelector('#confirmation-approve').className='btn '+(danger?'btn-danger':'btn-primary');
    confirmationDialog.returnValue='cancel';
    return new Promise(resolve=>{
      confirmationDialog.addEventListener('close',()=>resolve(confirmationDialog.returnValue==='confirm' && state.user?.id===userId && userId!==undefined),{once:true});
      confirmationDialog.showModal();
    });
  }
  function confirmPayment(item,payment){
    return confirmAction({
      title:'Confirm received payment',
      message:'Have you verified the actual payment outside this app? Recording it confirms the booking and issues a receipt.'+(payment.method==='cash'?' The final cash reference is assigned automatically when recorded.':'')+(payment.duplicateWarning||''),
      confirmLabel:'Confirm payment',amount:item.amount_pesos,amountLabel:'Amount received',
      details:[...reservationDetails(bookingForItem(item.id)),['Received by',payment.method==='cash'?'Cash':'External payment'],[payment.method==='cash'?'Cash reference (preview)':'Reference',payment.method==='cash'?cashReferencePreview():payment.reference]]
    });
  }
  async function referenceWarning(payment){
    if(payment.method==='cash')return '';
    state.busy=true;
    try{
      const result=await api('/api/payment-reference?reference='+encodeURIComponent(payment.reference));
      return result.count>0?` This reference is already recorded on ${result.count} payment${result.count===1?'':'s'}. Verify that this is a separate received payment before proceeding with a duplicate reference.`:'';
    }finally{state.busy=false;}
  }
  function walkinPaymentFields(){
    const cash=booking.method==='cash';
    return `<div><label for="walkin-method">Received by</label><select id="walkin-method" name="method"><option value="cash" ${cash?'selected':''}>Cash</option><option value="external" ${cash?'':'selected'}>External payment</option></select></div><div><label for="walkin-reference">Payment reference</label><input id="walkin-reference" name="reference" maxlength="100" value="${safe(cash?cashReferencePreview():booking.reference)}" ${cash?'readonly':'required'}><p class="fine" data-reference-help>${cash?'Cash reference preview. The final number is assigned when payment is recorded.':'Enter the actual transaction reference from the payment receipt.'}</p></div>`;
  }
  function confirmRefund(id,decision){
    const refund=entries(state.data?.refunds).find(r=>r.id===id);
    if(!refund)return Promise.resolve(false);
    const reservation=entries(state.data?.bookings).find(b=>b.id===refund.booking_id);
    const options={
      approve:{title:'Approve refund?',message:'Approve this request for staff to return the money. Approval does not record the money as returned.',confirmLabel:'Approve refund'},
      reject:{title:'Reject refund?',message:'This request will be marked rejected. No refund completion will be recorded.',confirmLabel:'Reject refund',danger:true},
      complete:{title:'Record returned money?',message:'Confirm the money has actually been returned to the customer. This records the completed refund in the ledger.',confirmLabel:'Record money returned'}
    };
    return confirmAction({...options[decision],amount:refund.amount_pesos,amountLabel:'Refund amount',details:[...reservationDetails(reservation),['Refund','#'+refund.id],['Reason',refund.reason]]});
  }
  async function action(path,body,success){
    if(state.busy)return;
    state.busy=true;app.setAttribute('aria-busy','true');
    const controls=Array.from(app.querySelectorAll('button')).map(button=>[button,button.disabled]);
    controls.forEach(([button])=>button.disabled=true);
    try{await api(path,body);await reload();toast(success);}catch(err){toast(errorText(err),true);}finally{state.busy=false;app.setAttribute('aria-busy','false');controls.forEach(([button,disabled])=>button.disabled=disabled);}
  }
  function printReceipt(id){
    const r=entries(state.data?.receipts).find(x=>x.id===id);
    if(!r)return toast('Receipt unavailable. Refresh and try again.',true);
    const b=entries(state.data?.bookings).find(x=>x.id===r.booking_id),p=pc(b?.pc_id),payment=entries(state.data?.payments).find(x=>x.id===r.payment_id);
    const sheet=document.querySelector('#print-sheet');
    sheet.innerHTML=`<h1>NO SLEEP</h1><p>Gaming café payment receipt</p><div class="print-line"><span>Receipt</span><strong>${safe(r.number)}</strong></div><div class="print-line"><span>Issued</span><strong>${safe(when(r.created_at))} Philippine time</strong></div><div class="print-line"><span>Booking</span><strong>${safe(b?.reference||'—')}</strong></div><div class="print-line"><span>Item</span><strong>${safe(r.description)}</strong></div><div class="print-line"><span>Payment method</span><strong>${safe(payment?.method||'—')}</strong></div><div class="print-line"><span>Payment reference</span><strong>${safe(payment?.reference||'—')}</strong></div><div class="print-total">Total paid: ${peso(r.amount_pesos)}</div><p>Payment was verified and recorded by café staff. Thank you for playing.</p>`;
    window.print();
  }
  app.addEventListener('click',async event=>{
    const button=event.target.closest('button');if(!button||state.busy||confirmationDialog.open)return;
    if(button.dataset.view){setView(button.dataset.view);return;}
    if(button.dataset.authMode){state.authMode=button.dataset.authMode;render();return;}
    if(button.dataset.adminTab){state.adminTab=button.dataset.adminTab;if(state.adminTab==='reports')loadReports();else render();return;}
    if(button.dataset.cashierTab){state.cashierTab=button.dataset.cashierTab;if(state.cashierTab==='walkin'){booking.pcId=null;booking.checkoutKey=null;if(!validBookingStart(startISO()))booking.start=initialStart();refreshAvailability();}else if(state.cashierTab==='reports')loadReports();else render();return;}
    if(button.dataset.detail){state.detailId=Number(button.dataset.detail);setView('detail');return;}
    if(button.dataset.seat){booking.pcId=Number(button.dataset.seat);booking.checkoutKey=null;render();return;}
    if(button.dataset.print){printReceipt(Number(button.dataset.print));return;}
    if(button.dataset.editPc){
      const p=pc(Number(button.dataset.editPc)),form=app.querySelector('[data-form="pc"]');if(!p||!form)return;
      form.elements.id.value=p.id;form.elements.code.value=p.code;form.elements.tier_id.value=p.tier_id;form.elements.specs.value=p.specs;form.elements.enabled.checked=p.enabled;
      app.querySelector('#pc-form-title').textContent=`Edit ${p.code}`;
      form.scrollIntoView({block:'center',behavior:'smooth'});form.elements.code.focus();return;
    }
    const id=Number(button.dataset.id);
    switch(button.dataset.action){
      case 'next-start':{const next=button.dataset.nextStart;if(!next||!validBookingStart(next))return;if(immediateWalkin())booking.walkinMode='scheduled';booking.start=localStart(new Date(next).getTime());booking.pcId=null;booking.checkoutKey=null;await refreshAvailability();return;}
      case 'report-today':case 'report-week':case 'report-all':{
        reportFilter.to=localStart(now()).slice(0,10);reportFilter.from=button.dataset.action==='report-week'?localStart(now()-6*86400000).slice(0,10):reportFilter.to;
        if(button.dataset.action==='report-all'){const dates=[...entries(state.data?.payments).map(x=>x.created_at),...entries(state.data?.refunds).map(x=>x.completed_at),...entries(state.data?.bookings).map(x=>x.created_at)].filter(Boolean).map(x=>localStart(new Date(x).getTime()).slice(0,10));reportFilter.from=dates.reduce((first,date)=>date<first?date:first,reportFilter.to);}await loadReports();return;
      }
      case 'booking-earliest':
      case 'booking-plus-two':
      case 'booking-plus-three':{
        const ahead=button.dataset.action==='booking-earliest'?15*60000:button.dataset.action==='booking-plus-two'?2*3600000:3*3600000;
        booking.start=localStart(Math.ceil((now()+ahead)/900000)*900000);
        booking.pcId=null;booking.checkoutKey=null;
        await refreshAvailability();return;
      }
      case 'retry':window.location.reload();return;
      case 'logout':
        try{await api('/api/logout',{});const me=await api('/api/me');state.user=me.user;state.csrf=me.csrf;state.data=null;state.view='home';render();toast('You have signed out.');}catch(err){toast(errorText(err),true);}return;
      case 'refresh':try{await reload();if(bookingSurface())await refreshAvailability();if((state.view==='cashier'&&state.cashierTab==='reports')||(state.view==='admin'&&state.adminTab==='reports'))await loadReports();toast('Data refreshed.');}catch(err){toast(errorText(err),true);}return;
      case 'walkin-now':case 'walkin-scheduled':booking.walkinMode=button.dataset.action==='walkin-now'?'now':'scheduled';booking.pcId=null;booking.checkoutKey=null;await refreshAvailability();return;
      case 'quiet-on':state.quiet=true;localStorage.setItem('nosleep-quiet','1');state.view='dashboard';render();return;
      case 'quiet-off':state.quiet=false;localStorage.setItem('nosleep-quiet','0');render();return;
      case 'hold':{
        if(state.busy)return;
        if(!booking.pcId)return toast('Choose an available PC first.',true);
        if(!validBookingStart(startISO())){toast('Choose a start at least 15 minutes from now.',true);await refreshAvailability();return;}
        const selected=pc(booking.pcId), selectedTier=selected&&tier(selected.tier_id);
        if(!selected||!selectedTier)return;
        const request={pc_id:selected.id,start_at:startISO(),slots:Number(booking.slots),key:booking.checkoutKey||uid()};
        const end=new Date(new Date(request.start_at).getTime()+request.slots*1800000).toISOString();
        if(!await confirmAction({title:'Place this 10-minute hold?',message:'Reserve this PC for your selected session. Staff must verify and record payment within 10 minutes. The server sets the final amount when you confirm.',confirmLabel:'Confirm hold',amount:sessionAmount(selectedTier.price_pesos,request.slots,request.start_at),amountLabel:'Estimated total',details:[['Station',`${selected.code} · ${selectedTier.name}`],['Starts',when(request.start_at)],['Ends',when(end)],['Duration',`${request.slots*30} minutes`],['Regular rate',`${peso(selectedTier.price_pesos)} / 30 min`],['Night offer','20% off session minutes from 1 AM to 6 AM']]}))return;
        if(!validBookingStart(request.start_at)){toast('Choose a start at least 15 minutes from now.',true);await refreshAvailability();return;}
        if(state.busy)return;
        try{state.busy=true;button.disabled=true;booking.checkoutKey=request.key;const b=await api('/api/bookings',request);await loadState();state.detailId=b.id;state.view='detail';render();booking.checkoutKey=null;toast('Your seat is held for 10 minutes. Visit the cashier to pay.');}catch(err){toast(errorText(err),true);await refreshAvailability();}finally{state.busy=false;button.disabled=false;}return;
      }
      case 'cancel':{
        const reservation=entries(state.data?.bookings).find(b=>b.id===id);
        if(!reservation)return;
        const paid=payments(id);
        if(!await confirmAction({title:'Cancel this booking?',message:'Your reserved seat will be released. Any paid amount not already covered by a refund request will be submitted for admin review. Money is returned separately by staff.',confirmLabel:'Cancel booking',cancelLabel:'Keep booking',danger:true,amount:paid.length?paid.reduce((total,p)=>total+Number(p.amount_pesos),0):null,amountLabel:'Total paid',details:reservationDetails(reservation)}))return;
        await action('/api/bookings/cancel',{booking_id:id},'Booking cancelled. Check the refund status in details.');return;
      }
      case 'start':await action('/api/play/start',{booking_id:id},'Play session started.');return;
      case 'end':{
        const reservation=entries(state.data?.bookings).find(b=>b.id===id);
        if(await confirmAction({title:'End this play session?',message:'Unused time does not receive an automatic refund.',confirmLabel:'End session',cancelLabel:'Keep playing',danger:true,details:reservationDetails(reservation)}))await action('/api/play/end',{booking_id:id},'Play session ended.');return;
      }
      case 'refund-approve':if(await confirmRefund(id,'approve'))await action('/api/refunds/action',{refund_id:id,action:'approve'},'Refund approved.');return;
      case 'refund-reject':if(await confirmRefund(id,'reject'))await action('/api/refunds/action',{refund_id:id,action:'reject'},'Refund rejected.');return;
      case 'refund-complete':if(await confirmRefund(id,'complete'))await action('/api/refunds/action',{refund_id:id,action:'complete'},'Returned money recorded.');return;
      case 'user-role':{
        const role=app.querySelector(`[data-role-select="${id}"]`)?.value;
        if(role)await action('/api/admin/users/role',{user_id:id,role},'User role updated.');return;
      }
      case 'user-active':{
        const active=button.dataset.active==='true';
        if(await confirmAction({title:`${active?'Activate':'Deactivate'} this account?`,message:active?'This account will be able to sign in again.':'This account will be signed out and unable to sign in.',confirmLabel:active?'Activate account':'Deactivate account',danger:!active,details:[['Account',user(id)?.name||`User #${id}`],['Email',user(id)?.email||'—']]}))await action('/api/admin/users/active',{user_id:id,active},`User ${active?'activated':'deactivated'}.`);return;
      }
      case 'pc-clear':{
        const form=app.querySelector('[data-form="pc"]');form?.reset();if(form)form.elements.id.value=0;
        app.querySelector('#pc-form-title').textContent='Add a station';return;
      }
    }
  });
  app.addEventListener('input',event=>{
    if(event.target.name==='guest_name')booking.guestName=event.target.value;
    if(event.target.id==='walkin-reference')booking.reference=event.target.value;
    if(['report-from','report-to'].includes(event.target.id)){
      reportFilter[event.target.name]=event.target.value;
      const link=app.querySelector('[data-report-download]');if(link){link.removeAttribute('href');link.setAttribute('aria-disabled','true');}
    }
  });
  app.addEventListener('change',event=>{
    const paymentForm=event.target.closest('form[data-form="payment"]')||event.target.closest('form[data-form="walkin"]');
    if(paymentForm&&event.target.name==='method'){
      const cash=event.target.value==='cash', reference=paymentForm.elements.reference;
      reference.readOnly=cash;reference.required=!cash;reference.value=cash?cashReferencePreview():'';
      reference.placeholder=cash?'Assigned automatically when recorded':'GCash, Maya or bank transaction reference';
      if(paymentForm.dataset.form==='walkin'){booking.method=event.target.value;booking.reference=reference.value;}
      paymentForm.querySelector('[data-reference-help]').textContent=cash?'Cash reference preview. The final number is assigned when payment is recorded.':'Enter the actual transaction reference from the payment receipt.';
      return;
    }
    if(!event.target.closest('#booking-filters'))return;
    const form=app.querySelector('#booking-filters');
    if(event.target.id==='book-date'){
      const date=form.elements.date.value, times=startTimes(date), previous=booking.start.slice(11,16);
      booking.start=times.length?`${date}T${times.includes(previous)?previous:times[0]}`:'';
    }else if(event.target.id==='book-time'){
      booking.start=`${form.elements.date.value}T${form.elements.time.value}`;
    }else if(!immediateWalkin())booking.start=form.elements.start.value;
    booking.slots=Number(form.elements.slots.value);
    booking.tier=form.elements.tier.value;
    booking.pcId=null;
    booking.checkoutKey=null;
    refreshAvailability();
  });
  app.addEventListener('keydown',event=>{
    const tab=event.target.closest('[role="tab"]');if(!tab)return;
    const row=tab.parentElement, tabs=Array.from(row.querySelectorAll('[role="tab"]'));
    let index=tabs.indexOf(tab);
    if(event.key==='ArrowRight')index=(index+1)%tabs.length;
    else if(event.key==='ArrowLeft')index=(index-1+tabs.length)%tabs.length;
    else if(event.key==='Home')index=0;
    else if(event.key==='End')index=tabs.length-1;
    else return;
    event.preventDefault();tabs[index].click();
    const next=app.querySelector(`[role="tab"][data-auth-mode="${tabs[index].dataset.authMode||''}"]`) || app.querySelector(`[role="tab"][data-cashier-tab="${tabs[index].dataset.cashierTab||''}"]`);
    next?.focus();
  });
  app.addEventListener('submit',async event=>{
    const form=event.target.closest('form[data-form]');if(!form)return;
    event.preventDefault();if(state.busy||confirmationDialog.open)return;
    const data=new FormData(form),type=form.dataset.form;
    if(type==='login'||type==='register'){
      state.busy=true;const submit=form.querySelector('[type="submit"]');submit.disabled=true;
      try{const body={email:String(data.get('email')).trim(),password:String(data.get('password'))};if(type==='register')body.name=String(data.get('name')).trim();const result=await api(`/api/${type}`,body);state.user=result.user;await loadState();state.view='dashboard';render();toast(type==='register'?'Account created. Welcome to NO SLEEP.':'Welcome back.');}catch(err){toast(errorText(err),true);submit.disabled=false;}finally{state.busy=false;}return;
    }
    if(type==='report'){
      reportFilter.from=String(data.get('from')||'');reportFilter.to=String(data.get('to')||'');
      if(!reportFilter.from||!reportFilter.to||reportFilter.from>reportFilter.to)return toast('Choose an end date on or after the start date.',true);
      await loadReports();return;
    }
    if(type==='walkin'){
      const userId=state.user?.id,immediate=immediateWalkin();
      const guest=String(data.get('guest_name')||'').trim(),selected=pc(booking.pcId),rate=selected&&tier(selected.tier_id);
      if(!isStaff()||guest.length<2||guest.length>80)return toast('Enter a guest name between 2 and 80 characters.',true);
      if(!selected||!rate||!booking.checked||!booking.available.some(p=>p.id===selected.id)||(!immediate&&!validBookingStart(startISO())))return toast('Choose an available station and valid start time.',true);
      if(immediate){
        const request={guest_name:guest,pc_id:selected.id,slots:Number(booking.slots),key:booking.checkoutKey||uid(),method:String(data.get('method')||'cash'),reference:String(data.get('reference')||'').trim(),expected_amount_pesos:sessionAmount(rate.price_pesos,Number(booking.slots),startISO())};
        if(request.method==='cash')request.reference='';else if(!request.reference)return toast('Enter the transaction reference from the payment receipt.',true);
        let warning;try{warning=await referenceWarning(request);}catch(err){toast(errorText(err),true);return;}
        if(state.user?.id!==userId||!isStaff())return;
        if(!await confirmAction({title:'Confirm payment and start now?',message:'Have you verified the actual payment outside this app? This records the reviewed amount, issues a receipt and starts play immediately. The server sets the actual start time. If the rate has changed, review the new price before recording payment.'+(request.method==='cash'?' The cash reference is assigned automatically.':'')+warning,confirmLabel:'Record payment and start',amount:request.expected_amount_pesos,amountLabel:'Amount received',details:[['Guest',guest],['Station',selected.code+' · '+rate.name],['Starts','Now · estimated until confirmed'],['Duration',request.slots*30+' minutes'],['Received by',request.method==='cash'?'Cash':'External payment'],['Reference',request.method==='cash'?cashReferencePreview()+' (preview)':request.reference]]}))return;
        if(state.user?.id!==userId||!isStaff())return;
        try{state.busy=true;booking.checkoutKey=request.key;const b=await api('/api/walk-ins/start-now',request);await loadState();state.detailId=b.id;state.view='detail';booking.checkoutKey=null;booking.guestName='';booking.reference='';render();toast('Payment recorded. Receipt issued and play started.');}catch(err){toast(errorText(err),true);try{await loadState();}catch(refreshError){toast(errorText(refreshError),true);}await refreshAvailability();}finally{state.busy=false;if(bookingSurface())render(true);else updateTimers();}return;
      }
      const request={guest_name:guest,pc_id:selected.id,start_at:startISO(),slots:Number(booking.slots),key:booking.checkoutKey||uid()};
      const end=new Date(new Date(request.start_at).getTime()+request.slots*1800000).toISOString();
      if(!await confirmAction({title:'Place this walk-in hold?',message:'No customer account is needed. This reserves the seat for 10 minutes while staff verify and record payment. The server sets the final amount.',confirmLabel:'Confirm walk-in hold',amount:sessionAmount(rate.price_pesos,request.slots,request.start_at),amountLabel:'Estimated total',details:[['Guest',guest],['Station',selected.code+' · '+rate.name],['Starts',when(request.start_at)],['Ends',when(end)],['Duration',request.slots*30+' minutes']]}))return;
      if(state.user?.id!==userId||!isStaff()||!validBookingStart(request.start_at))return toast('Choose a later start time and review the walk-in again.',true);
      try{state.busy=true;booking.checkoutKey=request.key;const b=await api('/api/walk-ins',request);await loadState();state.detailId=b.id;state.view='detail';booking.checkoutKey=null;booking.guestName='';render();toast('Walk-in held. Open Payments to record verified payment.');}catch(err){toast(errorText(err),true);await refreshAvailability();}finally{state.busy=false;}return;
    }
    if(type==='payment'){
      const userId=state.user?.id;
      const payment={item_id:Number(form.dataset.item),method:String(data.get('method')),reference:String(data.get('reference')||'').trim()};
      if(payment.method==='cash')payment.reference='';
      else if(!payment.reference){toast('Enter the transaction reference from the payment receipt.',true);return;}
      const item=entries(state.data?.items).find(i=>i.id===payment.item_id);
      if(!isStaff()||!item||item.status!=='held'||holdSeconds(item.hold_until||bookingForItem(item.id)?.hold_until)===0){toast('This payment is no longer available. Refresh the cashier workspace.',true);return;}
      try{payment.duplicateWarning=await referenceWarning(payment);}catch(err){toast(errorText(err),true);return;}
      if(state.user?.id!==userId||!isStaff())return;
      if(holdSeconds(item.hold_until||bookingForItem(item.id)?.hold_until)===0)return toast('This hold has expired. Create a new reservation before recording payment.',true);
      if(!await confirmPayment(item,payment))return;
      if(state.user?.id!==userId||!isStaff())return;
      if(holdSeconds(item.hold_until||bookingForItem(item.id)?.hold_until)===0)return toast('This hold has expired. Create a new reservation before recording payment.',true);
      delete payment.duplicateWarning;
      await action('/api/payments',{...payment,key:uid()},'Verified payment recorded. Receipt issued.');return;
    }
    if(type==='extend'){
      await action('/api/extensions',{booking_id:Number(form.dataset.booking),slots:Number(data.get('slots')),key:uid()},'Extension held. Ask the cashier to verify and record the additional payment.');return;
    }
    if(type==='pc'){
      await action('/api/admin/pcs',{id:Number(data.get('id')),code:String(data.get('code')).trim(),tier_id:Number(data.get('tier_id')),specs:String(data.get('specs')).trim(),enabled:data.has('enabled')},'Station saved.');return;
    }
    if(type==='tier'){
      await action('/api/admin/tiers',{id:Number(form.dataset.id),name:String(data.get('name')).trim(),price_pesos:String(data.get('price_pesos')).trim()},'Rate saved.');return;
    }
    if(type==='maintenance'){
      const start=new Date(`${data.get('start_at')}+08:00`),end=new Date(`${data.get('end_at')}+08:00`);
      if(!Number.isFinite(start.getTime())||!Number.isFinite(end.getTime())||end<=start){toast('Choose a maintenance end time after its start.',true);return;}
      await action('/api/admin/maintenance',{pc_id:Number(data.get('pc_id')),start_at:start.toISOString(),end_at:end.toISOString(),reason:String(data.get('reason')).trim(),public_reason:String(data.get('public_reason')||'').trim()},'Maintenance window scheduled.');return;
    }
    if(type==='refund'){
      const request={payment_id:Number(form.dataset.payment),reason:String(data.get('reason')).trim()};
      const payment=entries(state.data?.payments).find(p=>p.id===request.payment_id);
      if(!payment){toast('This payment is unavailable. Refresh and try again.',true);return;}
      const reservation=entries(state.data?.bookings).find(b=>b.id===payment.booking_id);
      if(!await confirmAction({title:'Request a refund?',message:'Submit this refund request for admin review. Staff record completion after returning the money.',confirmLabel:'Submit refund request',amount:payment.amount_pesos,amountLabel:'Payment amount',details:[...reservationDetails(reservation),['Reason',request.reason]]}))return;
      await action('/api/refunds',request,'Refund requested for admin review.');return;
    }
  });
  async function boot(){
    try{
      const me=await api('/api/me');state.user=me.user;state.csrf=me.csrf;state.demo=!!me.demo;
      if(state.user){await loadState();state.view='dashboard';}else state.view='home';
      render();
      refreshTimer=setInterval(updateTimers,1000);
      setInterval(syncState,5000);
    }catch(err){app.setAttribute('aria-busy','false');app.innerHTML=`<main id="main" class="container page"><div class="empty"><h3>Café connection unavailable</h3><p>${safe(errorText(err))}</p><button class="btn btn-primary" type="button" data-action="retry">Try again</button></div></main>`;}
  }
  async function syncState(){
    if(polling||!idleUI()||!state.user)return;
    polling=true;
    try{
      await loadState();
      if(bookingSurface()){if(idleUI())await refreshAvailability(true);return;}
      if(!confirmationDialog.open && dataVersion()!==renderedData && state.view!=='book' && state.view!=='auth' && !(state.view==='cashier'&&['walkin','reports'].includes(state.cashierTab)) && !(state.view==='admin'&&state.adminTab==='reports') && !document.activeElement?.closest('form'))render();
    }catch{/* Keep the current view during temporary network failures; expired auth is handled by api(). */}
    finally{polling=false;}
  }
  document.addEventListener('visibilitychange',()=>{if(!document.hidden)syncState();});
  window.addEventListener('focus',syncState);
  boot();
})();
