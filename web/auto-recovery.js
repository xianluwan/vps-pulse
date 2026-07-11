(() => {
  const commandField = document.getElementById('changeIpCommand');
  const label = document.createElement('label');
  label.className = 'switch-row';
  label.innerHTML = '<span><b>自动连通性恢复</b><small>连续 Ping 失败后自动换 IP 并更新 DNS</small></span><input id="autoRecovery" type="checkbox"><i></i>';
  commandField.closest('label').after(label);

  const originalApi = api;
  api = async function(url, options = {}) {
    if ((url === '/api/vps' || url.startsWith('/api/vps/')) && (options.method === 'POST' || options.method === 'PUT') && options.body) {
      try { const body=JSON.parse(options.body);body.autoRecovery=document.getElementById('autoRecovery').checked;options={...options,body:JSON.stringify(body)}; } catch (_) {}
    }
    return originalApi(url,options);
  };

  const originalOpenAdd = openAdd;
  openAdd = function(){originalOpenAdd();document.getElementById('autoRecovery').checked=false};
  const originalEdit = edit;
  edit = function(id){originalEdit(id);const v=data.find(item=>item.id===id);document.getElementById('autoRecovery').checked=!!v?.autoRecovery};
})();
