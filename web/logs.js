(() => {
  const section = document.createElement('section');
  section.className = 'log-section';
  section.innerHTML = `<article class="log-card">
    <div class="log-head"><div><small>最近 7 天</small><h2>事件日志</h2></div><div><button class="ghost" id="refreshLogs">刷新</button> <button class="ghost danger" id="clearLogs">清空日志</button></div></div>
    <div id="logList" class="log-list"><div class="log-empty">正在载入…</div></div>
  </article>`;
  const cards = document.getElementById('cards');
  cards.parentNode.insertBefore(section, cards.nextSibling);

  const labels = {online:'上线',offline:'离线',dns_updated:'DNS 更新成功',dns_failed:'DNS 更新失败',change_ip:'换 IP',ip_changed:'IP 已变化'};
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
  setInterval(loadLogs, 30000);
})();
