(() => {
  const section = document.createElement('section');
  section.className = 'log-section';
  section.innerHTML = `<article class="log-card">
    <div class="log-head"><div><small>最近 7 天</small><h2>事件日志</h2></div><div><button class="ghost" id="refreshLogs">刷新</button> <button class="ghost danger" id="clearLogs">清空日志</button></div></div>
    <div id="logList" class="log-list"><div class="log-empty">正在载入…</div></div>
  </article>`;
  const cards = document.getElementById('cards');
  cards.parentNode.insertBefore(section, cards.nextSibling);

  const labels = {
    agent_online:'Agent 上线',agent_offline:'Agent 离线',
    action_ping:'手动 Ping',action_changeip:'手动换 IP',action_dns:'手动更新 DNS',action_reboot:'手动重启',action_failed:'操作失败',
    ping_check_started:'开始自动 Ping',ping_ok:'Ping 成功',ping_failed:'Ping 失败',ping_recovered:'连通性恢复',
    change_ip_started:'开始换 IP',change_ip_command_ok:'换 IP 命令成功',change_ip_failed:'换 IP 失败',change_ip_skipped:'换 IP 已跳过',
    ip_check:'检测新 IP',ip_detected:'发现新 IP',ip_changed:'IP 已变化',
    dns_updated:'DNS 更新成功',dns_failed:'DNS 更新失败',
    auto_recovery_config:'自动恢复设置',settings_updated:'服务器设置已保存',reboot:'重启 VPS',
    failover_settings:'容灾设置',failover_activated:'容灾已切换',failover_recovered:'主节点已恢复',failover_failed:'容灾失败',failover_recheck:'复检主节点',failover_recheck_failed:'主节点复检失败',failover_backup_check:'检测备用节点',failover_backup_ready:'备用节点可用',failover_test_ok:'容灾演练通过'
    ,monitor_settings:'监控设置',monitor_up:'服务正常',monitor_down:'服务故障',monitor_recovered:'服务恢复',resource_alert:'资源告警',resource_recovered:'资源恢复',backup_created:'备份已创建',agent_upgrade_requested:'Agent 升级已下发',agent_upgrade_ready:'Agent 升级完成',agent_upgrade_failed:'Agent 升级失败',agent_rollback_ready:'Agent 回滚完成',agent_rollback_failed:'Agent 回滚失败'
  };
  const safe = value => String(value || '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

  async function loadLogs() {
    const list = document.getElementById('logList');
    try {
      const logs = await api('/api/vps/logs');
      if (!logs.length) { list.innerHTML = '<div class="log-empty">最近一周没有日志</div>'; return; }
      list.innerHTML = logs.map(item => `<div class="log-row">
        <time>${new Date(item.at).toLocaleString('zh-CN',{hour12:false})}</time>
        <span class="log-vps">${safe(item.vpsName || item.vpsId || '系统')}</span>
        <span class="log-type">${safe(labels[item.type] || item.type)}</span>
        <span class="log-detail">${safe(item.detail)}</span>
      </div>`).join('');
    } catch (error) { list.innerHTML = `<div class="log-empty">日志加载失败：${safe(error.message)}</div>`; }
  }

  document.getElementById('refreshLogs').onclick = loadLogs;
  document.getElementById('clearLogs').onclick = async () => {
    if (!confirm('确定清空全部事件日志吗？此操作无法恢复。')) return;
    try { await api('/api/vps/logs',{method:'DELETE'}); await loadLogs(); } catch (error) { alert(`清空失败：${error.message}`); }
  };
  loadLogs();
  setInterval(loadLogs, 5000);
})();
