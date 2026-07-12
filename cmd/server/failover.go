package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type failoverRule struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Enabled          bool       `json:"enabled"`
	PrimaryVPSID     string     `json:"primaryVpsId"`
	BackupVPSID      string     `json:"backupVpsId"`
	PrimaryMonitorID int64      `json:"primaryMonitorId"`
	BackupMonitorID  int64      `json:"backupMonitorId"`
	RecordName       string     `json:"recordName"`
	RecordType       string     `json:"recordType"`
	PrimaryTarget    string     `json:"primaryTarget"`
	BackupTarget     string     `json:"backupTarget"`
	CFToken          string     `json:"cfToken,omitempty"`
	Proxied          bool       `json:"proxied"`
	Status           string     `json:"status"`
	LastSwitch       *time.Time `json:"lastSwitch,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
}

var failoverLoopOnce sync.Once
var failoverActionMu sync.Mutex

func (a *App) ensureFailoverSchema() {
	_, _ = a.db.Exec(`CREATE TABLE IF NOT EXISTS failover_rules(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,enabled INTEGER DEFAULT 0,primary_vps_id TEXT NOT NULL,backup_vps_id TEXT NOT NULL,record_name TEXT NOT NULL,record_type TEXT NOT NULL DEFAULT 'A',primary_target TEXT DEFAULT '',backup_target TEXT DEFAULT '',cf_token TEXT NOT NULL,proxied INTEGER DEFAULT 0,status TEXT NOT NULL DEFAULT 'primary',last_switch DATETIME,last_error TEXT DEFAULT '')`)
	_, _ = a.db.Exec(`ALTER TABLE failover_rules ADD COLUMN primary_monitor_id INTEGER DEFAULT 0`)
	_, _ = a.db.Exec(`ALTER TABLE failover_rules ADD COLUMN backup_monitor_id INTEGER DEFAULT 0`)
}
func (a *App) startFailoverController() {
	a.ensureFailoverSchema()
	failoverLoopOnce.Do(func() { go a.failoverRecoveryLoop() })
}

func (a *App) failoverAPI(w http.ResponseWriter, r *http.Request, path []string) {
	a.ensureFailoverSchema()
	if r.Method == http.MethodPost && len(path) >= 2 {
		id, e := strconv.ParseInt(path[0], 10, 64)
		if e != nil {
			http.Error(w, "规则 ID 无效", 400)
			return
		}
		if e = a.manualFailoverAction(id, path[1]); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		write(w, map[string]bool{"ok": true})
		return
	}
	if r.Method == http.MethodGet {
		rules, e := a.listFailoverRules()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		for i := range rules {
			if rules[i].CFToken != "" {
				rules[i].CFToken = "••••••••"
			}
		}
		write(w, rules)
		return
	}
	if r.Method == http.MethodDelete {
		if len(path) == 0 {
			http.Error(w, "缺少规则 ID", 400)
			return
		}
		id, e := strconv.ParseInt(path[0], 10, 64)
		if e != nil {
			http.Error(w, "规则 ID 无效", 400)
			return
		}
		if _, e = a.db.Exec(`DELETE FROM failover_rules WHERE id=?`, id); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "method", 405)
		return
	}
	var rule failoverRule
	if json.NewDecoder(r.Body).Decode(&rule) != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	rule.RecordType = strings.ToUpper(strings.TrimSpace(rule.RecordType))
	rule.RecordName = strings.TrimSuffix(strings.TrimSpace(rule.RecordName), ".")
	if rule.RecordType != "A" && rule.RecordType != "CNAME" {
		http.Error(w, "记录类型只能是 A 或 CNAME", 400)
		return
	}
	if rule.Name == "" || rule.PrimaryVPSID == "" || rule.BackupVPSID == "" || rule.RecordName == "" {
		http.Error(w, "请填写完整规则", 400)
		return
	}
	if rule.PrimaryVPSID == rule.BackupVPSID {
		http.Error(w, "主 VPS 和备用 VPS 不能相同", 400)
		return
	}
	if rule.PrimaryMonitorID > 0 && !a.monitorBelongsTo(rule.PrimaryMonitorID, rule.PrimaryVPSID) {
		http.Error(w, "主服务监控必须关联主 VPS", 400)
		return
	}
	if rule.BackupMonitorID > 0 && !a.monitorBelongsTo(rule.BackupMonitorID, rule.BackupVPSID) {
		http.Error(w, "备用服务监控必须关联备用 VPS", 400)
		return
	}
	if rule.RecordType == "CNAME" && (rule.PrimaryTarget == "" || rule.BackupTarget == "") {
		http.Error(w, "CNAME 需要填写主目标和备用目标", 400)
		return
	}
	oldToken := ""
	if rule.ID > 0 {
		_ = a.db.QueryRow(`SELECT cf_token FROM failover_rules WHERE id=?`, rule.ID).Scan(&oldToken)
	}
	if rule.CFToken == "" || rule.CFToken == "••••••••" {
		rule.CFToken = oldToken
	} else {
		encrypted, e := encryptSecret(rule.CFToken)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		rule.CFToken = encrypted
	}
	if rule.CFToken == "" {
		http.Error(w, "请填写 Cloudflare API Token", 400)
		return
	}
	if rule.ID == 0 {
		res, e := a.db.Exec(`INSERT INTO failover_rules(name,enabled,primary_vps_id,backup_vps_id,primary_monitor_id,backup_monitor_id,record_name,record_type,primary_target,backup_target,cf_token,proxied,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'primary')`, rule.Name, rule.Enabled, rule.PrimaryVPSID, rule.BackupVPSID, rule.PrimaryMonitorID, rule.BackupMonitorID, rule.RecordName, rule.RecordType, rule.PrimaryTarget, rule.BackupTarget, rule.CFToken, rule.Proxied)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		rule.ID, _ = res.LastInsertId()
	} else {
		_, e := a.db.Exec(`UPDATE failover_rules SET name=?,enabled=?,primary_vps_id=?,backup_vps_id=?,primary_monitor_id=?,backup_monitor_id=?,record_name=?,record_type=?,primary_target=?,backup_target=?,cf_token=?,proxied=? WHERE id=?`, rule.Name, rule.Enabled, rule.PrimaryVPSID, rule.BackupVPSID, rule.PrimaryMonitorID, rule.BackupMonitorID, rule.RecordName, rule.RecordType, rule.PrimaryTarget, rule.BackupTarget, rule.CFToken, rule.Proxied, rule.ID)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
	}
	a.addFailoverEvent(rule.PrimaryVPSID, "failover_settings", fmt.Sprintf("容灾规则 %s 已保存；类型=%s；启用=%t", rule.Name, rule.RecordType, rule.Enabled))
	write(w, map[string]any{"ok": true, "id": rule.ID})
}

func (a *App) manualFailoverAction(id int64, action string) error {
	failoverActionMu.Lock()
	defer failoverActionMu.Unlock()
	rules, e := a.listFailoverRules()
	if e != nil {
		return e
	}
	var rule *failoverRule
	for i := range rules {
		if rules[i].ID == id {
			rule = &rules[i]
			break
		}
	}
	if rule == nil {
		return fmt.Errorf("容灾规则不存在")
	}
	switch action {
	case "test":
		if !a.verifyRuleBackup(*rule) {
			return fmt.Errorf("备用 VPS 主动检测失败")
		}
		target, e := a.failoverTarget(*rule, false)
		if e != nil {
			return e
		}
		token, e := decryptSecret(rule.CFToken)
		if e != nil {
			return e
		}
		if _, e = findCFZone(rule.RecordName, token); e != nil {
			return e
		}
		detail := fmt.Sprintf("容灾演练通过：备用目标 %s 可用，Cloudflare 权限正常，未修改 DNS", target)
		a.addFailoverEvent(rule.PrimaryVPSID, "failover_test_ok", detail)
		a.telegramNotify("ip", "[容灾演练] "+detail)
		return nil
	case "switch":
		if !a.verifyRuleBackup(*rule) {
			return fmt.Errorf("备用 VPS 主动检测失败")
		}
		target, e := a.failoverTarget(*rule, false)
		if e != nil {
			return e
		}
		if e = a.updateFailoverRecord(*rule, target); e != nil {
			return e
		}
		_, _ = a.db.Exec(`UPDATE failover_rules SET status='backup',last_switch=?,last_error='' WHERE id=?`, time.Now(), rule.ID)
		detail := fmt.Sprintf("手动容灾：%s 已切换到备用目标 %s", rule.RecordName, target)
		a.addFailoverEvent(rule.PrimaryVPSID, "failover_activated", detail)
		a.telegramNotify("ip", "[手动容灾] "+detail)
		return nil
	case "recover":
		if !a.rulePrimaryHealthy(*rule) {
			return fmt.Errorf("主 VPS 当前不在线，拒绝切回")
		}
		target, e := a.failoverTarget(*rule, true)
		if e != nil {
			return e
		}
		if e = a.updateFailoverRecord(*rule, target); e != nil {
			return e
		}
		_, _ = a.db.Exec(`UPDATE failover_rules SET status='primary',last_switch=?,last_error='' WHERE id=?`, time.Now(), rule.ID)
		detail := fmt.Sprintf("手动恢复：%s 已切回主目标 %s", rule.RecordName, target)
		a.addFailoverEvent(rule.PrimaryVPSID, "failover_recovered", detail)
		a.telegramNotify("ip", "[手动恢复] "+detail)
		return nil
	default:
		return fmt.Errorf("未知容灾动作")
	}
}

func (a *App) listFailoverRules() ([]failoverRule, error) {
	rows, e := a.db.Query(`SELECT id,name,enabled,primary_vps_id,backup_vps_id,primary_monitor_id,backup_monitor_id,record_name,record_type,primary_target,backup_target,cf_token,proxied,status,last_switch,last_error FROM failover_rules ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []failoverRule{}
	for rows.Next() {
		var x failoverRule
		if e = rows.Scan(&x.ID, &x.Name, &x.Enabled, &x.PrimaryVPSID, &x.BackupVPSID, &x.PrimaryMonitorID, &x.BackupMonitorID, &x.RecordName, &x.RecordType, &x.PrimaryTarget, &x.BackupTarget, &x.CFToken, &x.Proxied, &x.Status, &x.LastSwitch, &x.LastError); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, nil
}
func (a *App) monitorBelongsTo(monitorID int64, vpsID string) bool {
	var linked string
	var enabled bool
	return a.db.QueryRow(`SELECT vps_id,enabled FROM service_monitors WHERE id=?`, monitorID).Scan(&linked, &enabled) == nil && enabled && linked == vpsID
}
func (a *App) monitorHealthy(monitorID int64) bool {
	if monitorID <= 0 {
		return false
	}
	var status string
	var checked *time.Time
	var interval int
	if a.db.QueryRow(`SELECT last_status,last_checked,interval_sec FROM service_monitors WHERE id=? AND enabled=1`, monitorID).Scan(&status, &checked, &interval) != nil || checked == nil {
		return false
	}
	maxAge := time.Duration(interval*2+30) * time.Second
	return status == "up" && time.Since(*checked) <= maxAge
}
func (a *App) verifyRuleBackup(rule failoverRule) bool {
	if rule.BackupMonitorID > 0 {
		return a.monitorHealthy(rule.BackupMonitorID)
	}
	return a.verifyBackup(rule.BackupVPSID)
}
func (a *App) rulePrimaryHealthy(rule failoverRule) bool {
	if rule.PrimaryMonitorID > 0 {
		return a.monitorHealthy(rule.PrimaryMonitorID)
	}
	return a.vpsHealthy(rule.PrimaryVPSID)
}
func (a *App) handleFailoverAgentEvent(id, event string) {
	if a.isMaintenance(id) {
		return
	}
	if event == "change_ip_failed" {
		go a.activateFailoverFor(id)
	}
	if event == "ping_ok" || event == "ping_recovered" {
		go a.recoverFailoverFor(id)
	}
}

