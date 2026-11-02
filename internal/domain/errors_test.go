package domain

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

// httpErr steht für die HTTP-Fehlertypen der Clients (llm.httpError, notion.apiError).
type httpErr struct{ status int }

func (e *httpErr) Error() string { return fmt.Sprintf("HTTP %d", e.status) }

// Fängt: geänderte Klassifizierung. Jeder Fall entspricht einer heutigen
// Aufrufstelle (LLM, Notion, App) bzw. dem Kontext-Abbruch.
func TestIsTransient(t *testing.T) {
	base := errors.New("Grundfehler")
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"einfacher Fehler (Parse-Fehler, leere Datei)", base, false},
		{"eingewickelter einfacher Fehler", fmt.Errorf("Datei: %w", base), false},
		{"HTTP-Fehler ohne Markierung (LLM 400, Notion 400)", &httpErr{400}, false},
		{"Transient (LM Studio nicht erreichbar)", Transient(fmt.Errorf("LM Studio nicht erreichbar: %w", base)), true},
		{"Transient um HTTP-Fehler (429/5xx)", Transient(&httpErr{503}), true},
		{"Transient um %w-Kette (Notion 401/403/404)", Transient(fmt.Errorf("%w – Token gültig?", &httpErr{401})), true},
		{"Transient mit %w eingewickelt", fmt.Errorf("Notion: %w", Transient(base)), true},
		{"Transient doppelt eingewickelt", fmt.Errorf("a: %w", fmt.Errorf("b: %w", Transient(base))), true},
		{"Transient in errors.Join", errors.Join(base, Transient(base)), true},
		{"context.Canceled", context.Canceled, true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"eingewickeltes context.Canceled", fmt.Errorf("Anfrage: %w", context.Canceled), true},
		{"eingewickeltes context.DeadlineExceeded", fmt.Errorf("Anfrage: %w", context.DeadlineExceeded), true},
		{"http.Client-Timeout (url.Error mit DeadlineExceeded)", &url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}, true},
		{"eingewickelter url.Error mit DeadlineExceeded", fmt.Errorf("LLM: %w", &url.Error{Err: context.DeadlineExceeded}), true},
		{"Text ohne %w ist nicht transient", fmt.Errorf("Anfrage: %v", Transient(base)), false},
	}
	for _, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("%s: IsTransient = %v, erwartet %v", c.name, got, c.want)
		}
	}
}

// Fängt: Transient verändert Fehlertext oder verdeckt die Ursache.
func TestTransientKeepsCause(t *testing.T) {
	cause := &httpErr{503}
	err := fmt.Errorf("Notion: %w", Transient(cause))

	if got, want := Transient(cause).Error(), "HTTP 503"; got != want {
		t.Errorf("Error() = %q, erwartet %q", got, want)
	}
	if got, want := err.Error(), "Notion: HTTP 503"; got != want {
		t.Errorf("Error() = %q, erwartet %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is findet die Ursache nicht")
	}
	var he *httpErr
	if !errors.As(err, &he) || he.status != 503 {
		t.Errorf("errors.As = %v, erwartet Status 503", he)
	}
	if errors.Unwrap(Transient(cause)) != cause {
		t.Error("Unwrap liefert nicht die Ursache")
	}
}
