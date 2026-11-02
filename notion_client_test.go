package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Hilfsmittel für die Notion-Client-Tests. Alle Namen tragen das Präfix nc,
// damit sie nicht mit den Golden-/App-Tests kollidieren.

// ncSleeps ersetzt sleepFn: nichts wartet wirklich, die gewünschten Dauern
// werden aufgezeichnet. ok bestimmt, ob "das Warten" gelingt (false = Abbruch).
func ncSleeps(t *testing.T, ok bool) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	old := sleepFn
	sleepFn = func(_ context.Context, d time.Duration) bool {
		got = append(got, d)
		return ok
	}
	t.Cleanup(func() { sleepFn = old })
	return &got
}

type ncReply struct {
	status int
	header map[string]string
	body   string
}

// ncServer liefert die Antworten der Reihe nach (die letzte wiederholt sich)
// und merkt sich alle Requests.
type ncServer struct {
	mu      sync.Mutex
	replies []ncReply
	reqs    []*http.Request
	bodies  []string
}

func (s *ncServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	i := len(s.reqs)
	s.reqs = append(s.reqs, r)
	s.bodies = append(s.bodies, string(b))
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	rep := s.replies[i]
	s.mu.Unlock()
	for k, v := range rep.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rep.status)
	_, _ = io.WriteString(w, rep.body)
}

func ncNotion(t *testing.T, replies ...ncReply) (*Notion, *ncServer) {
	t.Helper()
	s := &ncServer{replies: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	n := NewNotion(NotionConfig{BaseURL: srv.URL + "/", APIVersion: "2025-09-03"}, "tok")
	return n, s
}

type ncRoundTrip func(*http.Request) (*http.Response, error)

func (f ncRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func ncEqualDurations(a, b []time.Duration) bool { return slices.Equal(a, b) }

// Fängt: fehlende Obergrenze (30 s) oder falsche Verdopplung.
func TestBackoff(t *testing.T) {
	cases := map[int]time.Duration{
		0: time.Second, 1: 2 * time.Second, 2: 4 * time.Second,
		4: 16 * time.Second, 5: 30 * time.Second, 10: 30 * time.Second,
	}
	for attempt, want := range cases {
		if got := backoff(attempt); got != want {
			t.Errorf("backoff(%d) = %v, erwartet %v", attempt, got, want)
		}
	}
}

// Fängt: falsche Grenzen (0 und >= 120 sind ungültig), Header wird ignoriert
// oder nicht numerischer Header führt zu 0 s Wartezeit.
func TestRetryAfter(t *testing.T) {
	cases := []struct {
		name, header string
		attempt      int
		want         time.Duration
	}{
		{"gültig", "7", 0, 7 * time.Second},
		{"obere Grenze", "119", 0, 119 * time.Second},
		{"kein Header", "", 2, 4 * time.Second},
		{"null", "0", 1, 2 * time.Second},
		{"negativ", "-5", 1, 2 * time.Second},
		{"zu groß", "120", 3, 8 * time.Second},
		{"Datum statt Sekunden", "Wed, 21 Oct 2015 07:28:00 GMT", 0, time.Second},
		{"Müll", "abc", 0, time.Second},
	}
	for _, c := range cases {
		resp := &http.Response{Header: http.Header{}}
		if c.header != "" {
			resp.Header.Set("Retry-After", c.header)
		}
		if got := retryAfter(resp, c.attempt); got != c.want {
			t.Errorf("%s: retryAfter = %v, erwartet %v", c.name, got, c.want)
		}
	}
}

// Fängt: sleepCtx ignoriert den Kontext (würde 1 h blockieren) oder meldet
// bei normalem Ablauf false.
func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("kurzes Warten ohne Abbruch muss true liefern")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if sleepCtx(ctx, time.Hour) {
		t.Error("abgebrochener Kontext muss false liefern")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("sleepCtx hat den Abbruch nicht sofort beachtet")
	}
}

// Fängt: Seam zeigt nicht auf die echte Funktion (Default = heutiges Verhalten).
func TestSleepFnDefaultIsSleepCtx(t *testing.T) {
	// Funktionen sind nicht vergleichbar; Verhalten prüfen.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepFn(ctx, time.Hour) {
		t.Error("Default von sleepFn muss sleepCtx sein (Abbruch -> false)")
	}
}

