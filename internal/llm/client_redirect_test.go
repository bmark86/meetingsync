package llm

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meetingsync/internal/domain"
)

// zaehlServer zählt eingehende Requests (Ziel einer Weiterleitung bzw. Proxy).
func zaehlServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func rdClient(baseURL string) *Client {
	return NewClient(Options{BaseURL: baseURL, Model: "gemma", Timeout: time.Minute, MaxChunkChars: 100000},
		log.New(io.Discard, "", 0), nil)
}

// Fängt: Transkript folgt einer Weiterleitung an einen anderen Host (S-02, D3).
func TestRedirectNotFollowed(t *testing.T) {
	ziel, hits := zaehlServer(t)
	quelle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ziel.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(quelle.Close)
	l := rdClient(quelle.URL + "/v1")

	_, err := l.Summarize(context.Background(), lcIn("geheimes Transkript"))
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("Summarize: err=%v, want Fehler mit 307", err)
	}
	if domain.IsTransient(err) {
		t.Errorf("3xx darf nicht vorübergehend sein: %v", err)
	}
	if strings.Contains(err.Error(), "geheimes Transkript") {
		t.Errorf("Transkript im Fehler: %v", err)
	}
	_, err = l.Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("Models: err=%v, want Fehler mit 307", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("Weiterleitungsziel hat %d Requests bekommen, want 0", got)
	}
}

// Fängt: Transkript geht über HTTP(S)_PROXY aus der Umgebung (S-02, D3).
func TestNoProxy(t *testing.T) {
	tr, ok := rdClient("http://127.0.0.1:1").http.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("Standard-Transport: %T, Proxy gesetzt=%v, want *http.Transport ohne Proxy", tr, ok && tr.Proxy != nil)
	}
	if tr == http.DefaultTransport {
		t.Error("http.DefaultTransport darf nicht verändert werden")
	}
	if p := http.DefaultTransport.(*http.Transport).Proxy; p == nil {
		t.Error("http.DefaultTransport hat keinen Proxy mehr – wurde das Original verändert?")
	}
}

// Ein injizierter Transport bleibt erhalten (Test-Hooks).
func TestInjectedTransportKept(t *testing.T) {
	rt := http.DefaultTransport
	l := NewClient(Options{}, log.New(io.Discard, "", 0), rt)
	if l.http.Transport != rt {
		t.Fatal("injizierter Transport wurde ersetzt")
	}
	if l.http.CheckRedirect == nil {
		t.Fatal("CheckRedirect fehlt bei injiziertem Transport")
	}
}
