package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type Metric struct {
	At                             time.Time `json:"at"`
	CPU, Memory, Disk, Load1       float64
	RxBPS, TxBPS, RxTotal, TxTotal uint64
	IPv4                           string
	AgentVersion                   string
}

var version = "dev"

type safeConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *safeConn) write(v any) error { c.mu.Lock(); defer c.mu.Unlock(); return c.conn.WriteJSON(v) }

type config struct {
	sync.RWMutex
	pingTarget, changeCommand string
	autoRecovery              bool
}

func (c *config) get() (string, string, bool) {
	c.RLock()
	defer c.RUnlock()
	return c.pingTarget, c.changeCommand, c.autoRecovery
}
func (c *config) set(target, command string, enabled bool) {
	c.Lock()
	c.pingTarget = target
	c.changeCommand = command
	c.autoRecovery = enabled
	c.Unlock()
}

type agent struct {
	ws       *safeConn
	cfg      config
	changeMu sync.Mutex
	stop     chan struct{}
}

func main() {
	server := flag.String("server", "", "面板地址")
	token := flag.String("token", "", "Agent Token")
	tokenFile := flag.String("token-file", "", "Agent Token 文件")
	flag.Parse()
	if *token == "" && *tokenFile != "" {
		if raw, e := os.ReadFile(*tokenFile); e == nil {
			*token = strings.TrimSpace(string(raw))
		}
	}
	if *server == "" || *token == "" {
		panic("必须提供 -server 和 -token")
	}
	url := strings.Replace(strings.TrimRight(*server, "/"), "https://", "wss://", 1)
	url = strings.Replace(url, "http://", "ws://", 1) + "/agent/ws"
	for {
		headers := http.Header{}
		headers.Set("Authorization", "Bearer "+*token)
		conn, _, err := websocket.DefaultDialer.Dial(url, headers)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		a := &agent{ws: &safeConn{conn: conn}, stop: make(chan struct{})}
		a.run()
		conn.Close()
		time.Sleep(3 * time.Second)
	}
}

func (a *agent) run() {
	go a.readCommands()
	go a.monitorConnectivity()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var previousRx, previousTx uint64
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			io, _ := gnet.IOCounters(false)
			if len(io) == 0 {
				continue
			}
			rx, tx := io[0].BytesRecv, io[0].BytesSent
			metric := collectMetric(rx, tx, previousRx, previousTx)
			previousRx, previousTx = rx, tx
			if a.ws.write(map[string]any{"type": "metric", "metric": metric}) != nil {
				return
			}
		}
	}
}

func (a *agent) readCommands() {
	defer close(a.stop)
	for {
		var message map[string]any
		if a.ws.conn.ReadJSON(&message) != nil {
			return
		}
		typ, _ := message["type"].(string)
		if typ == "config" {
			target, _ := message["pingTarget"].(string)
			command, _ := message["changeIpCommand"].(string)
			enabled, _ := message["autoRecovery"].(bool)
			a.cfg.set(strings.TrimSpace(target), strings.TrimSpace(command), enabled)
			a.event("auto_recovery_config", fmt.Sprintf("自动连通性恢复已设置为 %t", enabled), "")
			continue
		}
		if typ != "action" {
			continue
		}
		action, _ := message["action"].(string)
		switch action {
		case "ping":
			go a.manualPing()
		case "changeip":
			go a.changeIP("面板手动触发")
		case "reboot":
			go func() { a.event("reboot", "面板请求重启 VPS", ""); _, _ = shell("reboot", 10*time.Second) }()
		case "upgrade-agent":
			url, _ := message["url"].(string)
			checksum, _ := message["sha256"].(string)
			go a.upgrade(url, checksum)
		case "rollback-agent":
			go a.rollback()
		}
	}
}