func (a *App) activateFailoverFor(primaryID string) {
	failoverActionMu.Lock()
	defer failoverActionMu.Unlock()
	rules, _ := a.listFailoverRules()
	for _, rule := range rules {
		if !rule.Enabled || rule.PrimaryVPSID != primaryID || rule.PrimaryMonitorID > 0 || rule.Status == "backup" {
			continue
		}
		a.activateFailoverRule(rule, "主 VPS 自愈失败")
	}
}
func (a *App) activateFailoverForMonitor(monitorID int64) {
	failoverActionMu.Lock()
	defer failoverActionMu.Unlock()
	rules, _ := a.listFailoverRules()
	for _, rule := range rules {
		if rule.Enabled && rule.Status != "backup" && rule.PrimaryMonitorID == monitorID {
			a.activateFailoverRule(rule, "主服务监控连续失败")
		}
	}
}
func (a *App) activateFailoverRule(rule failoverRule, reason string) {
	if !a.verifyRuleBackup(rule) {
		a.failRule(rule, "备用服务监控未通过或备用 VPS 不可用，无法切换")
		return
	}
	target, e := a.failoverTarget(rule, false)
	if e != nil {
		a.failRule(rule, e.Error())
		return
	}
	if e = a.updateFailoverRecord(rule, target); e != nil {
		a.failRule(rule, e.Error())
		return
	}
	now := time.Now()
	_, _ = a.db.Exec(`UPDATE failover_rules SET status='backup',last_switch=?,last_error='' WHERE id=?`, now, rule.ID)
	detail := fmt.Sprintf("%s，Cloudflare %s 记录 %s 已切换到备用目标 %s", reason, rule.RecordType, rule.RecordName, target)
	a.addFailoverEvent(rule.PrimaryVPSID, "failover_activated", detail)
	a.telegramNotify("ip", "[容灾] "+detail)
}
func (a *App) recoverFailoverForMonitor(monitorID int64) {
	rules, _ := a.listFailoverRules()
	for _, rule := range rules {
		if rule.Enabled && rule.Status == "backup" && rule.PrimaryMonitorID == monitorID {
			go a.recoverFailoverFor(rule.PrimaryVPSID)
			return
		}
	}
}
func (a *App) recoverFailoverFor(primaryID string) {
	failoverActionMu.Lock()
	defer failoverActionMu.Unlock()
	rules, _ := a.listFailoverRules()
	for _, rule := range rules {
		if !rule.Enabled || rule.PrimaryVPSID != primaryID || rule.Status != "backup" {
			continue
		}
		if !a.rulePrimaryHealthy(rule) {
			continue
		}
		target, e := a.failoverTarget(rule, true)
		if e != nil {
			a.failRule(rule, e.Error())
			continue
		}
		if e = a.updateFailoverRecord(rule, target); e != nil {
			a.failRule(rule, "恢复主节点失败: "+e.Error())
			continue
		}
		now := time.Now()
		_, _ = a.db.Exec(`UPDATE failover_rules SET status='primary',last_switch=?,last_error='' WHERE id=?`, now, rule.ID)
		detail := fmt.Sprintf("主 VPS 已恢复，Cloudflare %s 记录 %s 已自动切回 %s", rule.RecordType, rule.RecordName, target)
		a.addFailoverEvent(primaryID, "failover_recovered", detail)
		a.telegramNotify("ip", "[容灾恢复] "+detail)
	}
}

