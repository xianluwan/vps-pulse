(() => {
  const originalApi = api;
  api = async function (url, options = {}) {
    const result = await originalApi(url, options);
    if (url === '/api/vps' && options.method === 'POST' && result?.vps?.id && result?.install) {
      localStorage.setItem(`vpspulse:install:${result.vps.id}`, result.install);
    }
    return result;
  };

  const uninstallCommand = "sudo systemctl disable --now vps-pulse-agent 2>/dev/null || true; sudo rm -f /etc/systemd/system/vps-pulse-agent.service /usr/local/bin/vps-pulse-agent; sudo systemctl daemon-reload; echo 'VPS Pulse Agent 已卸载'";

  async function copy(text, button) {
    if (!text) {
      alert('这台 VPS 是旧数据，没有保存安装令牌。请重新添加该 VPS 以生成安装命令。');
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const area = document.createElement('textarea');
      area.value = text;
      document.body.appendChild(area);
      area.select();
      document.execCommand('copy');
      area.remove();
    }
    const old = button.textContent;
    button.textContent = '已复制';
    setTimeout(() => button.textContent = old, 1200);
  }

  function enhanceCards() {
    document.querySelectorAll('.card .actions').forEach(actions => {
      if (actions.querySelector('.agent-install')) return;
      const source = actions.querySelector('[onclick*="changeip"]')?.getAttribute('onclick') || '';
      const id = source.match(/act\('([^']+)'/)?.[1];
      if (!id) return;

      const install = document.createElement('button');
      install.type = 'button';
      install.className = 'ghost agent-install';
      install.textContent = '复制安装 Agent';
      install.onclick = () => copy(localStorage.getItem(`vpspulse:install:${id}`), install);

      const uninstall = document.createElement('button');
      uninstall.type = 'button';
      uninstall.className = 'ghost agent-uninstall';
      uninstall.textContent = '复制卸载 Agent';
      uninstall.onclick = () => copy(uninstallCommand, uninstall);

      actions.append(install, uninstall);
    });
  }

  new MutationObserver(enhanceCards).observe(document.getElementById('cards'), { childList: true, subtree: true });
  enhanceCards();
})();