func (a *agent) monitorConnectivity() {
	// 等待首份配置，避免刚连接时对空目标进行检测。
	time.Sleep(5 * time.Second)
	failures := 0
	for {
		select {
		case <-a.stop:
			return
		default:
		}
		target, _, enabled := a.cfg.get()
		if !enabled {
			failures = 0
			time.Sleep(30 * time.Second)
			continue
		}
		if target == "" {
			time.Sleep(30 * time.Second)
			continue
		}
		a.event("ping_check_started", "开始自动检测连通性: "+target, "")
		if reachable(target) {
			if failures > 0 {
				a.event("ping_recovered", "连通性已恢复: "+target, "")
			}
			a.event("ping_ok", "自动 Ping 成功，下次检测将在 5 分钟后进行: "+target, "")
			failures = 0
			time.Sleep(5 * time.Minute)
			continue
		}
		failures++
		a.event("ping_failed", fmt.Sprintf("Ping %s 失败（%d/3）", target, failures), "")
		if failures < 3 {
			time.Sleep(2 * time.Minute)
			continue
		}
		failures = 0
		a.changeIP("连续三次 Ping 失败")
		// 一次自动恢复流程结束后进入正常检测周期，防止立即重复换 IP。
		time.Sleep(5 * time.Minute)
	}
}

func (a *agent) manualPing() {
	target, _, _ := a.cfg.get()
	if target == "" {
		a.event("ping_failed", "尚未设置 Ping 目标", "")
		return
	}
	if reachable(target) {
		a.event("ping_ok", "Ping 成功: "+target, "")
	} else {
		a.event("ping_failed", "Ping 失败: "+target, "")
	}
}

func (a *agent) changeIP(reason string) {
	if !a.changeMu.TryLock() {
		a.event("change_ip_skipped", "换 IP 流程正在执行，本次请求已忽略", "")
		return
	}
	defer a.changeMu.Unlock()
	target, command, _ := a.cfg.get()
	if command == "" {
		a.event("change_ip_failed", "未设置换 IP Shell 命令", "")
		return
	}
	oldIP := publicIP(true)
	a.event("change_ip_started", reason+"，开始执行换 IP 命令；旧 IP: "+oldIP, oldIP)
	output, err := shell(command, 30*time.Second)
	if err != nil {
		a.event("change_ip_failed", fmt.Sprintf("命令失败: %v；输出: %s", err, truncate(output, 300)), oldIP)
		return
	}
	a.event("change_ip_command_ok", "换 IP 命令执行成功，开始检测新 IPv4", oldIP)
	for attempt := 1; attempt <= 5; attempt++ {
		time.Sleep(time.Minute)
		newIP := publicIP(true)
		if newIP == "" || newIP == oldIP {
			a.event("ip_check", fmt.Sprintf("第 %d/5 次检测：IP 尚未变化", attempt), newIP)
			continue
		}
		a.event("ip_detected", fmt.Sprintf("检测到新 IP: %s（旧 IP: %s）", newIP, oldIP), newIP)
		if target != "" && !reachable(target) {
			a.event("change_ip_failed", "新 IP 已获取，但 Ping 目标仍不通: "+target, newIP)
			return
		}
		a.event("ip_changed", "新 IP 连通成功，请更新 Cloudflare DNS", newIP)
		return
	}
	a.event("change_ip_failed", "连续检测 5 次仍未发现新 IPv4，已停止自动操作", oldIP)
}

func collectMetric(rx, tx, previousRx, previousTx uint64) *Metric {
	memory, _ := mem.VirtualMemory()
	storage, _ := disk.Usage("/")
	avg, _ := load.Avg()
	percent, _ := cpu.Percent(0, false)
	metric := &Metric{At: time.Now(), Memory: memory.UsedPercent, Disk: storage.UsedPercent, RxTotal: rx, TxTotal: tx, IPv4: publicIP(false), AgentVersion: version}
	if len(percent) > 0 {
		metric.CPU = percent[0]
	}
	if avg != nil {
		metric.Load1 = avg.Load1
	}
	if rx >= previousRx {
		metric.RxBPS = rx - previousRx
	}
	if tx >= previousTx {
		metric.TxBPS = tx - previousTx
	}
	return metric
}

func reachable(target string) bool {
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	args := []string{"-c", "1", "-W", "3", host}
	if runtime.GOOS == "windows" {
		args = []string{"-n", "1", "-w", "3000", host}
	}
	return exec.Command("ping", args...).Run() == nil
}

