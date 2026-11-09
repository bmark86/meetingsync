package notion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meetingsync/internal/domain"
)

// Fängt: Notiz und Token folgen einer Weiterleitung an einen anderen Server (S-03).
func TestNotionRedirectNotFollowed(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits, mitToken atomic.Int32
			ziel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Header.Get("Authorization") != "" {
					mitToken.Add(1)
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(ziel.Close)
			n, s := ncNotion(t, ncReply{
				status: status,
				header: map[string]string{"Location": ziel.URL + "/v1/pages", "Content-Type": "application/json"},
				body:   `{"code":"geheimer_code","message":"geheimer Inhalt"}`,
			})
			sleeps := ncSleeps(n, true)

			_, _, err := n.CreatePage(context.Background(), "ds", map[string]any{"Name": "geheime Notiz"}, nil)
			if err == nil || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) {
				t.Fatalf("err = %v, want Fehler mit Status %d", err, status)
			}
			if domain.IsTransient(err) {
				t.Errorf("3xx darf nicht vorübergehend sein: %v", err)
			}
			for _, geheim := range []string{"geheimer_code", "geheimer Inhalt", "geheime Notiz", "tok"} {
				if strings.Contains(err.Error(), geheim) {
					t.Errorf("%q im Fehler: %v", geheim, err)
				}
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("Weiterleitungsziel hat %d Requests bekommen (%d mit Token), want 0", got, mitToken.Load())
			}
			if len(s.reqs) != 1 || len(*sleeps) != 0 {
				t.Errorf("Requests an Quelle = %d, Wartezeiten = %v, want 1 Request ohne Wiederholung", len(s.reqs), *sleeps)
			}
		})
	}
}

// Fängt: Redirect-Schutz entfällt, wenn ein Transport injiziert wird.
func TestNotionCheckRedirectSet(t *testing.T) {
	n := NewClient("https://api.notion.com", "v", "tok", http.DefaultTransport)
	if n.http.CheckRedirect == nil || !errors.Is(n.http.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("CheckRedirect muss http.ErrUseLastResponse liefern")
	}
	if n.http.Timeout != 60*time.Second {
		t.Errorf("Timeout = %v, want 60s", n.http.Timeout)
	}
	if n.http.Transport != http.DefaultTransport {
		t.Error("injizierter Transport geht verloren")
	}
}
