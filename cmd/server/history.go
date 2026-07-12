package main

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

type historyPoint struct {
	At      time.Time `json:"at"`
	CPU     float64   `json:"cpu"`
	Memory  float64   `json:"memory"`
	Disk    float64   `json:"disk"`
	Load1   float64   `json:"load1"`
	RxBytes uint64    `json:"rxBytes"`
	TxBytes uint64    `json:"txBytes"`
}

var historySchemaOnce sync.Once

func (a *App) ensureHistorySchema() {
	historySchemaOnce.Do(func() {
		_, _ = a.db.Exec(`CREATE TABLE IF NOT EXISTS metric_history(vps_id TEXT NOT NULL,minute DATETIME NOT NULL,cpu REAL DEFAULT 0,memory REAL DEFAULT 0,disk REAL DEFAULT 0,load1 REAL DEFAULT 0,rx_bytes INTEGER DEFAULT 0,tx_bytes INTEGER DEFAULT 0,samples INTEGER DEFAULT 0,PRIMARY KEY(vps_id,minute));CREATE INDEX IF NOT EXISTS idx_metric_history_minute ON metric_history(minute)`)
	})
}
func (a *App) recordHistory(id string, m Metric, delta uint64) {
	a.ensureHistorySchema()
	minute := m.At.UTC().Truncate(time.Minute)
	totalBPS := m.RxBPS + m.TxBPS
	rxDelta := uint64(0)
	txDelta := uint64(0)
	if totalBPS > 0 {
		rxDelta = delta * m.RxBPS / totalBPS
		txDelta = delta - rxDelta
	}
	_, _ = a.db.Exec(`INSERT INTO metric_history(vps_id,minute,cpu,memory,disk,load1,rx_bytes,tx_bytes,samples) VALUES(?,?,?,?,?,?,?,?,1) ON CONFLICT(vps_id,minute) DO UPDATE SET cpu=(metric_history.cpu*metric_history.samples+excluded.cpu)/(metric_history.samples+1),memory=(metric_history.memory*metric_history.samples+excluded.memory)/(metric_history.samples+1),disk=excluded.disk,load1=(metric_history.load1*metric_history.samples+excluded.load1)/(metric_history.samples+1),rx_bytes=metric_history.rx_bytes+excluded.rx_bytes,tx_bytes=metric_history.tx_bytes+excluded.tx_bytes,samples=metric_history.samples+1`, id, minute, m.CPU, m.Memory, m.Disk, m.Load1, rxDelta, txDelta)
	if minute.Minute() == 0 && minute.Hour()%6 == 0 {
		_, _ = a.db.Exec(`DELETE FROM metric_history WHERE minute<?`, time.Now().UTC().Add(-7*24*time.Hour))
	}
}
func (a *App) historyAPI(w http.ResponseWriter, r *http.Request, id string) {
	a.ensureHistorySchema()
	hours := 24
	if value, e := strconv.Atoi(r.URL.Query().Get("hours")); e == nil && (value == 1 || value == 24 || value == 168) {
		hours = value
	}
	rows, e := a.db.Query(`SELECT minute,cpu,memory,disk,load1,rx_bytes,tx_bytes FROM metric_history WHERE vps_id=? AND minute>=? ORDER BY minute`, id, time.Now().UTC().Add(-time.Duration(hours)*time.Hour))
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	defer rows.Close()
	points := []historyPoint{}
	for rows.Next() {
		var p historyPoint
		if rows.Scan(&p.At, &p.CPU, &p.Memory, &p.Disk, &p.Load1, &p.RxBytes, &p.TxBytes) == nil {
			points = append(points, p)
		}
	}
	write(w, points)
}
