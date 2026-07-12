package main

import (
	"os"
	"strings"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin, public string
		want           bool
	}{
		{"https://panel.example.com", "https://panel.example.com", true},
		{"https://panel.example.com:443", "https://panel.example.com", false},
		{"http://panel.example.com", "https://panel.example.com", false},
		{"https://evil.example", "https://panel.example.com", false},
	} {
		if got := sameOrigin(tc.origin, tc.public); got != tc.want {
			t.Fatalf("sameOrigin(%q,%q)=%v want %v", tc.origin, tc.public, got, tc.want)
		}
	}
}
func TestAgentTokenHashDoesNotExposeToken(t *testing.T) {
	token := "secret-agent-token"
	hash := agentTokenHash(token)
	if strings.Contains(hash, token) || !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("unsafe token hash: %q", hash)
	}
	if hash != agentTokenHash(token) {
		t.Fatal("token hash must be stable")
	}
}
func TestProtectedSecretRoundTrip(t *testing.T) {
	t.Setenv("MASTER_KEY", "test-master-key-at-least-32-characters")
	value := "curl -H Authorization:Bearer-secret"
	stored, e := protectStoredSecret(value)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(stored, value) || !strings.HasPrefix(stored, "enc:v1:") {
		t.Fatalf("secret was not protected: %q", stored)
	}
	got, e := revealStoredSecret(stored)
	if e != nil || got != value {
		t.Fatalf("round trip got %q, %v", got, e)
	}
}
func TestMain(m *testing.M) { os.Exit(m.Run()) }
