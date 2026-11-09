package notion

import (
	"context"
	"encoding/json"
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

	"meetingsync/internal/domain"
)

// Hilfsmittel für die Notion-Client-Tests (Präfix nc).

// ncSleeps ersetzt das Warten von n: nichts wartet wirklich, die gewünschten
// Dauern werden aufgezeichnet. ok bestimmt, ob "das Warten" gelingt (false = Abbruch).
func ncSleeps(n *Client, ok bool) *[]time.Duration {
	var got []time.Duration
	n.sleep = func(_ context.Context, d time.Duration) bool {
		got = append(got, d)
		return ok
	}
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

func ncNotion(t *testing.T, replies ...ncReply) (*Client, *ncServer) {
	t.Helper()
	s := &ncServer{replies: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	n := NewClient(srv.URL+"/", "2025-09-03", "tok", nil)
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
func TestClientSleepDefaultIsSleepCtx(t *testing.T) {
	// Funktionen sind nicht vergleichbar; Verhalten prüfen.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if NewClient("http://x", "v", "t", nil).sleep(ctx, time.Hour) {
		t.Error("Default von sleep muss sleepCtx sein (Abbruch -> false)")
	}
}

// Fängt: 429 wird bei nicht-idempotenten Aufrufen nicht wiederholt, Retry-After
// wird nicht genutzt, Payload geht beim Retry verloren.
func TestDo429RetriedEvenWhenNotIdempotent(t *testing.T) {
	n, s := ncNotion(t,
		ncReply{status: 429, header: map[string]string{"Retry-After": "3"}},
		ncReply{status: 429}, // ohne Header -> backoff(1)
		ncReply{status: 200, body: `{"id":"p1"}`},
	)
	sl := ncSleeps(n, true)
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
	n, s := ncNotion(t, ncReply{status: 429, body: `{"code":"rate_limited","message":"slow down"}`})
	sl := ncSleeps(n, true)
	err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true)
	if err == nil || !domain.IsTransient(err) {
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
	n, s := ncNotion(t, ncReply{status: 429}, ncReply{status: 200})
	sl := ncSleeps(n, false)
	err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true)
	if err == nil || !domain.IsTransient(err) {
		t.Fatalf("erwartet transienten 429-Fehler, bekam %v", err)
	}
	if len(s.reqs) != 1 || len(*sl) != 1 {
		t.Errorf("Requests = %d, Pausen = %d, erwartet je 1", len(s.reqs), len(*sl))
	}
}

// Fängt: 5xx wird bei idempotenten Aufrufen nicht wiederholt.
func TestDo5xxRetriedWhenIdempotent(t *testing.T) {
	n, s := ncNotion(t, ncReply{status: 503}, ncReply{status: 502}, ncReply{status: 200, body: `{}`})
	sl := ncSleeps(n, true)
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(s.reqs) != 3 || len(*sl) != 2 {
		t.Errorf("Requests = %d, Pausen = %d, erwartet 3 und 2", len(s.reqs), len(*sl))
	}
}

// Fängt: 5xx wird bei nicht-idempotenten Aufrufen wiederholt (doppelte Seiten!).
func TestDo5xxNotRetriedWhenNotIdempotent(t *testing.T) {
	n, s := ncNotion(t, ncReply{status: 500, body: `{"code":"internal","message":"boom"}`}, ncReply{status: 200})
	sl := ncSleeps(n, true)
	err := n.do(context.Background(), http.MethodPost, "/v1/pages", map[string]any{}, nil, false)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 500 || !domain.IsTransient(err) {
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
		n, _ := ncNotion(t, ncReply{status: 200})
		sl := ncSleeps(n, true)
		calls := 0
		n.http.Transport = ncRoundTrip(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("verbindung abgelehnt")
		})
		err := n.do(context.Background(), http.MethodPost, "/x", map[string]any{}, nil, idem)
		if err == nil || !domain.IsTransient(err) || !strings.Contains(err.Error(), "Notion nicht erreichbar") {
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
	n, _ := ncNotion(t, ncReply{status: 200})
	ncSleeps(n, false)
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
		n, s := ncNotion(t, ncReply{status: c.status, body: `{"code":"c","message":"m"}`})
		sl := ncSleeps(n, true)
		err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true)
		var ae *apiError
		if !errors.As(err, &ae) || ae.Status != c.status || ae.Code != "c" || ae.Message != "m" {
			t.Fatalf("%d: apiError = %v", c.status, err)
		}
		if domain.IsTransient(err) != c.transient {
			t.Errorf("%d: transient = %v, erwartet %v", c.status, domain.IsTransient(err), c.transient)
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
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Code != "" || domain.IsTransient(err) {
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

// Fängt: Retry-After wird bei 5xx (idempotent) ignoriert; sleep bekäme
// dann backoff(0) = 1 s statt 9 s.
func TestDoUsesRetryAfterHeaderFor5xx(t *testing.T) {
	n, _ := ncNotion(t, ncReply{status: 503, header: map[string]string{"Retry-After": strconv.Itoa(9)}}, ncReply{status: 200})
	sl := ncSleeps(n, true)
	if err := n.do(context.Background(), http.MethodGet, "/x", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{9 * time.Second}; !ncEqualDurations(*sl, want) {
		t.Errorf("Wartezeiten = %v, erwartet %v", *sl, want)
	}
}

// Fängt (Mutation: CreatePage liefert bei Anhänge-Fehler leere ID/URL oder nil):
// der Aufrufer kann die angelegte Seite nicht von einem Fehlschlag unterscheiden.
func TestCreatePageReturnsAppendError(t *testing.T) {
	var gotPatch int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			gotPatch++
			http.Error(w, `{"code":"validation_error","message":"x"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "p1", "url": "https://notion.so/p1"})
	}))
	defer srv.Close()
	n := NewClient(srv.URL, "2025-09-03", "tok", nil)
	blocks := make([]any, 150)
	for i := range blocks {
		blocks[i] = map[string]any{"type": "divider", "divider": map[string]any{}}
	}
	id, url, err := n.CreatePage(context.Background(), "ds", map[string]any{}, blocks)
	if id != "p1" || url != "https://notion.so/p1" || !errors.Is(err, ErrUnvollstaendig) || gotPatch != 1 {
		t.Fatalf("CreatePage = %q, %q, %v (PATCH %d); want p1, url, ErrUnvollstaendig, 1", id, url, err, gotPatch)
	}
	if id, _, err := n.CreatePage(context.Background(), "ds", map[string]any{}, blocks[:100]); id != "p1" || err != nil {
		t.Fatalf("ohne Rest: %q, %v; want p1, nil", id, err)
	}
}

// Fängt: fehlendes Antwortlimit, Body-Inhalt im Fehler, ErrUnklar nur bei
// CreatePage, falsche Einstufung bei Statusfehlern mit großem Body.
func TestNotionResponseLimit(t *testing.T) {
	big := strings.Repeat("x", 200)
	ctx := context.Background()

	// 2xx zu groß: GET ist ein normaler Fehler, kein ErrUnklar
	n, _ := ncNotion(t, ncReply{status: 200, body: big})
	n.maxBody = 100
	err := n.do(ctx, http.MethodGet, "/v1/x", nil, &struct{}{}, true)
	if !errors.Is(err, errAntwort) || errors.Is(err, ErrUnklar) || strings.Contains(err.Error(), "xxx") || domain.IsTransient(err) {
		t.Errorf("GET 2xx zu groß: %v", err)
	}
	// CreatePage: ErrUnklar
	n, s := ncNotion(t, ncReply{status: 200, body: big})
	n.maxBody = 100
	_, _, err = n.CreatePage(ctx, "ds", map[string]any{}, nil)
	if !errors.Is(err, ErrUnklar) || errors.Is(err, ErrUnvollstaendig) || domain.IsTransient(err) || strings.Contains(err.Error(), "xxx") {
		t.Errorf("CreatePage 2xx zu groß: %v", err)
	}
	if len(s.reqs) != 1 {
		t.Errorf("CreatePage darf nicht wiederholt werden, Requests: %d", len(s.reqs))
	}
	// Fehlerstatus mit zu großem Body: Status bleibt maßgeblich
	n, _ = ncNotion(t, ncReply{status: 404, body: big})
	n.maxBody = 100
	_, _, err = n.CreatePage(ctx, "ds", map[string]any{}, nil)
	if err == nil || !domain.IsTransient(err) || errors.Is(err, ErrUnklar) || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404 zu groß: %v", err)
	}
	n, _ = ncNotion(t, ncReply{status: 400, body: big})
	n.maxBody = 100
	_, _, err = n.CreatePage(ctx, "ds", map[string]any{}, nil)
	if err == nil || domain.IsTransient(err) || errors.Is(err, ErrUnklar) || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("400 zu groß: %v", err)
	}
	// genau am Limit ist erlaubt
	body := `{"id":"p","url":"u"}`
	n, _ = ncNotion(t, ncReply{status: 200, body: body})
	n.maxBody = int64(len(body))
	if id, _, err := n.CreatePage(ctx, "ds", map[string]any{}, nil); err != nil || id != "p" {
		t.Errorf("am Limit: %q, %v", id, err)
	}
}

// Fängt: abgeschnittene 2xx-Antwort wird als Erfolg oder JSON-Fehler gewertet
// statt als unklarer Ausgang beim Anlegen.
func TestNotionTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"id":"p"`)
	}))
	defer srv.Close()
	n := NewClient(srv.URL, "2025-09-03", "tok", nil)
	n.sleep = func(context.Context, time.Duration) bool { return false }
	_, _, err := n.CreatePage(context.Background(), "ds", map[string]any{}, nil)
	if !errors.Is(err, ErrUnklar) || !errors.Is(err, errAntwort) {
		t.Errorf("CreatePage: %v", err)
	}
	err = n.do(context.Background(), http.MethodGet, "/v1/x", nil, &struct{}{}, true)
	if !errors.Is(err, errAntwort) || errors.Is(err, ErrUnklar) {
		t.Errorf("GET: %v", err)
	}
}

// Fängt: unklarer Ausgang beim Anlegen (Seite evtl. angelegt) nicht als ErrUnklar
// markiert, eindeutige Fehler als unklar, Text oder Fehlerklasse verändert (R-M3-17).
func TestCreatePageAmbiguousErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		rep       ncReply
		netErr    bool
		unklar    bool
		transient bool
		text      string
	}{
		{name: "500", rep: ncReply{status: 500, body: `{"code":"internal","message":"boom"}`}, unklar: true, transient: true, text: "Notion HTTP 500 internal: boom"},
		{name: "503", rep: ncReply{status: 503}, unklar: true, transient: true, text: "Notion HTTP 503"},
		{name: "Netzwerk", netErr: true, unklar: true, transient: true, text: "Notion nicht erreichbar"},
		{name: "kein JSON", rep: ncReply{status: 200, body: "<html>ok</html>"}, unklar: true},
		{name: "falscher Typ", rep: ncReply{status: 200, body: `{"id":1}`}, unklar: true},
		{name: "429", rep: ncReply{status: 429}, transient: true, text: "HTTP 429"},
		{name: "400", rep: ncReply{status: 400, body: `{"code":"validation_error","message":"x"}`}, text: "HTTP 400"},
		{name: "404", rep: ncReply{status: 404}, transient: true, text: "HTTP 404"},
		{name: "307", rep: ncReply{status: 307}, text: "Weiterleitung"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, s := ncNotion(t, c.rep)
			ncSleeps(n, false)
			if c.netErr {
				n.http.Transport = ncRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("verbindung abgelehnt") })
			}
			_, _, err := n.CreatePage(ctx, "ds", map[string]any{}, nil)
			if err == nil || errors.Is(err, ErrUnklar) != c.unklar || domain.IsTransient(err) != c.transient || !strings.Contains(err.Error(), c.text) {
				t.Fatalf("err = %v: unklar %v (want %v), transient %v (want %v)", err, errors.Is(err, ErrUnklar), c.unklar, domain.IsTransient(err), c.transient)
			}
			if c.unklar && !c.netErr && strings.Contains(err.Error(), ErrUnklar.Error()) {
				t.Errorf("Text verändert: %v", err)
			}
			if !c.netErr && len(s.reqs) != 1 {
				t.Errorf("Requests = %d, want 1", len(s.reqs))
			}
		})
	}
}

