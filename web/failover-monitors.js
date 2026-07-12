(()=>{
  const dialog=document.getElementById('failoverDialog');
  const token=document.getElementById('foToken');
  if(!dialog||!token)return;
  const row=document.createElement('div');
  row.className='row';
  row.innerHTML='<label>主服务监控<select id="foPrimaryMonitor"><option value="0">使用 Agent Ping</option></select></label><label>备用服务监控<select id="foBackupMonitor"><option value="0">使用 Agent Ping</option></select></label>';
  token.closest('label').before(row);
  let monitorCache=[];
  const baseAPI=api;
	const escapeHTML=s=>String(s||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  function options(vpsId,selected){
    return '<option value="0">使用 Agent Ping</option>'+monitorCache.filter(m=>m.enabled&&m.vpsId===vpsId).map(m=>`<option value="${m.id}" ${+selected===m.id?'selected':''}>${escapeHTML(m.name)} · ${m.type} · ${escapeHTML(m.target)}</option>`).join('');
  }
  async function fillMonitors(primarySelected=0,backupSelected=0){
    monitorCache=await baseAPI('/api/vps/monitors');
    $('foPrimaryMonitor').innerHTML=options($('foPrimary').value,+primarySelected);
    $('foBackupMonitor').innerHTML=options($('foBackup').value,+backupSelected);
  }
  $('foPrimary').addEventListener('change',()=>fillMonitors());
  $('foBackup').addEventListener('change',()=>fillMonitors());
  const originalAPI=api;
  api=async function(url,opt={}){
    if(url==='/api/vps/failover'&&opt.method==='POST'&&opt.body){
      const body=JSON.parse(opt.body);
      body.primaryMonitorId=+$('foPrimaryMonitor').value||0;
      body.backupMonitorId=+$('foBackupMonitor').value||0;
      opt={...opt,body:JSON.stringify(body)};
    }
    return originalAPI(url,opt);
  };
  const originalEdit=window.editFailover;
  window.editFailover=async function(id){
    originalEdit(id);
    const rules=await baseAPI('/api/vps/failover');
    const rule=rules.find(r=>r.id===id);
    if(rule)await fillMonitors(rule.primaryMonitorId,rule.backupMonitorId);
  };
  const openButton=[...document.querySelectorAll('header button')].find(b=>b.textContent.includes('容灾'));
  if(openButton)openButton.addEventListener('click',()=>setTimeout(()=>fillMonitors(),0));
  $('foReset').addEventListener('click',()=>fillMonitors());
})();
