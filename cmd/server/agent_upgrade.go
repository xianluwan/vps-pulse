package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func agentBinaryChecksum() (string, error) {
	file, e := os.Open("/usr/local/bin/agent")
	if e != nil {
		return "", e
	}
	defer file.Close()
	hash := sha256.New()
	if _, e = io.Copy(hash, file); e != nil {
		return "", e
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func (a *App) sendAgentUpgrade(w http.ResponseWriter, id string) {
	if !strings.HasPrefix(env("PUBLIC_URL", ""), "https://") {
		http.Error(w, "Agent 在线升级要求 HTTPS", 409)
		return
	}
	sum, e := agentBinaryChecksum()
	if e != nil {
		http.Error(w, "升级包不可用: "+e.Error(), 500)
		return
	}
	a.mu.RLock()
	conn := a.agents[id]
	a.mu.RUnlock()
	if conn == nil {
		http.Error(w, "Agent 离线", 409)
		return
	}
	payload := map[string]any{"type": "action", "action": "upgrade-agent", "url": strings.TrimRight(env("PUBLIC_URL", ""), "/") + "/downloads/agent", "sha256": sum, "version": version}
	if e = conn.WriteJSON(payload); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	a.addFailoverEvent(id, "agent_upgrade_requested", fmt.Sprintf("已下发 Agent 升级 %s，SHA-256 %s", version, sum[:12]))
	write(w, map[string]bool{"ok": true})
}
