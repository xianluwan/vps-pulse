package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type backupInfo struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
	SHA256    string    `json:"sha256"`
}

var backupLoopOnce sync.Once

func (a *App) backupDir() string {
	dbPath := env("DATABASE_PATH", "panel.db")
	absolute, _ := filepath.Abs(dbPath)
	return filepath.Join(filepath.Dir(absolute), "backups")
}
func (a *App) startBackupController() { backupLoopOnce.Do(func() { go a.backupLoop() }) }
func (a *App) backupLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	a.runScheduledBackup()
	for range ticker.C {
		a.runScheduledBackup()
	}
}
func (a *App) runScheduledBackup() {
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	if now.Hour() < 3 {
		return
	}
	dir := a.backupDir()
	_ = os.MkdirAll(dir, 0700)
	marker := filepath.Join(dir, ".last-auto-backup")
	last, _ := os.ReadFile(marker)
	if strings.TrimSpace(string(last)) == now.Format("2006-01-02") {
		return
	}
	if _, e := a.createBackup("auto"); e == nil {
		_ = os.WriteFile(marker, []byte(now.Format("2006-01-02")), 0600)
	}
}
func (a *App) backupAPI(w http.ResponseWriter, r *http.Request, path []string) {
	if r.Method == http.MethodGet {
		if name := r.URL.Query().Get("download"); name != "" {
			a.downloadBackup(w, r, name)
			return
		}
		items, e := a.listBackups()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		write(w, items)
		return
	}
	if r.Method == http.MethodPost {
		item, e := a.createBackup("manual")
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		write(w, item)
		return
	}
	if r.Method == http.MethodDelete {
		if len(path) == 0 {
			http.Error(w, "缺少备份名称", 400)
			return
		}
		name := filepath.Base(path[0])
		if name != path[0] || !strings.HasSuffix(name, ".db") {
			http.Error(w, "备份名称无效", 400)
			return
		}
		if e := os.Remove(filepath.Join(a.backupDir(), name)); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.WriteHeader(204)
		return
	}
	http.Error(w, "method", 405)
}
func (a *App) createBackup(kind string) (backupInfo, error) {
	dir := a.backupDir()
	if e := os.MkdirAll(dir, 0700); e != nil {
		return backupInfo{}, e
	}
	name := fmt.Sprintf("panel-%s-%s.db", kind, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	sqlPath := strings.ReplaceAll(filepath.ToSlash(path), "'", "''")
	if _, e := a.db.Exec(`VACUUM INTO '` + sqlPath + `'`); e != nil {
		return backupInfo{}, e
	}
	_ = os.Chmod(path, 0600)
	item, e := backupFileInfo(path)
	if e != nil {
		return backupInfo{}, e
	}
	a.addFailoverEvent("", "backup_created", fmt.Sprintf("已创建%s备份 %s", map[bool]string{true: "自动", false: "手动"}[kind == "auto"], name))
	a.pruneBackups(7)
	return item, nil
}
func (a *App) listBackups() ([]backupInfo, error) {
	dir := a.backupDir()
	_ = os.MkdirAll(dir, 0700)
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, e
	}
	out := []backupInfo{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		item, e := backupFileInfo(filepath.Join(dir, entry.Name()))
		if e == nil {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func backupFileInfo(path string) (backupInfo, error) {
	file, e := os.Open(path)
	if e != nil {
		return backupInfo{}, e
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil {
		return backupInfo{}, e
	}
	hash := sha256.New()
	if _, e = io.Copy(hash, file); e != nil {
		return backupInfo{}, e
	}
	return backupInfo{Name: info.Name(), Size: info.Size(), CreatedAt: info.ModTime(), SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
func (a *App) pruneBackups(keep int) {
	items, _ := a.listBackups()
	for i := keep; i < len(items); i++ {
		_ = os.Remove(filepath.Join(a.backupDir(), items[i].Name))
	}
}
func (a *App) downloadBackup(w http.ResponseWriter, r *http.Request, name string) {
	safe := filepath.Base(name)
	if safe != name || !strings.HasSuffix(safe, ".db") {
		http.Error(w, "备份名称无效", 400)
		return
	}
	path := filepath.Join(a.backupDir(), safe)
	w.Header().Set("Content-Disposition", `attachment; filename="`+safe+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}
