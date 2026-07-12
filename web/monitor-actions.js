(()=>{
  const dialog=document.getElementById('monitorDialog');
  const enabled=document.getElementById('monEnabled');
  if(!dialog||!enabled)return;
  const row=document.createElement('div');
  row.className='row';
  row.innerHTML='<label>故障动作<select id="monFailureAction"><option value="notify">仅通知</option><option value="changeip">触发关联 VPS 换 IP</option></select></label><label>换 IP 冷却时间（分钟）<input id="monCooldown" type="number" min="5" max="1440" value="30"></label>';
  enabled.closest('.switch-row').before(row);

  const originalAPI=api;
  api=async function(url,opt={}){
    if(url==='/api/vps/monitors'&&(opt.method==='POST'||opt.method==='PUT')&&opt.body){
      const body=JSON.parse(opt.body);
      body.failureAction=document.getElementById('monFailureAction').value;
      body.actionCooldownSec=Math.max(5,+document.getElementById('monCooldown').value||30)*60;
      opt={...opt,body:JSON.stringify(body)};
    }
    return originalAPI(url,opt);
  };

  const originalEdit=window.editMonitor;
  window.editMonitor=async function(id){
    originalEdit(id);
    const monitors=await originalAPI('/api/vps/monitors');
    const monitor=monitors.find(x=>x.id===id);
    if(!monitor)return;
    document.getElementById('monFailureAction').value=monitor.failureAction||'notify';
    document.getElementById('monCooldown').value=Math.max(5,Math.round((monitor.actionCooldownSec||1800)/60));
  };
  document.getElementById('monReset').addEventListener('click',()=>{
    document.getElementById('monFailureAction').value='notify';
    document.getElementById('monCooldown').value=30;
  });
})();
