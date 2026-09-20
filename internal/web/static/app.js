'use strict';
const $ = id => document.getElementById(id);
let csrf = '', fingerprint = '', report = null, pollTimer = null;
const show = (id, visible = true) => { $(id).hidden = !visible; };
function error(message) { $('error').textContent = message; show('error', Boolean(message)); }
async function api(path, body) {
 const response = await fetch('/api/' + path, {method: body === undefined ? 'GET' : 'POST', headers: body === undefined ? {} : {'Content-Type':'application/json','X-CSRF-Token':csrf}, body: body === undefined ? undefined : JSON.stringify(body)});
 const data = await response.json(); if (!response.ok) throw new Error(data.error || 'Request failed'); return data;
}
async function action(fn) { error(''); show('busy'); try { await fn(); } catch(e) { error(e.message); } finally { show('busy', false); refreshAudit(); } }
async function refreshAudit(){ if(!$('auditBox').open) return; try{ const events=await api('log'); $('technical').textContent=(events||[]).map(e=>`${e.Time}  ${e.Category}  exit=${e.ExitCode}  ${e.DurationMS} ms${e.Error?'  ERROR: '+e.Error:''}\n    ${e.Command||''}`).join('\n'); $('technical').scrollTop=$('technical').scrollHeight; }catch{} }
function size(n) { return (n / 1073741824).toLocaleString(undefined,{maximumFractionDigits:2}) + ' GiB'; }
function text(tag, value, className) { const el=document.createElement(tag); el.textContent=value; if(className) el.className=className; return el; }
function metrics(id, pairs) { const root=$(id); root.replaceChildren(); pairs.forEach(([label,value])=>{const el=text('div','', 'metric');el.append(text('span',label),text('strong',value));root.append(el);}); }
$('loginForm').addEventListener('submit', event=>{event.preventDefault();action(async()=>{const d=await api('login',{Token:$('token').value});csrf=d.csrf;$('token').value='';show('login',false);show('connect');});});
$('probeForm').addEventListener('submit',event=>{event.preventDefault();action(async()=>{fingerprint='';$('trust').checked=false;const d=await api('probe',{Host:$('host').value,Port:Number($('port').value)});fingerprint=d.fingerprint;$('fingerprint').textContent=d.address+'\n'+fingerprint;show('hostkey');show('connectForm');});});
for (const id of ['host','port']) $(id).addEventListener('input',()=>{fingerprint='';$('trust').checked=false;show('hostkey',false);show('connectForm',false);});
$('auth').addEventListener('change',()=>{const key=$('auth').value==='key';show('keyField',key);show('passwordField',!key);});
$('connectForm').addEventListener('submit',event=>{event.preventDefault();action(async()=>{if(!$('trust').checked)throw new Error('Verify and confirm the SSH host fingerprint first.');const key=$('auth').value==='key';const d=await api('connect',{Username:$('username').value,Password:key?'':$('password').value,PrivateKey:key?$('privateKey').value:'',Passphrase:key?$('passphrase').value:'',Fingerprint:fingerprint,Confirmed:true});for(const id of ['password','privateKey','passphrase'])$(id).value='';$('hostInfo').textContent=$('host').value+' · '+d.Capabilities.Version;$('vm').replaceChildren();d.VMs.forEach(v=>{const o=text('option',v.Name+' — '+v.Datastore);o.value=v.ID;$('vm').append(o);});$('datastore').replaceChildren();d.Datastores.filter(x=>x.Mounted&&['VMFS-5','VMFS-6'].includes(x.Type)).forEach(x=>{const o=text('option',x.Name+' · '+size(x.Free)+' free');o.value=x.UUID;$('datastore').append(o);});show('existing',Boolean(d.ExistingOperation));$('existing').textContent=d.ExistingOperation;show('selection');show('audit');show('connect',false);});});
$('mode').addEventListener('change',()=>{show('moveOptions',$('mode').value==='MOVE');if($('mode').value==='COPY')$('powerOn').checked=false;});
for(const id of ['vm','datastore','mode','powerOn'])$(id).addEventListener('change',()=>{report=null;show('analysis',false);});
$('targetName').addEventListener('input',()=>{report=null;show('analysis',false);});
$('analyzeForm').addEventListener('submit',event=>{event.preventDefault();action(async()=>{report=null;show('analysis',false);const d=await api('analyze',{VMID:Number($('vm').value),TargetUUID:$('datastore').value,Mode:$('mode').value,PowerOn:$('mode').value==='MOVE'&&$('powerOn').checked,TargetName:$('targetName').value});report=d;$('maintenance').checked=false;$('start').disabled=true;$('readyBadge').textContent=d.Ready?'READY':'BLOCKED';$('readyBadge').className='badge '+(d.Ready?'ok':'block');metrics('metrics',[['SOURCE DATASTORE',d.SourceDatastore],['POWER STATE',d.Power],['TARGET FREE',size(d.TargetFree)],['REQUIRED',size(d.Required)]]);$('checks').replaceChildren();d.Checks.forEach(c=>{const tr=document.createElement('tr');tr.append(text('td',c.Name),text('td',c.Status,c.Status.toLowerCase()),text('td',c.Detail));$('checks').append(tr);});$('disks').replaceChildren();(d.Disks||[]).forEach(disk=>{const el=text('div',disk.Source,'disk');el.append(text('small','Provisioned '+size(disk.Provisioned)+' · Allocated '+(disk.AllocationKnown?size(disk.Allocated):'unknown; using provisioned')+' · Source '+(disk.Thin?'thin':'thick')+' → thin'));$('disks').append(el);});$('destination').textContent='Target: '+d.TargetVMX;show('analysis');$('analysis').scrollIntoView({behavior:'smooth',block:'start'});});});
$('maintenance').addEventListener('change',()=>{$('start').disabled=!(report&&report.Ready&&$('maintenance').checked);});
$('start').addEventListener('click',()=>action(async()=>{$('start').disabled=true;const d=await api('start',{AnalysisID:report.ID,MaintenanceConfirmed:$('maintenance').checked});show('analysis',false);show('selection',false);renderJob(d);poll();}));
function rate(j){
 if(j.Complete||j.Phase!=='cloning'||!j.DiskStarted||j.Progress<=0) return '';
 const secs=(Date.now()-new Date(j.DiskStarted))/1000;
 if(secs<5) return '';
 const left=Math.round(secs*(100-j.Progress)/j.Progress);
 let out=' · ~'+(left>90?Math.round(left/60)+' min':left+' s')+' left';
 if(j.DiskBytes>0) out+=' · '+Math.round(j.DiskBytes*j.Progress/100/secs/1048576)+' MiB/s';
 return out;
}
function renderJob(j) {show('nextAction',j.Complete);show('job');$('jobTitle').textContent=j.Complete?(j.Phase==='completed'?'Migration completed':j.Phase==='rolled_back'?'Registration restored':'Migration stopped'):'Migration in progress';$('phase').textContent=j.Phase.replaceAll('_',' ');$('jobMessage').textContent=j.Message;show('jobError',Boolean(j.Error));$('jobError').textContent=j.Error;$('currentDisk').textContent=j.CurrentDisk?`Disk ${j.DiskIndex} of ${j.DiskCount} · ${j.CurrentDisk}`:'';if(j.Complete||j.Phase==='cloning'){$('progress').value=j.Progress;$('percent').textContent=j.Progress+'%';}else{$('progress').removeAttribute('value');$('percent').textContent='working';}$('elapsed').textContent='Elapsed '+Math.floor(((j.Complete?new Date(j.Updated):Date.now())-new Date(j.Started))/1000)+' s'+rate(j);show('shutdownActions',j.Phase==='awaiting_shutdown');show('rollback',j.Complete&&j.CanRollback);metrics('result',[['SOURCE REGISTRATION',j.SourceRegistration],['SOURCE POWER',j.SourcePower],['TARGET REGISTRATION',j.TargetRegistration],['TARGET POWER',j.TargetPower],['TARGET VERIFIED',j.TargetVerified?'Yes':'Not yet'],['SOURCE FILES','Preserved']]);$('jobPaths').textContent='Source: '+j.SourceVMX+'\nTarget: '+j.TargetVMX;$('cloneLog').textContent=j.TechnicalLog||'';return j;}
function poll(){clearTimeout(pollTimer);pollTimer=setTimeout(async()=>{try{const j=renderJob(await api('job'));refreshAudit();if(!j.Complete)poll();}catch(e){error(e.message+' — the ESXi clone may continue.');poll();}},2000);}
for(const id of ['wait','manual','force'])$(id).addEventListener('click',()=>action(async()=>{if(id==='force'&&!confirm('Force Power Off is equivalent to cutting power and may lose guest data. Explicitly confirm force shutdown of the selected source VM.'))return;await api('control',{Action:id,ForceConfirmed:id==='force'});}));
$('rollback').addEventListener('click',()=>action(async()=>{if(!confirm('Restore source registration? The target must be powered off. Both copies and all files will remain, and neither VM will be powered on.'))return;renderJob(await api('rollback',{Confirmed:true}));}));
$('refreshLog').addEventListener('click',()=>refreshAudit());
(async()=>{try{const d=await api('session');csrf=d.csrf;show('login',false);show('connect');if(d.hasJob){show('connect',false);show('audit');renderJob(await api('job'));poll();}}catch{}})();

$('newConnection').addEventListener('click',()=>{clearTimeout(pollTimer);show('job',false);show('nextAction',false);show('connect');report=null;error('');});
let auditTimer=null;
$('auditBox').addEventListener('toggle',()=>{clearInterval(auditTimer);auditTimer=null;if($('auditBox').open){refreshAudit();auditTimer=setInterval(refreshAudit,2000);}});
