package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type serviceMonitor struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	VPSID               string     `json:"vpsId"`
	Type                string     `json:"type"`
	Target              string     `json:"target"`
	IntervalSec         int        `json:"intervalSec"`
	TimeoutSec          int        `json:"timeoutSec"`
	ExpectedStatus      int        `json:"expectedStatus"`
	Keyword             string     `json:"keyword"`
	NotifyAfter         int        `json:"notifyAfter"`
	FailureAction       string     `json:"failureAction"`
	ActionCooldownSec   int        `json:"actionCooldownSec"`
	LastActionAt        *time.Time `json:"lastActionAt,omitempty"`
	Enabled             bool       `json:"enabled"`
	LastStatus          string     `json:"lastStatus"`
	LastLatencyMS       int64      `json:"lastLatencyMs"`
	LastError           string     `json:"lastError"`
	LastChecked         *time.Time `json:"lastChecked,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
}

var monitorLoopOnce sync.Once
var monitorRunning sync.Map

func (a *App) ensureMonitorSchema() {
	_, _ = a.db.Exec(`CREATE TABLE IF NOT EXISTS service_monitors(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,vps_id TEXT DEFAULT '',type TEXT NOT NULL,target TEXT NOT NULL,interval_sec INTEGER DEFAULT 60,timeout_sec INTEGER DEFAULT 5,expected_status INTEGER DEFAULT 200,keyword TEXT DEFAULT '',notify_after INTEGER DEFAULT 3,enabled INTEGER DEFAULT 1,last_status TEXT DEFAULT 'unknown',last_latency_ms INTEGER DEFAULT 0,last_error TEXT DEFAULT '',last_checked DATETIME,consecutive_failures INTEGER DEFAULT 0)`)
	_, _ = a.db.Exec(`ALTER TABLE service_monitors ADD COLUMN failure_action TEXT DEFAULT 'notify'`)
	_, _ = a.db.Exec(`ALTER TABLE service_monitors ADD COLUMN action_cooldown_sec INTEGER DEFAULT 1800`)
	_, _ = a.db.Exec(`ALTER TABLE service_monitors ADD COLUMN last_action_at DATETIME`)
}
func (a *App) startMonitorController() {
	a.ensureMonitorSchema()
	monitorLoopOnce.Do(func() { go a.monitorLoop() })
}
func (a *App) monitorAPI(w http.ResponseWriter, r *http.Request, path []string) {
	a.ensureMonitorSchema()
	if r.Method == http.MethodGet {
		items, e := a.listMonitors()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		write(w, items)
		return
	}
	if r.Method == http.MethodDelete {
		if len(path) == 0 {
			http.Error(w, "缺少监控 ID", 400)
			return
		}
		id, e := strconv.ParseInt(path[0], 10, 64)
		if e != nil {
			http.Error(w, "监控 ID 无效", 400)
			return
		}
		_, e = a.db.Exec(`DELETE FROM service_monitors WHERE id=?`, id)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "method", 405)
		return
	}
	var m serviceMonitor
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&m) != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	m.Type = strings.ToUpper(strings.TrimSpace(m.Type))
	if m.Type != "TCP" && m.Type != "HTTP" && m.Type != "DNS" && m.Type != "TLS" {
		http.Error(w, "监控类型无效", 400)
		return
	}
	if m.Name == "" || m.Target == "" {
		http.Error(w, "名称和目标不能为空", 400)
		return
	}
	if m.IntervalSec < 10 {
		m.IntervalSec = 10
	}
	if m.IntervalSec > 86400 {
		m.IntervalSec = 86400
	}
	if m.TimeoutSec < 1 {
		m.TimeoutSec = 1
	}
	if m.TimeoutSec > 30 {
		m.TimeoutSec = 30
	}
	if m.NotifyAfter < 1 {
		m.NotifyAfter = 1
	}
	if m.FailureAction != "changeip" {
		m.FailureAction = "notify"
	}
	if m.FailureAction == "changeip" && m.VPSID == "" {
		http.Error(w, "触发换 IP 必须关联 VPS", 400)
		return
	}
	if m.ActionCooldownSec < 300 {
		m.ActionCooldownSec = 300
	}
	if m.ActionCooldownSec > 86400 {
		m.ActionCooldownSec = 86400
	}
	if m.ExpectedStatus == 0 {
		m.ExpectedStatus = 200
	}
	if m.ID == 0 {
		res, e := a.db.Exec(`INSERT INTO service_monitors(name,vps_id,type,target,interval_sec,timeout_sec,expected_status,keyword,notify_after,enabled,failure_action,action_cooldown_sec) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, m.Name, m.VPSID, m.Type, m.Target, m.IntervalSec, m.TimeoutSec, m.ExpectedStatus, m.Keyword, m.NotifyAfter, m.Enabled, m.FailureAction, m.ActionCooldownSec)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		m.ID, _ = res.LastInsertId()
	} else {
		_, e := a.db.Exec(`UPDATE service_monitors SET name=?,vps_id=?,type=?,target=?,interval_sec=?,timeout_sec=?,expected_status=?,keyword=?,notify_after=?,enabled=?,failure_action=?,action_cooldown_sec=? WHERE id=?`, m.Name, m.VPSID, m.Type, m.Target, m.IntervalSec, m.TimeoutSec, m.ExpectedStatus, m.Keyword, m.NotifyAfter, m.Enabled, m.FailureAction, m.ActionCooldownSec, m.ID)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
	}
	a.addMonitorEvent(m, "monitor_settings", fmt.Sprintf("服务监控已保存：%s %s", m.Type, m.Target))
	write(w, map[string]any{"ok": true, "id": m.ID})
}
func (a *App) listMonitors() ([]serviceMonitor, error) {
	rows, e := a.db.Query(`SELECT id,name,vps_id,type,target,interval_sec,timeout_sec,expected_status,keyword,notify_after,enabled,last_status,last_latency_ms,last_error,last_checked,consecutive_failures,failure_action,action_cooldown_sec,last_action_at FROM service_monitors ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []serviceMonitor{}
	for rows.Next() {
		var m serviceMonitor
		if e = rows.Scan(&m.ID, &m.Name, &m.VPSID, &m.Type, &m.Target, &m.IntervalSec, &m.TimeoutSec, &m.ExpectedStatus, &m.Keyword, &m.NotifyAfter, &m.Enabled, &m.LastStatus, &m.LastLatencyMS, &m.LastError, &m.LastChecked, &m.ConsecutiveFailures, &m.FailureAction, &m.ActionCooldownSec, &m.LastActionAt); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, nil
}
func (a *App) monitorLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	a.runDueMonitors()
	for range ticker.C {
		a.runDueMonitors()
	}
}
func (a *App) runDueMonitors() {
	items, _ := a.listMonitors()
	now := time.Now()
	sem := make(chan struct{}, 5)
	for _, m := range items {
		if !m.Enabled || (m.LastChecked != nil && now.Sub(*m.LastChecked) < time.Duration(m.IntervalSec)*time.Second) {
			continue
		}
		if _, loaded := monitorRunning.LoadOrStore(m.ID, true); loaded {
			continue
		}
		sem <- struct{}{}
		go func(item serviceMonitor) {
			defer func() { <-sem; monitorRunning.Delete(item.ID) }()
			a.executeMonitor(item)
		}(m)
	}
}
func (a *App) executeMonitor(m serviceMonitor) {
	start := time.Now()
	e := runMonitorCheck(m)
	latency := time.Since(start).Milliseconds()
	now := time.Now()
	oldStatus := m.LastStatus
	failures := m.ConsecutiveFailures
	status := "up"
	message := ""
	if e != nil {
		status = "down"
		message = e.Error()
		failures++
	} else {
		failures = 0
	}
	_, _ = a.db.Exec(`UPDATE service_monitors SET last_status=?,last_latency_ms=?,last_error=?,last_checked=?,consecutive_failures=? WHERE id=?`, status, latency, message, now, failures, m.ID)
	if status == "down" && failures == m.NotifyAfter {
		detail := fmt.Sprintf("监控 %s 连续失败 %d 次：%s", m.Name, failures, message)
		a.addMonitorEvent(m, "monitor_down", detail)
		a.telegramNotify("resource", "[服务故障] "+detail)
		if m.FailureAction == "changeip" {
			a.triggerMonitorChangeIP(m, now)
		}
		go a.activateFailoverForMonitor(m.ID)
	} else if status == "up" && oldStatus == "down" {
		detail := fmt.Sprintf("监控 %s 已恢复，延迟 %dms", m.Name, latency)
		a.addMonitorEvent(m, "monitor_recovered", detail)
		a.telegramNotify("resource", "[服务恢复] "+detail)
		go a.recoverFailoverForMonitor(m.ID)
	} else if status == "up" && oldStatus == "unknown" {
		a.addMonitorEvent(m, "monitor_up", fmt.Sprintf("监控 %s 首次检测成功，延迟 %dms", m.Name, latency))
	}
}
func (a *App) triggerMonitorChangeIP(m serviceMonitor, now time.Time) {
	if m.VPSID == "" {
		return
	}
	if m.LastActionAt != nil && now.Sub(*m.LastActionAt) < time.Duration(m.ActionCooldownSec)*time.Second {
		a.addMonitorEvent(m, "monitor_action_skipped", fmt.Sprintf("监控 %s 已进入换 IP 冷却期，本次跳过", m.Name))
		return
	}
	a.mu.RLock()
	conn := a.agents[m.VPSID]
	a.mu.RUnlock()
	if conn == nil {
		a.addMonitorEvent(m, "monitor_changeip_failed", fmt.Sprintf("监控 %s 触发换 IP 失败：Agent 不在线", m.Name))
		return
	}
	if e := conn.WriteJSON(map[string]any{"type": "action", "action": "changeip"}); e != nil {
		a.addMonitorEvent(m, "monitor_changeip_failed", fmt.Sprintf("监控 %s 触发换 IP 失败：%s", m.Name, e))
		return
	}
	_, _ = a.db.Exec(`UPDATE service_monitors SET last_action_at=? WHERE id=?`, now, m.ID)
	a.addMonitorEvent(m, "monitor_changeip_triggered", fmt.Sprintf("监控 %s 连续失败 %d 次，已向关联 VPS 下发换 IP 命令", m.Name, m.NotifyAfter))
}
func runMonitorCheck(m serviceMonitor) error {
	timeout := time.Duration(m.TimeoutSec) * time.Second
	switch m.Type {
	case "TCP":
		c, e := net.DialTimeout("tcp", m.Target, timeout)
		if e != nil {
			return e
		}
		return c.Close()
	case "HTTP":
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, m.Target, nil)
		if e != nil {
			return e
		}
		client := http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("重定向超过 3 次")
			}
			return nil
		}}
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != m.ExpectedStatus {
			return fmt.Errorf("HTTP 状态码 %d，预期 %d", resp.StatusCode, m.ExpectedStatus)
		}
		if m.Keyword != "" {
			body, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if e != nil {
				return e
			}
			if !strings.Contains(string(body), m.Keyword) {
				return fmt.Errorf("响应中未找到关键字")
			}
		}
		return nil
	case "DNS":
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		ips, e := net.DefaultResolver.LookupHost(ctx, m.Target)
		if e != nil {
			return e
		}
		if m.Keyword != "" {
			for _, ip := range ips {
				if ip == m.Keyword {
					return nil
				}
			}
			return fmt.Errorf("DNS 结果不包含 %s", m.Keyword)
		}
		if len(ips) == 0 {
			return fmt.Errorf("DNS 无解析结果")
		}
		return nil
	case "TLS":
		dialer := &net.Dialer{Timeout: timeout}
		conn, e := tls.DialWithDialer(dialer, "tcp", m.Target, &tls.Config{MinVersion: tls.VersionTLS12})
		if e != nil {
			return e
		}
		defer conn.Close()
		certs := conn.ConnectionState().PeerCertificates
		if len(certs) == 0 {
			return fmt.Errorf("未返回证书")
		}
		days := int(time.Until(certs[0].NotAfter).Hours() / 24)
		threshold := m.ExpectedStatus
		if threshold <= 0 {
			threshold = 14
		}
		if days < threshold {
			return fmt.Errorf("TLS 证书将在 %d 天后过期，阈值 %d 天", days, threshold)
		}
		return nil
	}
	return fmt.Errorf("未知监控类型")
}
func (a *App) addMonitorEvent(m serviceMonitor, typ, detail string) {
	_, _ = a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, m.VPSID, time.Now(), typ, detail)
}
