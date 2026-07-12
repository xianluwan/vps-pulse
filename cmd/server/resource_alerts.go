package main

import (
	"fmt"
	"sync"
	"time"
)

var resourceAlertOnce sync.Once

func (a *App) ensureVPSColumns() {
	for _, q := range []string{`ALTER TABLE vps ADD COLUMN auto_recovery INTEGER DEFAULT 0`, `ALTER TABLE vps ADD COLUMN cpu_threshold REAL DEFAULT 90`, `ALTER TABLE vps ADD COLUMN memory_threshold REAL DEFAULT 90`, `ALTER TABLE vps ADD COLUMN disk_threshold REAL DEFAULT 90`, `ALTER TABLE vps ADD COLUMN alert_duration_sec INTEGER DEFAULT 300`, `ALTER TABLE vps ADD COLUMN maintenance_until DATETIME`} {
		_, _ = a.db.Exec(q)
	}
}
func (a *App) startResourceAlertController() {
	a.ensureVPSColumns()
	_, _ = a.db.Exec(`CREATE TABLE IF NOT EXISTS resource_alert_state(vps_id TEXT,metric TEXT,since DATETIME,active INTEGER DEFAULT 0,PRIMARY KEY(vps_id,metric))`)
	resourceAlertOnce.Do(func() { go a.resourceAlertLoop() })
}
func (a *App) resourceAlertLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	a.evaluateResourceAlerts()
	for range ticker.C {
		a.evaluateResourceAlerts()
		a.refreshMaintenanceConfigs()
	}
}
func (a *App) evaluateResourceAlerts() {
	rows, e := a.db.Query(`SELECT id,name,cpu_threshold,memory_threshold,disk_threshold,alert_duration_sec,maintenance_until FROM vps`)
	if e != nil {
		return
	}
	type resourceTarget struct {
		id, name          string
		cpu, memory, disk float64
		duration          int
		maintenanceUntil  *time.Time
	}
	targets := make([]resourceTarget, 0)
	for rows.Next() {
		var target resourceTarget
		if rows.Scan(&target.id, &target.name, &target.cpu, &target.memory, &target.disk, &target.duration, &target.maintenanceUntil) == nil {
			targets = append(targets, target)
		}
	}
	// SQLite is intentionally configured with one connection. Close the result
	// set before evaluateOneResource performs further queries, otherwise the
	// background alert loop can hold the only connection forever and block login.
	_ = rows.Close()
	for _, target := range targets {
		if maintenanceActive(target.maintenanceUntil) {
			continue
		}
		a.mu.RLock()
		m, ok := a.live[target.id]
		a.mu.RUnlock()
		if !ok || time.Since(m.At) > 15*time.Second {
			continue
		}
		if target.duration < 30 {
			target.duration = 30
		}
		a.evaluateOneResource(target.id, target.name, "CPU", m.CPU, target.cpu, target.duration)
		a.evaluateOneResource(target.id, target.name, "内存", m.Memory, target.memory, target.duration)
		a.evaluateOneResource(target.id, target.name, "磁盘", m.Disk, target.disk, target.duration)
	}
}
func (a *App) evaluateOneResource(id, name, metric string, value, threshold float64, duration int) {
	if threshold <= 0 {
		return
	}
	var since *time.Time
	var active bool
	e := a.db.QueryRow(`SELECT since,active FROM resource_alert_state WHERE vps_id=? AND metric=?`, id, metric).Scan(&since, &active)
	if value >= threshold {
		now := time.Now()
		if e != nil || since == nil {
			_, _ = a.db.Exec(`INSERT INTO resource_alert_state(vps_id,metric,since,active) VALUES(?,?,?,0) ON CONFLICT(vps_id,metric) DO UPDATE SET since=excluded.since,active=0`, id, metric, now)
			return
		}
		if !active && time.Since(*since) >= time.Duration(duration)*time.Second {
			detail := fmt.Sprintf("%s %s 持续超过阈值：当前 %.1f%%，阈值 %.1f%%", name, metric, value, threshold)
			_, _ = a.db.Exec(`UPDATE resource_alert_state SET active=1 WHERE vps_id=? AND metric=?`, id, metric)
			a.addFailoverEvent(id, "resource_alert", detail)
			a.telegramNotify("resource", "[资源告警] "+detail)
		}
	} else if active && value < threshold-5 {
		detail := fmt.Sprintf("%s %s 已恢复：当前 %.1f%%", name, metric, value)
		_, _ = a.db.Exec(`DELETE FROM resource_alert_state WHERE vps_id=? AND metric=?`, id, metric)
		a.addFailoverEvent(id, "resource_recovered", detail)
		a.telegramNotify("resource", "[资源恢复] "+detail)
	} else if !active {
		_, _ = a.db.Exec(`DELETE FROM resource_alert_state WHERE vps_id=? AND metric=?`, id, metric)
	}
}
func maintenanceActive(until *time.Time) bool { return until != nil && time.Now().Before(*until) }
func (a *App) isMaintenance(id string) bool {
	var until *time.Time
	if a.db.QueryRow(`SELECT maintenance_until FROM vps WHERE id=?`, id).Scan(&until) != nil {
		return false
	}
	return maintenanceActive(until)
}
func (a *App) refreshMaintenanceConfigs() {
	rows, e := a.db.Query(`SELECT id,ping_target,change_cmd,auto_recovery,maintenance_until FROM vps`)
	if e != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, ping, stored string
		var enabled bool
		var until *time.Time
		if rows.Scan(&id, &ping, &stored, &enabled, &until) != nil {
			continue
		}
		command, _ := revealStoredSecret(stored)
		a.mu.RLock()
		c := a.agents[id]
		a.mu.RUnlock()
		if c != nil {
			_ = c.WriteJSON(map[string]any{"type": "config", "pingTarget": ping, "changeIpCommand": command, "autoRecovery": enabled && !maintenanceActive(until)})
		}
	}
}
