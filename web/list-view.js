(() => {
  const fmtSpeed = n => n >= 1073741824 ? `${(n/1073741824).toFixed(2)} Gbps` : n >= 1048576 ? `${(n/1048576).toFixed(2)} MB/s` : `${(n/1024).toFixed(1)} KB/s`;
  const fmtBytes = n => n >= 1099511627776 ? `${(n/1099511627776).toFixed(2)} TB` : n >= 1073741824 ? `${(n/1073741824).toFixed(2)} GB` : `${(n/1048576).toFixed(1)} MB`;
  const clean = s => String(s || '').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const meter = (value, kind='normal') => `<div class="list-meter ${kind}"><i style="width:${Math.min(Math.max(+value||0,0),100)}%"></i><b>${(+value||0).toFixed(1)}%</b></div>`;

  async function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return;
    }
    const area = document.createElement('textarea');
    area.value = text;
    area.setAttribute('readonly','');
    area.style.cssText = 'position:fixed;left:-9999px;top:0';
    document.body.appendChild(area);
    area.select();
    area.setSelectionRange(0, area.value.length);
    const ok = document.execCommand('copy');
    area.remove();
    if (!ok) throw new Error('浏览器拒绝复制，请配置 HTTPS 后重试');
  }

  window.copyAgentCommand = async (id, type) => {
    const uninstall = "sudo systemctl disable --now vps-pulse-agent 2>/dev/null || true; sudo rm -f /etc/systemd/system/vps-pulse-agent.service /usr/local/bin/vps-pulse-agent; sudo systemctl daemon-reload; echo 'VPS Pulse Agent 已卸载'";
    const command = type === 'install' ? localStorage.getItem(`vpspulse:install:${id}`) : uninstall;
    if (!command) { alert('旧 VPS 没有保存安装命令，请重新添加以生成新令牌。'); return; }
    try { await copyText(command); alert('命令已复制'); }
    catch (error) { prompt('自动复制失败，请手动复制下面的命令：', command); }
  };
  window.removeVPS = async id => {
    if (!confirm('确定删除这台 VPS 吗？流量统计和日志也会删除。远端 Agent 不会自动卸载。')) return;
    await api(`/api/vps/${id}`,{method:'DELETE'}); localStorage.removeItem(`vpspulse:install:${id}`); data=await api('/api/vps'); render();
  };

  render = function () {
    const openedMenu = document.querySelector('.server-menu details[open]')?.dataset.vpsId || '';
    const on=data.filter(v=>v.online).length; $('online').textContent=`${on} / ${data.length} 在线`;
    $('cards').className='server-list';
    $('cards').innerHTML = data.map(v => `<article class="server-row">
      <div class="server-title"><span class="dot ${v.online?'on':''}"></span><div><b>${clean(v.name)}</b><small>${clean(v.IPv4||v.ipv4||'等待 IP')}</small></div></div>
      <div class="server-cell"><small>实时速率</small><strong class="down">↓ ${fmtSpeed(v.RxBPS||0)}</strong><strong class="up">↑ ${fmtSpeed(v.TxBPS||0)}</strong></div>
      <div class="server-cell"><small>流量</small><span>今日 ${fmtBytes(v.DayBytes??v.dayBytes??0)}</span><span>本月 ${fmtBytes(v.MonthBytes??v.monthBytes??0)}</span><span>累计 ${fmtBytes(v.TotalBytes??v.totalBytes??0)}</span></div>
      <div class="server-cell resource"><small>CPU</small>${meter(v.CPU,'cpu')}</div>
      <div class="server-cell resource"><small>内存</small>${meter(v.Memory)}</div>
      <div class="server-cell resource"><small>存储</small>${meter(v.Disk)}</div>
      <div class="server-menu"><details data-vps-id="${v.id}"><summary>操作</summary><div class="menu-pop">
        <button onclick="edit('${v.id}')">服务器设置</button><button onclick="act('${v.id}','ping')">立即 Ping</button><button onclick="act('${v.id}','changeip')">更换 IP</button><button onclick="act('${v.id}','dns')">更新 DNS</button><button onclick="copyAgentCommand('${v.id}','install')">复制安装 Agent</button><button onclick="copyAgentCommand('${v.id}','uninstall')">复制卸载 Agent</button><button onclick="confirm('确认重启？')&&act('${v.id}','reboot')">重启 VPS</button><button class="danger" onclick="removeVPS('${v.id}')">删除 VPS</button>
      </div></details></div>
    </article>`).join('') || '<div class="list-empty">还没有 VPS，点击右上角添加。</div>';
    if (openedMenu) {
      const menu = [...document.querySelectorAll('.server-menu details')].find(item => item.dataset.vpsId === openedMenu);
      if (menu) menu.open = true;
    }
  };

  const headerActions = document.querySelector('header>div:last-child');
  if (headerActions && !document.getElementById('globalSettings')) {
    const settings = document.createElement('button'); settings.id='globalSettings'; settings.className='ghost'; settings.textContent='面板设置';
    settings.onclick=()=>alert('Telegram 设置正在接入，完成后将在这里配置。'); headerActions.appendChild(settings);
  }
  render();
  // 实时速率由 WebSocket 每秒更新；持久化的日/月/累计流量定期从服务端校准。
  setInterval(async () => {
    try {
      const fresh = await api('/api/vps');
      data = fresh;
      render();
    } catch (_) {}
  }, 5000);
})();
