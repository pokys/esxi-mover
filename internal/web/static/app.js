'use strict';
const $ = id => document.getElementById(id);
let csrf = '', fingerprint = '', report = null, pollTimer = null, stores = {}, busy = false;
const show = (id, visible = true) => { $(id).hidden = !visible; };
function error(message) { $('error').textContent = message; show('error', Boolean(message)); if(message) $('error').scrollIntoView({behavior:'smooth',block:'center'}); }
async function api(path, body) {
 const response = await fetch('/api/' + path, {method: body === undefined ? 'GET' : 'POST', headers: body === undefined ? {} : {'Content-Type':'application/json','X-CSRF-Token':csrf}, body: body === undefined ? undefined : JSON.stringify(body)});
 const data = await response.json();
 if (!response.ok) {
  const failure = new Error(data.error || 'Request failed');
  failure.status = response.status;
  throw failure;
 }
 return data;
}
function updateStart() {
 $('start').disabled = busy || !(report && report.Ready && $('maintenance').checked);
}
async function action(fn, doing) {
 if (busy) return;
 busy = true;
 const controls = [...document.querySelectorAll('main button, main input, main select, main textarea')].map(el => [el, el.disabled]);
 controls.forEach(([el]) => { el.disabled = true; });
 error('');
 $('busyText').textContent = doing || 'Working';
 show('busy');
 try { await fn(); }
 catch(e) { error(e.message); }
 finally {
  controls.forEach(([el, disabled]) => { el.disabled = disabled; });
  busy = false;
  updateStart();
  show('busy', false);
  refreshAudit();
 }
}
async function refreshAudit(){ if(!$('auditBox').open) return; try{ const events=await api('log'); $('technical').textContent=(events||[]).map(e=>`${e.Time}  ${e.Category}  exit=${e.ExitCode}  ${e.DurationMS} ms${e.Runs>1?'  ×'+e.Runs:''}${e.Error?'  ERROR: '+e.Error:''}\n    ${e.Command||''}`).join('\n'); $('technical').scrollTop=$('technical').scrollHeight; }catch{} }
function size(n) { return (n / 1073741824).toLocaleString(undefined,{maximumFractionDigits:2}) + ' GiB'; }
function text(tag, value, className) { const el=document.createElement(tag); el.textContent=value; if(className) el.className=className; return el; }
function metrics(id, pairs) { const root=$(id); root.replaceChildren(); pairs.forEach(([label,value])=>{const el=text('div','', 'metric');el.append(text('span',label),text('strong',value));root.append(el);}); }
$('loginForm').addEventListener('submit', event=>{event.preventDefault();action(async()=>{const d=await api('login',{Token:$('token').value});csrf=d.csrf;$('token').value='';show('login',false);show('connect');},'Unlocking');});
$('probeForm').addEventListener('submit',event=>{event.preventDefault();action(async()=>{fingerprint='';const d=await api('probe',{Host:$('host').value,Port:Number($('port').value)});fingerprint=d.fingerprint;$('fingerprint').textContent=d.address+'\n'+fingerprint;show('hostkey');show('connectForm');},'Reading the host key from '+$('host').value);});
for (const id of ['host','port']) $(id).addEventListener('input',()=>{fingerprint='';show('hostkey',false);show('connectForm',false);});
$('auth').addEventListener('change',()=>{const key=$('auth').value==='key';show('keyField',key);show('passwordField',!key);});
$('connectForm').addEventListener('submit',event=>{event.preventDefault();action(async()=>{const key=$('auth').value==='key';const d=await api('connect',{Username:$('username').value,Password:key?'':$('password').value,PrivateKey:key?$('privateKey').value:'',Passphrase:key?$('passphrase').value:'',Fingerprint:fingerprint,Confirmed:true});for(const id of ['password','privateKey','passphrase'])$(id).value='';$('hostInfo').textContent=$('host').value+' · '+d.Capabilities.Version;connected($('host').value);$('vm').replaceChildren();d.VMs.forEach(v=>{const o=text('option',v.Name+' — '+v.Datastore);o.value=v.ID;$('vm').append(o);});$('datastore').replaceChildren();stores={};d.Datastores.forEach(x=>{stores[x.UUID]=x.Name;});d.Datastores.filter(x=>x.Mounted&&['VMFS-5','VMFS-6'].includes(x.Type)).forEach(x=>{const o=text('option',x.Name+' · '+size(x.Free)+' free');o.value=x.UUID;$('datastore').append(o);});show('existing',Boolean(d.ExistingOperation));$('existing').textContent=d.ExistingOperation;show('selection');show('audit');show('connect',false);},'Signing in to '+$('host').value+' and reading virtual machines and datastores');});
const mode=()=>document.querySelector('input[name=mode]:checked').value;
const hints={
 COPY:'The VM is shut down at the start and stays off. The copy holds everything up to the shutdown and is left unregistered.',
 MOVE:'The VM is shut down at the start. The target holds everything up to the shutdown and takes its place in the inventory.',
 liveCOPY:'The VM keeps running during the copy and is shut down only at the end, then stays off. The copy holds everything up to the shutdown and is left unregistered.',
 liveMOVE:'The VM keeps running during the copy and is shut down only at the end. The target holds everything up to the shutdown and takes its place in the inventory.'
};
const bringHint=' Disks outside the VM folder are copied into the target folder too (experimental); the originals stay where they are.';
// The hint under the operation names only its result, and every switch has a
// fixed description, so nothing below moves while options are toggled.
const modeHints={
 COPY:'The copy holds everything up to the shutdown and is left unregistered; the source stays registered and off.',
 MOVE:'The target holds everything up to the shutdown and takes the source\'s place in the inventory.'
};
function modeChanged(){
 const move=mode()==='MOVE';
 $('powerOn').disabled=!move;
 if(!move)$('powerOn').checked=false;
 $('modeHint').textContent=modeHints[mode()];
}
modeChanged();
for(const r of document.querySelectorAll('input[name=mode]'))r.addEventListener('change',()=>{modeChanged();report=null;show('analysis',false);});
for(const id of ['vm','datastore','powerOn','live','bringDisks'])$(id).addEventListener('change',()=>{report=null;show('analysis',false);});
$('targetName').addEventListener('input',()=>{report=null;show('analysis',false);});
$('analyzeForm').addEventListener('submit',event=>{event.preventDefault();action(async()=>{report=null;show('analysis',false);const d=await api('analyze',{VMID:Number($('vm').value),TargetUUID:$('datastore').value,Mode:mode(),PowerOn:mode()==='MOVE'&&$('powerOn').checked,Live:$('live').checked,BringDisks:$('bringDisks').checked,TargetName:$('targetName').value});report=d;$('maintenance').checked=false;$('start').disabled=true;$('readyBadge').textContent=d.Ready?'Ready':'Blocked';$('readyBadge').className='badge '+(d.Ready?'ok':'block');renderReport(d);show('analysis');$('analysis').scrollIntoView({behavior:'smooth',block:'start'});},'Running the safety checks on ESXi');});
const base=p=>(p||'').split('/').filter(Boolean).pop()||'';
function renderReport(d){
 const move=d.Request.Mode==='MOVE';
 $('fromStore').textContent=d.SourceDatastore;$('fromDir').textContent=base(d.SourceDir)+'/';
 $('toStore').textContent=stores[d.Request.TargetUUID]||'target datastore';$('toDir').textContent=base(d.TargetDir)+'/';
 $('routeMode').textContent=(move?'Copy and switch':'Copy only')+(d.Request.Live?' · running during the copy':'')+(move&&d.Request.PowerOn?' · start':'');
 $('modeNote').textContent=(d.Request.Live?'Experimental. ':'')+hints[(d.Request.Live?'live':'')+d.Request.Mode]+(d.Request.BringDisks?bringHint:'');
 metrics('metrics',[['Virtual machine',d.VM.Name],['Power state',d.Power],['Data to copy',size(d.Allocated)+' of '+size(d.Provisioned)],['Space needed',size(d.Required)+' of '+size(d.TargetFree)+' free']]);
 const bad=d.Checks.filter(c=>c.Status!=='OK'), blocks=bad.filter(c=>c.Status==='BLOCK').length;
 $('checkSummary').textContent=blocks?blocks+' of '+d.Checks.length+' safety checks block this migration':bad.length?'Safe to start · '+bad.length+' warning'+(bad.length>1?'s':''):'All '+d.Checks.length+' safety checks passed';
 $('checkSummary').className='summary '+(blocks?'block':bad.length?'warning':'ok');
 renderIssues(bad.filter(c=>c.Status==='BLOCK').concat(bad.filter(c=>c.Status!=='BLOCK')), blocks>0);
 $('checks').replaceChildren();d.Checks.forEach(c=>{const tr=document.createElement('tr');tr.append(text('td',c.Name),text('td',c.Status,c.Status.toLowerCase()),text('td',c.Detail));$('checks').append(tr);});$('checkBox').open=false;
 $('disks').replaceChildren();(d.Disks||[]).forEach(disk=>{const renamed=base(disk.Target)!==base(disk.Source);const el=text('div',base(disk.Source)+(renamed?' → '+base(disk.Target):''),'disk');if(disk.External)el.append(text('em','From another folder','tag'),text('small',disk.Source));el.append(text('small',size(disk.Provisioned)+' provisioned · '+(disk.AllocationKnown?size(disk.Allocated)+' used':'usage unknown, counting provisioned')+' · '+(disk.Thin?'thin':'thick')+' → thin'));$('disks').append(el);});
 $('destination').textContent=d.SourceDir+'  →  '+d.TargetDir;
}
// Problems read as one grouped list. Each row names the problem in plain words
// with its reason underneath; a row with steps opens to show what to do, and
// the first block starts open so the way forward is visible without a click.
function renderIssues(list, blocked){
 const root=$('problems');root.replaceChildren();
 if(!list.length)return;
 const box=text('div','','issues');
 list.forEach((c,i)=>{
  const steps=c.Steps||[], more=steps.length>0||Boolean(c.Why), open=i===0&&c.Status==='BLOCK'&&more;
  const item=text('div','','issue is-'+c.Status.toLowerCase()+(open?' open':''));
  const head=text(more?'button':'div','','issue-head');if(more)head.type='button';
  const words=text('div','','issue-text');words.append(text('strong',c.Title||c.Name),text('span',c.Detail));
  head.append(text('span','','issue-icon'),words);
  item.append(head);
  if(more){
   head.append(text('span','›','chev'));head.setAttribute('aria-expanded',open);
   head.addEventListener('click',()=>head.setAttribute('aria-expanded',item.classList.toggle('open')));
   const body=text('div','','issue-body');
   if(c.Why)body.append(text('h4','Why it matters'),text('p',c.Why,'why'));
   if(steps.length){const ol=document.createElement('ol');steps.forEach(s=>{const li=text('li',s.Text);if(s.Command)li.append(commandLine(s.Command));ol.append(li);});body.append(text('h4','What to do'),ol);}
   item.append(body);
  }
  box.append(item);
 });
 root.append(box);
 if(blocked){const again=text('button','Analyze again','secondary');again.type='button';again.addEventListener('click',()=>$('analyzeForm').requestSubmit());const bar=text('div','','actions');bar.append(again);root.append(bar);}
}
// Plain HTTP on a LAN has no clipboard API, so fall back to selecting the
// command for Ctrl+C, as the log copy does.
function commandLine(cmd){
 const line=text('div','','cmd'),code=text('code',cmd),copy=text('button','Copy');copy.type='button';
 copy.addEventListener('click',async()=>{
  try{await navigator.clipboard.writeText(cmd);copy.textContent='Copied';}
  catch{getSelection().selectAllChildren(code);copy.textContent='Press Ctrl+C';}
  setTimeout(()=>{copy.textContent='Copy';},2000);
 });
 line.append(code,copy);return line;
}
$('maintenance').addEventListener('change', updateStart);
function showJob(j) {
 report = null;
 show('analysis', false);
 show('selection', false);
 show('connect', false);
 show('audit');
 clearTimeout(pollTimer);
 renderJob(j);
 if (!j.Complete) poll();
}
async function startMigration() {
 if (!report || !report.Ready || !$('maintenance').checked) return;
 const id = report.ID;
 try {
  showJob(await api('start', {AnalysisID:id, MaintenanceConfirmed:true}));
 } catch(failure) {
  // A lost HTTP reply can still mean the job started. Reconcile by ID;
  // never automatically repeat the POST.
  try {
   const j = await api('job');
   if (j.ID === id) { showJob(j); return; }
  } catch(check) {
   if (check.status !== 404 && (!failure.status || failure.status >= 500)) {
    report = null;
    throw new Error('Could not confirm whether the migration started. Reload this page to check its status before starting another migration.');
   }
  }
  if (failure.status === 409) report = null;
  throw failure;
 }
}
$('start').addEventListener('click', () => action(startMigration, 'Starting the migration'));
function rate(j){
 if(j.Complete||j.Phase!=='cloning'||!j.DiskStarted||j.Progress<=0) return '';
 const secs=(Date.now()-new Date(j.DiskStarted))/1000;
 if(secs<5) return '';
 const left=Math.round(secs*(100-j.Progress)/j.Progress);
 let out=' · ~'+(left>90?Math.round(left/60)+' min':left+' s')+' left';
 if(j.DiskBytes>0) out+=' · '+Math.round(j.DiskBytes*j.Progress/100/secs/1048576)+' MiB/s';
 return out;
}
const outcome={completed:'ok',rolled_back:'warn',stopped:'warn',failed:'fail',unknown:'warn',awaiting_shutdown:'warn'};
let live=false;
function renderJob(j) {
 const unknown = j.Phase === 'unknown';
 const titles = {completed:'Migration completed', failed:'Migration failed', stopped:'Migration stopped', unknown:'Outcome unknown', rolled_back:j.Live?'Source restored':'Registration restored'};
 $('job').dataset.state = j.Phase === 'completed' && j.Error ? 'warn' : outcome[j.Phase] || (j.Complete ? 'fail' : 'running');
 show('nextAction', j.Complete);
 show('job');
 $('jobTitle').textContent = titles[j.Phase] || 'Migration in progress';
 $('phase').textContent = j.Phase.replaceAll('_', ' ');
 $('jobMessage').textContent = j.Message;
 show('jobError', Boolean(j.Error));
 $('jobError').textContent = j.Error;
 $('currentDisk').textContent = j.CurrentDisk ? `Disk ${j.DiskIndex} of ${j.DiskCount} · ${j.CurrentDisk}` : '';
 if (j.Complete || j.Phase === 'cloning' || unknown) {
  $('progress').value = j.Progress;
  $('percent').textContent = j.Progress + (unknown ? '% last reported' : '%');
 } else {
  $('progress').removeAttribute('value');
  $('percent').textContent = 'working';
 }
 $('elapsed').textContent = 'Elapsed ' + Math.floor(((j.Complete ? new Date(j.Updated) : Date.now()) - new Date(j.Started)) / 1000) + ' s' + rate(j);
 show('shutdownActions', j.Phase === 'awaiting_shutdown');
 show('stop', j.CanStop);
 live = j.Live;
 show('rollback', j.Complete && j.CanRollback);
 metrics('result', [['Source registration',j.SourceRegistration], ['Source power',j.SourcePower], ['Target registration',j.TargetRegistration], ['Target power',j.TargetPower], ['Target verified',j.TargetVerified?'Yes':'Not yet'], ['Source files','Preserved']]);
 $('jobPaths').textContent = 'Source: ' + j.SourceVMX + '\nTarget: ' + j.TargetVMX;
 $('cloneLog').textContent = j.TechnicalLog || '';
 return j;
}
function poll(){clearTimeout(pollTimer);pollTimer=setTimeout(async()=>{try{const j=renderJob(await api('job'));refreshAudit();if(!j.Complete)poll();}catch(e){error(e.message+' — the ESXi clone may continue.');poll();}},2000);}
for(const id of ['wait','manual','force'])$(id).addEventListener('click',()=>action(async()=>{if(id==='force'&&!confirm('Force Power Off is equivalent to cutting power and may lose guest data. Explicitly confirm force shutdown of the selected source VM.'))return;await api('control',{Action:id,ForceConfirmed:id==='force'});},'Sending the request to ESXi'));
$('rollback').addEventListener('click',()=>action(async()=>{if(!confirm('Restore source registration? The target must be powered off. Both copies and all files will remain, and neither VM will be powered on.'))return;renderJob(await api('rollback',{Confirmed:true}));},'Restoring the source registration'));
$('stop').addEventListener('click',()=>action(async()=>{
 const what=live
  ?'The clone in progress ends, the temporary snapshot is merged back and the source keeps running, since it has not been shut down yet.'
  :'The clone in progress ends and nothing is registered on the target. The source stays registered; if it was already shut down, it stays off until you start it.';
 if(!confirm('Stop the migration?\n\n'+what+' The partial target folder is kept.'))return;
 await api('control',{Action:'stop',ForceConfirmed:true});
},'Stopping the migration'));
$('copyLog').addEventListener('click',async()=>{
 const b=$('copyLog');
 try{await navigator.clipboard.writeText($('technical').textContent);b.textContent='Copied';}
 catch{getSelection().selectAllChildren($('technical'));b.textContent='Press Ctrl+C';}
 setTimeout(()=>{b.textContent='Copy';},2000);
});
action(async () => {
 try {
  const d = await api('session');
  csrf = d.csrf;
  show('login', false);
  show('connect');
  if (d.connected) connected(d.address);
  if (d.hasJob) {
   show('connect', false);
   showJob(await api('job'));
  }
 } catch(e) {
  if (e.status !== 401) throw e;
 }
}, 'Restoring session');

function connected(host){$('statusText').textContent=host;show('status');}
$('change').addEventListener('click',()=>{report=null;show('selection',false);show('analysis',false);show('connect');error('');});
$('newConnection').addEventListener('click',()=>{clearTimeout(pollTimer);show('job',false);show('nextAction',false);show('connect');report=null;error('');});
let auditTimer=null;
// The appliance stores nothing in the browser, so the choice lasts for this
// page only; the system preference decides on every load.
function setTheme(t){document.documentElement.dataset.theme=t;$('theme').textContent=t==='dark'?'☀':'☾';}
setTheme(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');
$('theme').addEventListener('click',()=>setTheme(document.documentElement.dataset.theme==='dark'?'light':'dark'));
$('auditBox').addEventListener('toggle',()=>{clearInterval(auditTimer);auditTimer=null;if($('auditBox').open){refreshAudit();auditTimer=setInterval(refreshAudit,2000);}});