// Fängt: 429 wird bei nicht-idempotenten Aufrufen nicht wiederholt, Retry-After
// wird nicht genutzt, Payload geht beim Retry verloren.
func TestDo429RetriedEvenWhenNotIdempotent(t *testing.T) {
	sl := ncSleeps(t, true)
	n, s := ncNotion(t,
		ncReply{status: 429, header: map[string]string{"Retry-After": "3"}},
		ncReply{status: 429}, // ohne Header -> backoff(1)
		ncReply{status: 200, body: `{"id":"p1"}`},
	)
	var out struct{ ID string }
	if err := n.do(context.Background(), http.MethodPost, "/v1/pages", map[string]any{"a": 1}, &out, false); err != nil {
		t.Fatalf("do: %v", err)
	}
	if out.ID != "p1" {
		t.Errorf("out.ID = %q", out.ID)
	}
	if len(s.reqs) != 3 {
		t.Fatalf("Requests = %d, erwartet 3", len(s.reqs))
	}
	if want := []time.Duration{3 * time.Second, 2 * time.Second}; !ncEqualDurations(*sl, want) {
		t.Errorf("Wartezeiten = %v, erwartet %v", *sl, want)
	}
	for i, b := range s.bodies {
		if b != `{"a":1}` {
			t.Errorf("Body %d = %q: Payload muss bei jedem Versuch identisch gesendet werden", i, b)
		}
	}
}

// Fängt: Obergrenze der Versuche (attempt < 4 -> genau 5 Requests, 4 Pausen)
// und dass der Endfehler als transient klassifiziert wird.
func TestDo429ExhaustedIsTransient(t *testing.T) {
	sl := ncSleeps(t, true)
	n, s := ncNotion(t, ncReply{status: 429, body: `{"code":"rate_limited","message":"slow down"}`})
	err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true)
	if err == nil || !isTransient(err) {
		t.Fatalf("erwartet transienten Fehler, bekam %v", err)
	}
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 429 || ae.Code != "rate_limited" || ae.Message != "slow down" {
		t.Errorf("apiError = %+v", ae)
	}
	if len(s.reqs) != 5 {
		t.Errorf("Requests = %d, erwartet 5", len(s.reqs))
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}; !ncEqualDurations(*sl, want) {
		t.Errorf("Wartezeiten = %v, erwartet %v", *sl, want)
	}
}

// Fängt: Retry trotz abgebrochenem Kontext (sleep liefert false -> sofort aufgeben).
func TestDoStopsRetryWhenSleepIsCancelled(t *testing.T) {
	sl := ncSleeps(t, false)
	n, s := ncNotion(t, ncReply{status: 429}, ncReply{status: 200})
	err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true)
	if err == nil || !isTransient(err) {
		t.Fatalf("erwartet transienten 429-Fehler, bekam %v", err)
	}
	if len(s.reqs) != 1 || len(*sl) != 1 {
		t.Errorf("Requests = %d, Pausen = %d, erwartet je 1", len(s.reqs), len(*sl))
	}
}

// Fängt: 5xx wird bei idempotenten Aufrufen nicht wiederholt.
func TestDo5xxRetriedWhenIdempotent(t *testing.T) {
	sl := ncSleeps(t, true)
	n, s := ncNotion(t, ncReply{status: 503}, ncReply{status: 502}, ncReply{status: 200, body: `{}`})
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(s.reqs) != 3 || len(*sl) != 2 {
		t.Errorf("Requests = %d, Pausen = %d, erwartet 3 und 2", len(s.reqs), len(*sl))
	}
}

