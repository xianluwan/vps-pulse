package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"log"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Metric struct {
	At                             time.Time `json:"at"`
	CPU, Memory, Disk, Load1       float64
	RxBPS, TxBPS, RxTotal, TxTotal uint64
	IPv4                           string
	AgentVersion                   string
}

var version = "dev"

type VPS struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Token  string `json:"token,omitempty"`
	Online bool   `json:"online"`
	Metric
	DayBytes, MonthBytes, TotalBytes, DayLimit, MonthLimit uint64
	BillingDay                                             int        `json:"billingDay"`
	PingTarget                                             string     `json:"pingTarget"`
	ChangeIPCommand                                        string     `json:"changeIpCommand,omitempty"`
	HasChangeIPCommand                                     bool       `json:"hasChangeIpCommand"`
	AutoRecovery                                           bool       `json:"autoRecovery"`
	CFZoneID                                               string     `json:"cfZoneId,omitempty"`
	CFRecordID                                             string     `json:"cfRecordId,omitempty"`
	CFRecordName                                           string     `json:"cfRecordName,omitempty"`
	CFToken                                                string     `json:"cfToken,omitempty"`
	CPUThreshold                                           float64    `json:"cpuThreshold"`
	MemoryThreshold                                        float64    `json:"memoryThreshold"`
	DiskThreshold                                          float64    `json:"diskThreshold"`
	AlertDurationSec                                       int        `json:"alertDurationSec"`
	MaintenanceUntil                                       *time.Time `json:"maintenanceUntil,omitempty"`
}
type App struct {
	db       *sql.DB
	mu       sync.RWMutex
	live     map[string]Metric
	agents   map[string]*safeWS
	views    map[*safeWS]bool
	up       websocket.Upgrader
	password string
}