func shell(command string, timeout time.Duration) (string, error) {
	done := make(chan struct{})
	var output []byte
	var err error
	cmd := exec.Command("/bin/sh", "-c", command)
	go func() { output, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return string(output), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return string(output), fmt.Errorf("执行超时")
	}
}

func (a *agent) event(kind, detail, ip string) {
	_ = a.ws.write(map[string]any{"type": "event", "event": kind, "detail": detail, "IPv4": ip})
}
func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max] + "…"
	}
	return value
}

var ipCache struct {
	sync.Mutex
	value string
	at    time.Time
}

func publicIP(force bool) string {
	ipCache.Lock()
	defer ipCache.Unlock()
	if !force && ipCache.value != "" && time.Since(ipCache.at) < 30*time.Second {
		return ipCache.value
	}
	client := http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ipCache.value
	}
	defer resp.Body.Close()
	var value string
	fmt.Fscan(resp.Body, &value)
	if net.ParseIP(value) != nil {
		ipCache.value = value
		ipCache.at = time.Now()
	}
	return ipCache.value
}

func (a *agent) upgrade(downloadURL, expected string) {
	if !strings.HasPrefix(downloadURL, "https://") {
		a.event("agent_upgrade_failed", "拒绝从非 HTTPS 地址升级", "")
		return
	}
	client := http.Client{Timeout: 90 * time.Second}
	resp, e := client.Get(downloadURL)
	if e != nil {
		a.event("agent_upgrade_failed", e.Error(), "")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		a.event("agent_upgrade_failed", resp.Status, "")
		return
	}
	tmp := "/usr/local/bin/vps-pulse-agent.new"
	file, e := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if e != nil {
		a.event("agent_upgrade_failed", e.Error(), "")
		return
	}
	hash := sha256.New()
	_, e = io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, 100<<20))
	closeErr := file.Close()
	if e != nil || closeErr != nil {
		_ = os.Remove(tmp)
		a.event("agent_upgrade_failed", "写入升级包失败", "")
		return
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		_ = os.Remove(tmp)
		a.event("agent_upgrade_failed", "SHA-256 校验失败", "")
		return
	}
	exe, e := os.Executable()
	if e != nil {
		_ = os.Remove(tmp)
		a.event("agent_upgrade_failed", e.Error(), "")
		return
	}
	backup := exe + ".bak"
	_ = os.Remove(backup)
	if e = os.Rename(exe, backup); e != nil {
		_ = os.Remove(tmp)
		a.event("agent_upgrade_failed", "备份旧版本失败: "+e.Error(), "")
		return
	}
	if e = os.Rename(tmp, exe); e != nil {
		_ = os.Rename(backup, exe)
		a.event("agent_upgrade_failed", "替换程序失败: "+e.Error(), "")
		return
	}
	_ = os.Chmod(exe, 0755)
	a.event("agent_upgrade_ready", "新版本校验通过，正在重启 Agent", "")
	_ = exec.Command("/bin/sh", "-c", "sleep 1; systemctl restart vps-pulse-agent").Start()
}
func (a *agent) rollback() {
	exe, e := os.Executable()
	if e != nil {
		a.event("agent_rollback_failed", e.Error(), "")
		return
	}
	backup := exe + ".bak"
	if _, e = os.Stat(backup); e != nil {
		a.event("agent_rollback_failed", "没有可回滚的旧版本", "")
		return
	}
	failed := exe + ".failed"
	_ = os.Remove(failed)
	if e = os.Rename(exe, failed); e != nil {
		a.event("agent_rollback_failed", e.Error(), "")
		return
	}
	if e = os.Rename(backup, exe); e != nil {
		_ = os.Rename(failed, exe)
		a.event("agent_rollback_failed", e.Error(), "")
		return
	}
	_ = os.Chmod(exe, 0755)
	a.event("agent_rollback_ready", "已恢复上一版，正在重启 Agent", "")
	_ = exec.Command("/bin/sh", "-c", "sleep 1; systemctl restart vps-pulse-agent").Start()
}