func (a *App) failoverRecoveryLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rules, _ := a.listFailoverRules()
		seen := map[string]bool{}
		for _, rule := range rules {
			if !rule.Enabled || rule.Status != "backup" || seen[rule.PrimaryVPSID] {
				continue
			}
			seen[rule.PrimaryVPSID] = true
			if rule.PrimaryMonitorID > 0 {
				a.addFailoverEvent(rule.PrimaryVPSID, "failover_recheck", "容灾期间复检主服务监控")
				if a.monitorHealthy(rule.PrimaryMonitorID) {
					go a.recoverFailoverFor(rule.PrimaryVPSID)
				}
				continue
			}
			a.addFailoverEvent(rule.PrimaryVPSID, "failover_recheck", "容灾期间开始复检主 VPS 连通性")
			a.mu.RLock()
			conn := a.agents[rule.PrimaryVPSID]
			a.mu.RUnlock()
			if conn == nil {
				a.addFailoverEvent(rule.PrimaryVPSID, "failover_recheck_failed", "主 VPS Agent 离线，5 分钟后继续检测")
				continue
			}
			if e := conn.WriteJSON(map[string]any{"type": "action", "action": "ping"}); e != nil {
				a.addFailoverEvent(rule.PrimaryVPSID, "failover_recheck_failed", "复检指令发送失败: "+e.Error())
			}
		}
	}
}
func (a *App) vpsHealthy(id string) bool {
	a.mu.RLock()
	m, ok := a.live[id]
	a.mu.RUnlock()
	return ok && time.Since(m.At) < 15*time.Second && m.IPv4 != ""
}
func (a *App) verifyBackup(id string) bool {
	if !a.vpsHealthy(id) {
		return false
	}
	a.mu.RLock()
	conn := a.agents[id]
	a.mu.RUnlock()
	if conn == nil {
		return false
	}
	var marker int64
	_ = a.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM events`).Scan(&marker)
	a.addFailoverEvent(id, "failover_backup_check", "容灾切换前开始检测备用 VPS 连通性")
	if conn.WriteJSON(map[string]any{"type": "action", "action": "ping"}) != nil {
		return false
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		_ = a.db.QueryRow(`SELECT COUNT(*) FROM events WHERE id>? AND vps_id=? AND type='ping_ok'`, marker, id).Scan(&count)
		if count > 0 {
			a.addFailoverEvent(id, "failover_backup_ready", "备用 VPS 连通性检测成功")
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}
func (a *App) failoverTarget(rule failoverRule, primary bool) (string, error) {
	if rule.RecordType == "CNAME" {
		if primary {
			return strings.TrimSuffix(rule.PrimaryTarget, "."), nil
		}
		return strings.TrimSuffix(rule.BackupTarget, "."), nil
	}
	id := rule.BackupVPSID
	if primary {
		id = rule.PrimaryVPSID
	}
	var ip string
	if e := a.db.QueryRow(`SELECT ipv4 FROM vps WHERE id=?`, id).Scan(&ip); e != nil || ip == "" {
		return "", fmt.Errorf("VPS %s 没有可用公网 IPv4", id)
	}
	return ip, nil
}
func (a *App) updateFailoverRecord(rule failoverRule, target string) error {
	token, e := decryptSecret(rule.CFToken)
	if e != nil {
		return fmt.Errorf("Cloudflare Token 解密失败: %w", e)
	}
	zone, e := findCFZone(rule.RecordName, token)
	if e != nil {
		return e
	}
	record, found, e := findCFRecordType(zone.ID, rule.RecordName, rule.RecordType, token)
	if e != nil {
		return e
	}
	payload := map[string]any{"type": rule.RecordType, "name": rule.RecordName, "content": target, "ttl": 60, "proxied": rule.Proxied}
	method, path := http.MethodPost, "/zones/"+zone.ID+"/dns_records"
	if found {
		method = http.MethodPut
		path += "/" + record.ID
	}
	_, e = cfRequest(method, path, token, payload)
	return e
}
func findCFRecordType(zoneID, name, typ, token string) (cfRecord, bool, error) {
	path := "/zones/" + zoneID + "/dns_records?type=" + url.QueryEscape(typ) + "&name=" + url.QueryEscape(name)
	raw, e := cfRequest(http.MethodGet, path, token, nil)
	if e != nil {
		return cfRecord{}, false, e
	}
	var records []cfRecord
	if e = json.Unmarshal(raw, &records); e != nil {
		return cfRecord{}, false, e
	}
	if len(records) == 0 {
		return cfRecord{}, false, nil
	}
	return records[0], true, nil
}
func (a *App) failRule(rule failoverRule, message string) {
	_, _ = a.db.Exec(`UPDATE failover_rules SET last_error=? WHERE id=?`, message, rule.ID)
	a.addFailoverEvent(rule.PrimaryVPSID, "failover_failed", rule.Name+": "+message)
	a.telegramNotify("ip", "[容灾失败] "+rule.Name+": "+message)
}
func (a *App) addFailoverEvent(vpsID, typ, detail string) {
	_, _ = a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`, vpsID, time.Now(), typ, detail)
}