// Fängt: falscher Filter (Spalte, nicht equals), älteste nicht zuerst, Ergebnis
// falsch gelesen, ID nicht maskiert (R-M3-17).
func TestFindByText(t *testing.T) {
	n, s := ncNotion(t, ncReply{status: 200, body: `{"results":[{"id":"a","url":"https://notion.so/a"},{"id":"b","url":"https://notion.so/b"}]}`})
	got, err := n.FindByText(context.Background(), "d/s", "Quelle", "a.srt · 2026-11-04 08:45:12")
	if err != nil || len(got) != 2 || got[0] != (PageRef{ID: "a", URL: "https://notion.so/a"}) || got[1].ID != "b" {
		t.Fatalf("FindByText = %+v, %v", got, err)
	}
	if r := s.reqs[0]; r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/data_sources/d%2Fs/query" {
		t.Errorf("Request %s %s", r.Method, r.URL.EscapedPath())
	}
	want := `{"filter":{"property":"Quelle","rich_text":{"equals":"a.srt · 2026-11-04 08:45:12"}},"page_size":10,"sorts":[{"direction":"ascending","timestamp":"created_time"}]}`
	if s.bodies[0] != want {
		t.Errorf("Body:\n%s\nerwartet\n%s", s.bodies[0], want)
	}
	n, _ = ncNotion(t, ncReply{status: 200, body: `{"results":[]}`})
	if got, err := n.FindByText(context.Background(), "ds", "Quelle", "x"); err != nil || len(got) != 0 {
		t.Errorf("leer: %+v, %v", got, err)
	}
	n, _ = ncNotion(t, ncReply{status: 502})
	ncSleeps(n, false)
	if _, err := n.FindByText(context.Background(), "ds", "Quelle", "x"); err == nil || !domain.IsTransient(err) {
		t.Errorf("502: %v, want vorübergehend", err)
	}
}

