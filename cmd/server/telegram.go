package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var telegramLoopOnce sync.Once

type telegramSettings struct {
	Enabled        bool   `json:"enabled"`
	BotToken       string `json:"botToken"`
	AllowedUserID  string `json:"allowedUserId"`
	NotifyAgent    bool   `json:"notifyAgent"`
	NotifyPing     bool   `json:"notifyPing"`
	NotifyIP       bool   `json:"notifyIp"`
	NotifyDNS      bool   `json:"notifyDns"`
	NotifyResource bool   `json:"notifyResource"`
}

func (a *App) ensureTelegram() {
	a.db.Exec(`CREATE TABLE IF NOT EXISTS telegram_settings(id INTEGER PRIMARY KEY CHECK(id=1),enabled INTEGER DEFAULT 0,bot_token TEXT DEFAULT '',allowed_user_id TEXT DEFAULT '',notify_agent INTEGER DEFAULT 1,notify_ping INTEGER DEFAULT 1,notify_ip INTEGER DEFAULT 1,notify_dns INTEGER DEFAULT 1,notify_resource INTEGER DEFAULT 1);INSERT OR IGNORE INTO telegram_settings(id) VALUES(1)`)
}
func (a *App) telegramSettings(w http.ResponseWriter, r *http.Request) {
	a.ensureTelegram()
	if r.Method == "GET" {
		s, e := a.loadTelegram()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		if s.BotToken != "" {
			s.BotToken = "••••••••"
		}
		write(w, s)
		return
	}
	if r.Method != "PUT" && r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	var s telegramSettings
	if json.NewDecoder(r.Body).Decode(&s) != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	old, _ := a.loadTelegram()
	if s.BotToken == "" || s.BotToken == "••••••••" {
		s.BotToken = old.BotToken
	}
	if r.URL.Query().Get("test") == "1" {
		if err := sendTelegram(s.BotToken, s.AllowedUserID, "✅ VPS Pulse Telegram 连接测试成功"); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		write(w, map[string]bool{"ok": true})
		return
	}
	encrypted, e := encryptSecret(s.BotToken)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	_, e = a.db.Exec(`UPDATE telegram_settings SET enabled=?,bot_token=?,allowed_user_id=?,notify_agent=?,notify_ping=?,notify_ip=?,notify_dns=?,notify_resource=? WHERE id=1`, s.Enabled, encrypted, s.AllowedUserID, s.NotifyAgent, s.NotifyPing, s.NotifyIP, s.NotifyDNS, s.NotifyResource)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES('',?,?,?)`, time.Now(), "telegram_settings", "Telegram 设置已保存")
	if s.Enabled {
		a.startTelegramBot()
	}
	write(w, map[string]bool{"ok": true})
}
func (a *App) loadTelegram() (s telegramSettings, e error) {
	a.ensureTelegram()
	var encrypted string
	e = a.db.QueryRow(`SELECT enabled,bot_token,allowed_user_id,notify_agent,notify_ping,notify_ip,notify_dns,notify_resource FROM telegram_settings WHERE id=1`).Scan(&s.Enabled, &encrypted, &s.AllowedUserID, &s.NotifyAgent, &s.NotifyPing, &s.NotifyIP, &s.NotifyDNS, &s.NotifyResource)
	if encrypted != "" {
		s.BotToken, e = decryptSecret(encrypted)
	}
	return
}
func (a *App) telegramNotify(kind, text string) {
	s, e := a.loadTelegram()
	if e != nil || !s.Enabled {
		return
	}
	a.startTelegramBot()
	allowed := map[string]bool{"agent": s.NotifyAgent, "ping": s.NotifyPing, "ip": s.NotifyIP, "dns": s.NotifyDNS, "resource": s.NotifyResource}
	if enabled, ok := allowed[kind]; ok && !enabled {
		return
	}
	go sendTelegram(s.BotToken, s.AllowedUserID, text)
}
func sendTelegram(token, chat, text string) error {
	if token == "" || chat == "" {
		return fmt.Errorf("请填写 Bot Token 和 Telegram User ID")
	}
	if _, e := strconv.ParseInt(chat, 10, 64); e != nil {
		return fmt.Errorf("Telegram User ID 格式错误")
	}
	body, _ := json.Marshal(map[string]string{"chat_id": chat, "text": text})
	client := http.Client{Timeout: 12 * time.Second}
	resp, e := client.Post("https://api.telegram.org/bot"+token+"/sendMessage", "application/json", bytes.NewReader(body))
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Telegram API: %s", strings.TrimSpace(string(b)))
	}
	return nil
}
func secretKey() []byte { k := os.Getenv("MASTER_KEY"); sum := sha256.Sum256([]byte(k)); return sum[:] }
func encryptSecret(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	block, e := aes.NewCipher(secretKey())
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.RawStdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(v), nil)), nil
}
func decryptSecret(v string) (string, error) {
	raw, e := base64.RawStdEncoding.DecodeString(v)
	if e != nil {
		return "", e
	}
	block, e := aes.NewCipher(secretKey())
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	if len(raw) < g.NonceSize() {
		return "", fmt.Errorf("密钥数据损坏")
	}
	plain, e := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	return string(plain), e
}