func main() {
	db, e := sql.Open("sqlite", env("DATABASE_PATH", "panel.db"))
	if e != nil {
		log.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS vps(id TEXT PRIMARY KEY,name TEXT,token TEXT UNIQUE,billing_day INTEGER DEFAULT 1,day_limit INTEGER DEFAULT 0,month_limit INTEGER DEFAULT 0,ping_target TEXT DEFAULT '',change_cmd TEXT DEFAULT '',cf_zone TEXT DEFAULT '',cf_record TEXT DEFAULT '',cf_name TEXT DEFAULT '',cf_token TEXT DEFAULT '',ipv4 TEXT DEFAULT '',last_rx INTEGER DEFAULT 0,last_tx INTEGER DEFAULT 0,day_key TEXT DEFAULT '',month_key TEXT DEFAULT '',day_bytes INTEGER DEFAULT 0,month_bytes INTEGER DEFAULT 0,total_bytes INTEGER DEFAULT 0); CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,vps_id TEXT,at DATETIME,type TEXT,detail TEXT);`)
	if e != nil {
		log.Fatal(e)
	}
	a := &App{db: db, live: map[string]Metric{}, agents: map[string]*safeWS{}, views: map[*safeWS]bool{}, password: env("ADMIN_PASSWORD", "")}
	if len(a.password) < 12 {
		log.Fatal("ADMIN_PASSWORD must be at least 12 characters")
	}
	a.up = websocket.Upgrader{CheckOrigin: a.checkOrigin, HandshakeTimeout: 10 * time.Second}
	if e = a.ensureSecuritySchema(); e != nil {
		log.Fatal(e)
	}
	a.ensureVPSColumns()
	if e = a.migrateSensitiveData(); e != nil {
		log.Fatal(e)
	}
	a.startFailoverController()
	a.startTelegramBot()
	a.startMonitorController()
	a.startResourceAlertController()
	a.startBackupController()
	m := http.NewServeMux()
	m.HandleFunc("/api/login", a.login)
	m.HandleFunc("/api/logout", a.auth(a.logout))
	m.HandleFunc("/api/vps", a.auth(a.vps))
	m.HandleFunc("/api/vps/", a.auth(a.one))
	m.HandleFunc("/ws/live", a.auth(a.view))
	m.HandleFunc("/agent/ws", a.agent)
	m.HandleFunc("/install-agent.sh", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "install-agent.sh") })
	m.Handle("/", http.FileServer(http.Dir("web")))
	server := &http.Server{Addr: ":8080", Handler: securityHeaders(m), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	log.Println("VPS Pulse listening :8080")
	log.Fatal(server.ListenAndServe())
}
func (a *App) vps(w http.ResponseWriter, r *http.Request) {
	a.startTelegramBot()
	a.startFailoverController()
	a.ensureVPSColumns()
	if r.Method == "POST" {
		var v VPS
		json.NewDecoder(r.Body).Decode(&v)
		v.ID = tok(6)
		v.Token = tok(24)
		if v.BillingDay == 0 {
			v.BillingDay = 1
		}
		storedCommand, e := protectStoredSecret(v.ChangeIPCommand)
		if e != nil {
			http.Error(w, "secret encryption failed", 500)
			return
		}
		storedCFToken, e := protectStoredSecret(v.CFToken)
		if e != nil {
			http.Error(w, "secret encryption failed", 500)
			return
		}
		_, e = a.db.Exec(`INSERT INTO vps(id,name,token,billing_day,day_limit,month_limit,ping_target,change_cmd,auto_recovery,cf_zone,cf_record,cf_name,cf_token,cpu_threshold,memory_threshold,disk_threshold,alert_duration_sec,maintenance_until) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Name, agentTokenHash(v.Token), v.BillingDay, v.DayLimit, v.MonthLimit, v.PingTarget, storedCommand, v.AutoRecovery, v.CFZoneID, v.CFRecordID, v.CFRecordName, storedCFToken, v.CPUThreshold, v.MemoryThreshold, v.DiskThreshold, v.AlertDurationSec, v.MaintenanceUntil)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		write(w, map[string]any{"vps": v, "install": "curl -fsSL " + env("PUBLIC_URL", "http://localhost:8080") + "/install-agent.sh | sudo bash -s -- '" + env("PUBLIC_URL", "http://localhost:8080") + "' '" + v.Token + "'"})
		return
	}
	rows, e := a.db.Query(`SELECT id,name,token,billing_day,day_limit,month_limit,ping_target,change_cmd,auto_recovery,cf_zone,cf_record,cf_name,cf_token,ipv4,day_bytes,month_bytes,total_bytes,cpu_threshold,memory_threshold,disk_threshold,alert_duration_sec,maintenance_until FROM vps`)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	defer rows.Close()
	out := []VPS{}
	for rows.Next() {
		var v VPS
		rows.Scan(&v.ID, &v.Name, &v.Token, &v.BillingDay, &v.DayLimit, &v.MonthLimit, &v.PingTarget, &v.ChangeIPCommand, &v.AutoRecovery, &v.CFZoneID, &v.CFRecordID, &v.CFRecordName, &v.CFToken, &v.IPv4, &v.DayBytes, &v.MonthBytes, &v.TotalBytes, &v.CPUThreshold, &v.MemoryThreshold, &v.DiskThreshold, &v.AlertDurationSec, &v.MaintenanceUntil)
		a.mu.RLock()
		m, ok := a.live[v.ID]
		a.mu.RUnlock()
		if ok {
			v.Metric = m
			v.Online = time.Since(m.At) < 15*time.Second
		}
		v.Token = ""
		v.CFToken = ""
		v.HasChangeIPCommand = v.ChangeIPCommand != ""
		v.ChangeIPCommand = ""
		out = append(out, v)
	}
	write(w, out)
}
func (a *App) one(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/vps/"), "/"), "/")
	id := p[0]
	if id == "" {
		http.Error(w, "缺少 VPS ID", 400)
		return
	}
	if id == "telegram" {
		a.telegramSettings(w, r)
		return
	}
	if id == "failover" {
		a.failoverAPI(w, r, p[1:])
		return
	}
	if id == "monitors" {
		a.monitorAPI(w, r, p[1:])
		return
	}
	if id == "backups" {
		a.backupAPI(w, r, p[1:])
		return
	}
	if id == "logs" {
		if r.Method == "DELETE" {
			if _, e := a.db.Exec(`DELETE FROM events`); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != "GET" {
			http.Error(w, "method", 405)
			return
		}
		_, _ = a.db.Exec(`DELETE FROM events WHERE at < ?`, time.Now().Add(-7*24*time.Hour))
		rows, e := a.db.Query(`SELECT e.id,e.vps_id,COALESCE(v.name,''),e.at,e.type,e.detail FROM events e LEFT JOIN vps v ON v.id=e.vps_id WHERE e.at>=? ORDER BY e.id DESC LIMIT 500`, time.Now().Add(-7*24*time.Hour))
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		defer rows.Close()
		logs := []map[string]any{}
		for rows.Next() {
			var logID int
			var vpsID, name, typ, detail string
			var at time.Time
			if rows.Scan(&logID, &vpsID, &name, &at, &typ, &detail) == nil {
				logs = append(logs, map[string]any{"id": logID, "vpsId": vpsID, "vpsName": name, "at": at, "type": typ, "detail": detail})
			}
		}
		write(w, logs)
		return
	}
	if r.Method == "DELETE" {
		tx, e := a.db.Begin()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		defer tx.Rollback()
		if _, e = tx.Exec(`DELETE FROM events WHERE vps_id=?`, id); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		res, e := tx.Exec(`DELETE FROM vps WHERE id=?`, id)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			http.Error(w, "VPS 不存在", 404)
			return
		}
		if e = tx.Commit(); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		a.mu.Lock()
		if c := a.agents[id]; c != nil {
			_ = c.Close()
		}
		delete(a.agents, id)
		delete(a.live, id)
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(p) > 1 {
		if p[1] == "history" && r.Method == http.MethodGet {
			a.historyAPI(w, r, id)
			return
		}
		if p[1] == "upgrade-agent" && r.Method == http.MethodPost {
			a.sendAgentUpgrade(w, id)
			return
		}
		if p[1] == "dns" {
			a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "action_dns", "面板手动请求更新 Cloudflare DNS")
			if e := a.updateCloudflareDNS(id); e != nil {
				a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "dns_failed", e.Error())
				http.Error(w, e.Error(), 502)
				return
			}
			a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "dns_updated", "Cloudflare A 记录更新成功")
			write(w, map[string]bool{"ok": true})
			return
		}
		actionNames := map[string]string{"ping": "立即 Ping", "changeip": "手动换 IP", "reboot": "重启 VPS"}
		actionName := actionNames[p[1]]
		if actionName == "" {
			actionName = p[1]
		}
		a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "action_"+p[1], "面板请求执行："+actionName)
		a.mu.RLock()
		c := a.agents[id]
		a.mu.RUnlock()
		if c == nil {
			a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "action_failed", actionName+"失败：Agent 离线")
			http.Error(w, "Agent 离线", 409)
			return
		}
		if e := c.WriteJSON(map[string]any{"type": "action", "action": p[1]}); e != nil {
			a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "action_failed", actionName+"发送失败："+e.Error())
			http.Error(w, e.Error(), 500)
			return
		}
		write(w, map[string]bool{"ok": true})
		return
	}
	if r.Method != "PUT" {
		http.Error(w, "method", 405)
		return
	}
	var v VPS
	json.NewDecoder(r.Body).Decode(&v)
	a.ensureVPSColumns()
	storedCommand := ""
	storedCFToken := ""
	var e error
	if v.ChangeIPCommand != "" {
		storedCommand, e = protectStoredSecret(v.ChangeIPCommand)
		if e != nil {
			http.Error(w, "secret encryption failed", 500)
			return
		}
	}
	if v.CFToken != "" {
		storedCFToken, e = protectStoredSecret(v.CFToken)
		if e != nil {
			http.Error(w, "secret encryption failed", 500)
			return
		}
	}
	_, e = a.db.Exec(`UPDATE vps SET name=?,billing_day=?,day_limit=?,month_limit=?,ping_target=?,change_cmd=CASE WHEN ?='' THEN change_cmd ELSE ? END,auto_recovery=?,cf_zone=?,cf_record=?,cf_name=?,cf_token=CASE WHEN ?='' THEN cf_token ELSE ? END,cpu_threshold=?,memory_threshold=?,disk_threshold=?,alert_duration_sec=?,maintenance_until=? WHERE id=?`, v.Name, v.BillingDay, v.DayLimit, v.MonthLimit, v.PingTarget, storedCommand, storedCommand, v.AutoRecovery, v.CFZoneID, v.CFRecordID, v.CFRecordName, storedCFToken, storedCFToken, v.CPUThreshold, v.MemoryThreshold, v.DiskThreshold, v.AlertDurationSec, v.MaintenanceUntil, id)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "settings_updated", fmt.Sprintf("服务器设置已保存；自动恢复=%t；Ping目标=%s", v.AutoRecovery, v.PingTarget))
	a.mu.RLock()
	c := a.agents[id]
	a.mu.RUnlock()
	if c != nil {
		var command string
		_ = a.db.QueryRow(`SELECT change_cmd FROM vps WHERE id=?`, id).Scan(&command)
		command, _ = revealStoredSecret(command)
		c.WriteJSON(map[string]any{"type": "config", "pingTarget": v.PingTarget, "changeIpCommand": command, "autoRecovery": v.AutoRecovery && !maintenanceActive(v.MaintenanceUntil)})
	}
	write(w, map[string]bool{"ok": true})
}
func (a *App) agent(w http.ResponseWriter, r *http.Request) {
	a.startFailoverController()
	a.ensureVPSColumns()
	var id string
	var bill int
	var ping, cmd string
	var autoRecovery bool
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		http.Error(w, "token", 401)
		return
	}
	var maintenanceUntil *time.Time
	e := a.db.QueryRow(`SELECT id,billing_day,ping_target,change_cmd,auto_recovery,maintenance_until FROM vps WHERE token=? OR token=?`, agentTokenHash(token), token).Scan(&id, &bill, &ping, &cmd, &autoRecovery, &maintenanceUntil)
	if e != nil {
		http.Error(w, "token", 401)
		return
	}
	cmd, e = revealStoredSecret(cmd)
	if e != nil {
		http.Error(w, "agent configuration error", 500)
		return
	}
	_, _ = a.db.Exec(`UPDATE vps SET token=? WHERE id=? AND token NOT LIKE 'sha256:%'`, agentTokenHash(token), id)
	raw, e := a.up.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	c := newSafeWS(raw)
	a.mu.Lock()
	a.agents[id] = c
	a.mu.Unlock()
	a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "agent_online", "Agent 已连接并开始上报数据")
	c.WriteJSON(map[string]any{"type": "config", "pingTarget": ping, "changeIpCommand": cmd, "autoRecovery": autoRecovery && !maintenanceActive(maintenanceUntil)})
	defer func() {
		a.mu.Lock()
		delete(a.agents, id)
		a.mu.Unlock()
		c.Close()
		a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "agent_offline", "Agent 连接已断开")
	}()
	for {
		var x struct {
			Type                string  `json:"type"`
			Metric              *Metric `json:"metric"`
			Event, Detail, IPv4 string
		}
		if c.ReadJSON(&x) != nil {
			return
		}
		if x.Metric != nil {
			x.Metric.At = time.Now()
			a.record(id, bill, *x.Metric)
			a.mu.Lock()
			a.live[id] = *x.Metric
			for v := range a.views {
				v.WriteJSON(map[string]any{"type": "metric", "vpsId": id, "metric": x.Metric})
			}
			a.mu.Unlock()
			continue
		}
		a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), x.Event, x.Detail)
		a.handleFailoverAgentEvent(id, x.Event)
		kind := "resource"
		if strings.Contains(x.Event, "ping") {
			kind = "ping"
		} else if strings.Contains(x.Event, "ip_") || strings.Contains(x.Event, "change_ip") {
			kind = "ip"
		} else if strings.Contains(x.Event, "dns") {
			kind = "dns"
		} else if strings.Contains(x.Event, "agent_") {
			kind = "agent"
		}
		var vpsName string
		_ = a.db.QueryRow(`SELECT name FROM vps WHERE id=?`, id).Scan(&vpsName)
		a.telegramNotify(kind, "["+vpsName+"] "+x.Detail)
		if x.Event == "ip_changed" && x.IPv4 != "" {
			a.db.Exec(`UPDATE vps SET ipv4=? WHERE id=?`, x.IPv4, id)
			go func() {
				if err := a.updateCloudflareDNS(id); err != nil {
					a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "dns_failed", err.Error())
				} else {
					a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, id, time.Now(), "dns_updated", "新 IP 连通成功，Cloudflare A 记录已更新")
				}
			}()
		}
	}
}
func (a *App) record(id string, bill int, m Metric) {
	var lr, lt, db, mb, t uint64
	var dk, mk string
	a.db.QueryRow(`SELECT last_rx,last_tx,day_bytes,month_bytes,total_bytes,day_key,month_key FROM vps WHERE id=?`, id).Scan(&lr, &lt, &db, &mb, &t, &dk, &mk)
	delta := uint64(0)
	if m.RxTotal >= lr {
		delta += m.RxTotal - lr
	}
	if m.TxTotal >= lt {
		delta += m.TxTotal - lt
	}
	now := time.Now().In(time.FixedZone("CST", 28800))
	nd := now.Format("2006-01-02")
	anchor := time.Date(now.Year(), now.Month(), bill, 0, 0, 0, 0, now.Location())
	if now.Before(anchor) {
		anchor = anchor.AddDate(0, -1, 0)
	}
	nm := anchor.Format("2006-01-02")
	if dk != nd {
		db = 0
	}
	if mk != nm {
		mb = 0
	}
	a.db.Exec(`UPDATE vps SET ipv4=?,last_rx=?,last_tx=?,day_key=?,month_key=?,day_bytes=?,month_bytes=?,total_bytes=? WHERE id=?`, m.IPv4, m.RxTotal, m.TxTotal, nd, nm, db+delta, mb+delta, t+delta, id)
	a.recordHistory(id, m, delta)
}
func (a *App) view(w http.ResponseWriter, r *http.Request) {
	raw, e := a.up.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	c := newSafeWS(raw)
	a.mu.Lock()
	a.views[c] = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.views, c); a.mu.Unlock() }()
	for {
		if _, _, e := c.ReadMessage(); e != nil {
			return
		}
	}
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func tok(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