// Fängt: 5xx wird bei nicht-idempotenten Aufrufen wiederholt (doppelte Seiten!).
func TestDo5xxNotRetriedWhenNotIdempotent(t *testing.T) {
	sl := ncSleeps(t, true)
	n, s := ncNotion(t, ncReply{status: 500, body: `{"code":"internal","message":"boom"}`}, ncReply{status: 200})
	err := n.do(context.Background(), http.MethodPost, "/v1/pages", map[string]any{}, nil, false)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 500 || !isTransient(err) {
		t.Fatalf("erwartet transienten 500-Fehler, bekam %v", err)
	}
	if len(s.reqs) != 1 || len(*sl) != 0 {
		t.Errorf("Requests = %d, Pausen = %d, erwartet 1 und 0", len(s.reqs), len(*sl))
	}
}

// Fängt: Netzwerkfehler wird bei nicht-idempotenten Aufrufen wiederholt, bei
// idempotenten gar nicht oder zu oft (attempt < 3 -> 4 Versuche, Pausen 1/2/4 s).
func TestDoNetworkErrorRetryOnlyWhenIdempotent(t *testing.T) {
	for _, idem := range []bool{true, false} {
		sl := ncSleeps(t, true)
		n, _ := ncNotion(t, ncReply{status: 200})
		calls := 0
		n.http.Transport = ncRoundTrip(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("verbindung abgelehnt")
		})
		err := n.do(context.Background(), http.MethodPost, "/x", map[string]any{}, nil, idem)
		if err == nil || !isTransient(err) || !strings.Contains(err.Error(), "Notion nicht erreichbar") {
			t.Fatalf("idem=%v: erwartet transienten Netzwerkfehler, bekam %v", idem, err)
		}
		wantCalls, wantSleeps := 1, []time.Duration(nil)
		if idem {
			wantCalls, wantSleeps = 4, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
		}
		if calls != wantCalls || !ncEqualDurations(*sl, wantSleeps) {
			t.Errorf("idem=%v: Versuche = %d (erwartet %d), Pausen = %v (erwartet %v)", idem, calls, wantCalls, *sl, wantSleeps)
		}
	}
}

// Fängt: Netzwerkfehler-Retry bei abgebrochenem Kontext läuft weiter.
func TestDoNetworkErrorStopsWhenSleepCancelled(t *testing.T) {
	ncSleeps(t, false)
	n, _ := ncNotion(t, ncReply{status: 200})
	calls := 0
	n.http.Transport = ncRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("weg")
	})
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true); err == nil || calls != 1 {
		t.Errorf("err = %v, Versuche = %d, erwartet Fehler nach 1 Versuch", err, calls)
	}
}

// Fängt: falsche Klassifizierung. 401/403/404 sind transient (Datei bleibt
// liegen) mit Hinweistext, 400/409 sind endgültig; keiner wird wiederholt.
func TestDoStatusClassification(t *testing.T) {
	cases := []struct {
		status    int
		transient bool
		hint      bool
	}{
		{400, false, false}, {409, false, false},
		{401, true, true}, {403, true, true}, {404, true, true},
	}
	for _, c := range cases {
		sl := ncSleeps(t, true)
		n, s := ncNotion(t, ncReply{status: c.status, body: `{"code":"c","message":"m"}`})
		err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true)
		var ae *apiError
		if !errors.As(err, &ae) || ae.Status != c.status || ae.Code != "c" || ae.Message != "m" {
			t.Fatalf("%d: apiError = %v", c.status, err)
		}
		if isTransient(err) != c.transient {
			t.Errorf("%d: transient = %v, erwartet %v", c.status, isTransient(err), c.transient)
		}
		if strings.Contains(err.Error(), "Token gültig?") != c.hint {
			t.Errorf("%d: Hinweistext = %v, erwartet %v (%v)", c.status, !c.hint, c.hint, err)
		}
		if len(s.reqs) != 1 || len(*sl) != 0 {
			t.Errorf("%d: Requests = %d, Pausen = %d, erwartet 1 und 0", c.status, len(s.reqs), len(*sl))
		}
	}
}