// Fängt: AppendRest hängt die ersten 100 Blöcke doppelt an, fragt ohne Rest an
// oder meldet einen Fehler ohne ErrUnvollstaendig (R-M3-17).
func TestAppendRest(t *testing.T) {
	blocks := make([]any, 150)
	for i := range blocks {
		blocks[i] = map[string]any{"type": "divider", "i": i}
	}
	n, s := ncNotion(t, ncReply{status: 200, body: `{}`})
	if err := n.AppendRest(context.Background(), "p1", blocks[:100]); err != nil || len(s.reqs) != 0 {
		t.Fatalf("ohne Rest: %v, Requests %d", err, len(s.reqs))
	}
	if err := n.AppendRest(context.Background(), "p1", blocks); err != nil || len(s.reqs) != 1 {
		t.Fatalf("Rest: %v, Requests %d", err, len(s.reqs))
	}
	var body struct {
		Children []map[string]any `json:"children"`
	}
	if err := json.Unmarshal([]byte(s.bodies[0]), &body); err != nil || len(body.Children) != 50 || body.Children[0]["i"] != 100.0 {
		t.Errorf("PATCH %s: %d Blöcke, erster %v", s.reqs[0].URL.Path, len(body.Children), body.Children[0]["i"])
	}
	if s.reqs[0].Method != http.MethodPatch || s.reqs[0].URL.Path != "/v1/blocks/p1/children" {
		t.Errorf("Request %s %s", s.reqs[0].Method, s.reqs[0].URL.Path)
	}
	n, _ = ncNotion(t, ncReply{status: 400, body: `{"code":"validation_error","message":"x"}`})
	if err := n.AppendRest(context.Background(), "p1", blocks); !errors.Is(err, ErrUnvollstaendig) {
		t.Errorf("Fehler: %v, want ErrUnvollstaendig", err)
	}
}

