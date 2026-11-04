package main

// Verdrahtung des LLM-API-Keys (R-M3-15): newEnv holt den Key über
// secret.NewLLM und gibt ihn nur an den Authorization-Header weiter. Die
// Schlüsselbund-Zweige testet internal/secret mit Fake-Runner; hier läuft nie
// security (api_key_keychain_service bleibt leer).

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meetingsync/internal/config"
)

// Fängt: LLM_API_KEY oder llm.api_key erreichen den Header nicht, falsche
// Reihenfolge, Header ohne Key, Key im Log.
func TestNewEnvLLMKey(t *testing.T) {
	tests := []struct {
		name, env, file, want string
	}{
		{name: "LLM_API_KEY gewinnt vor der Config", env: "env-key", file: "file-key", want: "Bearer env-key"},
		{name: "Rückfall auf llm.api_key", file: "file-key", want: "Bearer file-key"},
		{name: "nichts gesetzt: kein Header"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var auth []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				auth = append(auth, r.Header.Values("Authorization")...)
				mu.Unlock()
				_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			}))
			defer srv.Close()
			t.Setenv("NOTION_TOKEN", "t")
			t.Setenv("LLM_API_KEY", tc.env)
			cfg := config.Default()
			cfg.LLM.BaseURL = srv.URL + "/v1"
			cfg.LLM.Model = "m"
			cfg.LLM.APIKey = tc.file
			cfg.LLM.APIKeyKeychainService = ""
			buf := &bytes.Buffer{}
			env, err := newEnv(cfg, time.UTC, log.New(buf, "", 0), false, failingRT{t: t})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.LLM.Models(context.Background()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case tc.want == "" && len(auth) != 0:
				t.Fatalf("Authorization-Header ohne Key gesendet (%d)", len(auth))
			case tc.want != "" && (len(auth) != 1 || auth[0] != tc.want):
				t.Fatalf("Authorization-Header falsch (Anzahl %d)", len(auth))
			}
			for _, k := range []string{"env-key", "file-key"} {
				if strings.Contains(buf.String(), k) {
					t.Fatal("Key im Log")
				}
			}
		})
	}
}