// Fängt: nicht-JSON-Fehlerkörper führt zu Panic oder verschluckt den Status.
func TestDoErrorBodyNotJSON(t *testing.T) {
	n, _ := ncNotion(t, ncReply{status: 400, body: "<html>bad</html>"})
	err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Code != "" || isTransient(err) {
		t.Errorf("err = %v", err)
	}
}

// Fängt: fehlende/falsche Header (Token, API-Version) und Content-Type auch ohne Body.
func TestDoHeadersAndPath(t *testing.T) {
	n, s := ncNotion(t, ncReply{status: 200, body: `{}`})
	ctx := context.Background()
	if err := n.do(ctx, http.MethodPost, "/v1/pages", map[string]any{"k": "v"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := n.do(ctx, http.MethodGet, "/v1/users/me", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	post, get := s.reqs[0], s.reqs[1]
	if post.URL.Path != "/v1/pages" || get.URL.Path != "/v1/users/me" {
		t.Errorf("Pfade = %q, %q (BaseURL-Slash muss entfernt sein)", post.URL.Path, get.URL.Path)
	}
	for _, r := range s.reqs {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Notion-Version") != "2025-09-03" {
			t.Errorf("Header Authorization/Notion-Version falsch: %v", r.Header)
		}
	}
	if post.Header.Get("Content-Type") != "application/json" || s.bodies[0] != `{"k":"v"}` {
		t.Errorf("POST: Content-Type %q, Body %q", post.Header.Get("Content-Type"), s.bodies[0])
	}
	if get.Header.Get("Content-Type") != "" || s.bodies[1] != "" {
		t.Errorf("GET ohne Body darf kein Content-Type/Body senden")
	}
}

// Fängt: out wird ignoriert, ungültiges JSON in out wird verschluckt,
// Marshal-Fehler schickt trotzdem einen Request.
func TestDoOutputAndInputErrors(t *testing.T) {
	n, s := ncNotion(t, ncReply{status: 200, body: `{"id":"abc"}`}, ncReply{status: 200, body: `kein json`})
	var out struct{ ID string }
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, &out, true); err != nil || out.ID != "abc" {
		t.Errorf("out = %+v, err = %v", out, err)
	}
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, &out, true); err == nil {
		t.Error("ungültiges JSON in der Antwort muss einen Fehler liefern")
	}
	before := len(s.reqs)
	if err := n.do(context.Background(), http.MethodPost, "/x", make(chan int), nil, false); err == nil {
		t.Error("nicht serialisierbarer Payload muss einen Fehler liefern")
	}
	if len(s.reqs) != before {
		t.Error("bei Marshal-Fehler darf kein Request gesendet werden")
	}
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true); err != nil {
		t.Errorf("out == nil darf keinen Fehler liefern: %v", err)
	}
}

// Fängt: ungültige URL (Steuerzeichen) wird nicht als Fehler gemeldet.
func TestDoInvalidURL(t *testing.T) {
	n, _ := ncNotion(t, ncReply{status: 200})
	if err := n.do(context.Background(), http.MethodGet, "/x\x7f", nil, nil, true); err == nil {
		t.Error("ungültige URL muss einen Fehler liefern")
	}
}

// Fängt: Retry-After wird bei 5xx (idempotent) ignoriert; sleepFn bekäme
// dann backoff(0) = 1 s statt 9 s.
func TestDoUsesRetryAfterHeaderFor5xx(t *testing.T) {
	sl := ncSleeps(t, true)
	n, _ := ncNotion(t, ncReply{status: 503, header: map[string]string{"Retry-After": strconv.Itoa(9)}}, ncReply{status: 200})
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{9 * time.Second}; !ncEqualDurations(*sl, want) {
		t.Errorf("Wartezeiten = %v, erwartet %v", *sl, want)
	}
}