// Fängt: ID mit ../, ?, # oder / landet unmaskiert im Pfad und trifft einen anderen Endpunkt (S-16).
func TestIDsAreEscapedInPath(t *testing.T) {
	const bad = "a/../b?x=1#f%41"
	const esc = "a%2F..%2Fb%3Fx=1%23f%2541"
	ok := ncReply{status: 200, header: map[string]string{"Content-Type": "application/json"}, body: `{"properties":{},"results":[],"data_sources":[{"id":"` + bad + `"}],"id":"` + bad + `","url":"u"}`}
	cases := map[string]struct {
		call func(n *Client) error
		want string
	}{
		"data_sources": {func(n *Client) error { _, err := n.dataSourceProps(context.Background(), bad); return err }, "/v1/data_sources/" + esc},
		"query": {func(n *Client) error {
			_, err := n.ActiveProjects(context.Background(), bad, ProjectsProps{}, Schema{})
			return err
		}, "/v1/data_sources/" + esc + "/query"},
		"children":  {func(n *Client) error { return n.appendChildren(context.Background(), bad, []any{map[string]any{}}) }, "/v1/blocks/" + esc + "/children"},
		"databases": {func(n *Client) error { _, _, err := n.ResolveDataSource(context.Background(), bad); return err }, "/v1/databases/" + esc},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			n, s := ncNotion(t, ncReply{status: 404, body: `{"code":"object_not_found"}`}, ok)
			if name != "databases" {
				n, s = ncNotion(t, ok)
			}
			_ = c.call(n)
			found := false
			for _, r := range s.reqs {
				if r.URL.EscapedPath() == c.want && r.URL.RawQuery == "" && r.URL.Fragment == "" {
					found = true
				}
				if strings.Contains(r.URL.EscapedPath(), "/../") || !strings.HasPrefix(r.URL.EscapedPath(), "/v1/") {
					t.Errorf("unerwarteter Pfad %q", r.URL.EscapedPath())
				}
			}
			if !found {
				t.Errorf("kein Request mit Pfad %q", c.want)
			}
		})
	}
}

// Fängt: gültige UUIDs (mit/ohne Bindestriche) werden verändert.
func TestValidUUIDPathUnchanged(t *testing.T) {
	for _, id := range []string{"1a2b3c4d-1111-2222-3333-444455556666", "1a2b3c4d111122223333444455556666"} {
		n, s := ncNotion(t, ncReply{status: 200, body: `{"properties":{}}`})
		_, _ = n.dataSourceProps(context.Background(), id)
		if got := s.reqs[0].URL.EscapedPath(); got != "/v1/data_sources/"+id {
			t.Errorf("Pfad = %q", got)
		}
	}
}
