// Package notion schreibt Meeting-Notizen und To-dos nach Notion: HTTP-Client
// mit Retry, Property-Typen (PropertyKind) und Seitenblöcke.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"meetingsync/internal/domain"
)

// Client ist ein schlanker Client für die Notion-API (Version 2025-09-03,
// Datenbanken werden über ihre Data Sources angesprochen).
type Client struct {
	base    string
	version string
	token   string
	http    *http.Client
	sleep   func(ctx context.Context, d time.Duration) bool // Warten zwischen Versuchen; Tests setzen sie
	maxBody int64                                           // Lese-Limit je Antwort; Tests setzen es kleiner
}

// maxAntwortBytes begrenzt, wie viel von einer Antwort gelesen wird. Größte
// echte Antwort ist eine Query-Seite mit 100 Seiten samt Properties (einige
// hundert KiB); 10 MiB liegt weit darüber und schützt trotzdem vor einem
// Server, der den Speicher füllt.
const maxAntwortBytes = 10 << 20

// errAntwort heißt: die Antwort war nicht vollständig lesbar (Lesefehler oder
// über dem Limit). Der Text enthält nie Inhalt der Antwort.
var errAntwort = errors.New("Antwort von Notion unvollständig oder zu groß")

// ErrUnklar heißt: unklar, ob Notion die Seite angelegt hat, weil die
// Antwort auf das Anlegen nicht vollständig gelesen werden konnte. CreatePage
// umhüllt den Fehler damit; ein neuer Versuch könnte eine Dublette erzeugen.
var ErrUnklar = errors.New("Seite evtl. angelegt")

// NewClient baut den Client. transport ist der HTTP-Transport; nil heißt
// http.DefaultTransport (Produktion), Tests setzen eigene Hooks.
// Weiterleitungen werden nie verfolgt: Ein 307/308 würde den Inhalt der
// Notiz an das Ziel erneut senden (S-03). Der Proxy aus der Umgebung bleibt
// erlaubt (Firmen-Proxy), das Token geht nur über https.
func NewClient(baseURL, apiVersion, token string, transport http.RoundTripper) *Client {
	return &Client{
		base:    strings.TrimRight(baseURL, "/"),
		version: apiVersion,
		token:   token,
		http: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		sleep:   sleepCtx,
		maxBody: maxAntwortBytes,
	}
}

// PageWriter legt Seiten an; *Client erfüllt es. Damit lässt sich der Ablauf
// später ohne HTTP testen (R-M2-09).
type PageWriter interface {
	CreatePage(ctx context.Context, dsID string, props map[string]any, children []any) (string, string, error)
}

var _ PageWriter = (*Client)(nil)

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Notion HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// do schickt einen Request. Rate-Limits (429) werden immer wiederholt,
// Netzwerk- und Serverfehler nur bei idempotenten Aufrufen, damit beim
// Anlegen keine doppelten Seiten entstehen.
// IDs aus Config und API-Antworten gehen nur über url.PathEscape in den Pfad (S-16).
func (n *Client) do(ctx context.Context, method, path string, in, out any, idempotent bool) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, n.base+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+n.token)
		req.Header.Set("Notion-Version", n.version)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := n.http.Do(req)
		if err != nil {
			if idempotent && attempt < 3 && n.sleep(ctx, backoff(attempt)) {
				continue
			}
			return domain.Transient(fmt.Errorf("Notion nicht erreichbar: %w", err))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, n.maxBody+1))
		_ = resp.Body.Close() //nolint:errcheck // Body ist bereits gelesen, Close-Fehler ohne Folgen
		if readErr == nil && int64(len(body)) > n.maxBody {
			readErr = errors.New("über dem Größenlimit")
		}
		if readErr != nil {
			body = nil // bei Statusfehlern zählt nur der Status, Details entfallen
		}

		if resp.StatusCode == http.StatusTooManyRequests ||
			(idempotent && resp.StatusCode >= 500) {
			if attempt < 4 && n.sleep(ctx, retryAfter(resp, attempt)) {
				continue
			}
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			// Weiterleitung wird nicht verfolgt (siehe NewClient); Inhalt der Antwort entfällt.
			return &apiError{Status: resp.StatusCode, Message: "Weiterleitung wird nicht verfolgt"}
		}
		if resp.StatusCode >= 300 {
			var e struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(body, &e) //nolint:errcheck // Fehlerdetails sind optional, Status wird ohnehin gemeldet
			ae := &apiError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
			switch {
			case resp.StatusCode == 429 || resp.StatusCode >= 500:
				return domain.Transient(ae)
			case resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404:
				// Token oder Freigabe fehlt: Dateien sollen warten, nicht im Fehlerordner landen.
				return domain.Transient(fmt.Errorf("%w – Token gültig? Ist die Integration mit der Datenbank verbunden?", ae))
			}
			return ae
		}
		if readErr != nil {
			return fmt.Errorf("%w: %w", errAntwort, readErr)
		}
		if out != nil {
			return json.Unmarshal(body, out)
		}
		return nil
	}
}

// ResolveDataSource akzeptiert eine Data-Source-ID oder eine Datenbank-ID
// und liefert Data-Source-ID plus Property-Typen (Name → Typ).
func (n *Client) ResolveDataSource(ctx context.Context, id string) (string, Schema, error) {
	id = strings.TrimSpace(strings.TrimPrefix(id, "collection://"))
	props, err := n.dataSourceProps(ctx, id)
	if err == nil {
		return id, props, nil
	}
	var db struct {
		DataSources []struct {
			ID string `json:"id"`
		} `json:"data_sources"`
	}
	if derr := n.do(ctx, http.MethodGet, "/v1/databases/"+url.PathEscape(id), nil, &db, true); derr == nil && len(db.DataSources) > 0 {
		dsID := db.DataSources[0].ID
		props, err2 := n.dataSourceProps(ctx, dsID)
		return dsID, props, err2
	}
	return "", nil, err
}

func (n *Client) dataSourceProps(ctx context.Context, id string) (Schema, error) {
	var out struct {
		Properties map[string]struct {
			Type PropertyKind `json:"type"`
		} `json:"properties"`
	}
	if err := n.do(ctx, http.MethodGet, "/v1/data_sources/"+url.PathEscape(id), nil, &out, true); err != nil {
		return nil, err
	}
	m := make(Schema, len(out.Properties))
	for k, v := range out.Properties {
		m[k] = v.Type
	}
	return m, nil
}

// Page ist eine Seite mit ID und Titel (z. B. ein Projekt).
type Page struct {
	ID   string
	Name string
}

// ActiveProjects liefert alle Projekte, die nicht erledigt und nicht archiviert sind.
func (n *Client) ActiveProjects(ctx context.Context, dsID string, p ProjectsProps, types Schema) ([]Page, error) {
	var filters []any
	if t := types[p.Status]; types.Is(p.Status, KindStatus, KindSelect) && p.DoneValue != "" {
		filters = append(filters, t.filter(p.Status, map[string]any{"does_not_equal": p.DoneValue}))
	}
	if types.Is(p.Archive, KindCheckbox) {
		filters = append(filters, KindCheckbox.filter(p.Archive, map[string]any{"equals": false}))
	}
	body := map[string]any{"page_size": 100}
	switch len(filters) {
	case 0:
	case 1:
		body["filter"] = filters[0]
	default:
		body["filter"] = map[string]any{"and": filters}
	}
	var res []Page
	for page := 0; page < 20; page++ {
		var out struct {
			Results []struct {
				ID         string                     `json:"id"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"results"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		if err := n.do(ctx, http.MethodPost, "/v1/data_sources/"+url.PathEscape(dsID)+"/query", body, &out, true); err != nil {
			return nil, err
		}
		for _, r := range out.Results {
			if name := titleText(r.Properties[p.Title]); name != "" {
				res = append(res, Page{ID: r.ID, Name: name})
			}
		}
		if !out.HasMore || out.NextCursor == "" {
			break
		}
		body["start_cursor"] = out.NextCursor
	}
	return res, nil
}

func titleText(raw json.RawMessage) string {
	var p struct {
		Title []struct {
			PlainText string `json:"plain_text"`
		} `json:"title"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return ""
	}
	var b strings.Builder
	for _, t := range p.Title {
		b.WriteString(t.PlainText)
	}
	return strings.TrimSpace(b.String())
}

// ErrUnvollstaendig heißt: die Seite existiert, aber nicht alle Blöcke wurden
// angehängt. CreatePage liefert dann ID und URL mit diesem Fehler.
var ErrUnvollstaendig = errors.New("Seite angelegt, aber nicht der ganze Inhalt")

// CreatePage legt eine Seite in einer Data Source an. Notion nimmt höchstens
// 100 Blöcke pro Request; der Rest wird angehängt. Scheitert das Anhängen,
// kommen ID und URL zusammen mit einem Fehler zurück, der ErrUnvollstaendig
// umhüllt (die Seite existiert, ein neuer Versuch gäbe eine Dublette).
func (n *Client) CreatePage(ctx context.Context, dsID string, props map[string]any, children []any) (string, string, error) {
	first, rest := children, []any(nil)
	if len(children) > 100 {
		first, rest = children[:100], children[100:]
	}
	body := map[string]any{
		"parent":     map[string]any{"type": "data_source_id", "data_source_id": dsID},
		"properties": props,
	}
	if len(first) > 0 {
		body["children"] = first
	}
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := n.do(ctx, http.MethodPost, "/v1/pages", body, &out, false); err != nil {
		if errors.Is(err, errAntwort) {
			err = fmt.Errorf("%w: %w", ErrUnklar, err)
		}
		return "", "", err
	}
	if len(rest) > 0 {
		if err := n.appendChildren(ctx, out.ID, rest); err != nil {
			return out.ID, out.URL, fmt.Errorf("%w: %w", ErrUnvollstaendig, err)
		}
	}
	return out.ID, out.URL, nil
}

// appendChildren hängt Blöcke an, höchstens 100 pro Request.
func (n *Client) appendChildren(ctx context.Context, blockID string, children []any) error {
	for len(children) > 0 {
		batch := children
		if len(batch) > 100 {
			batch = children[:100]
		}
		children = children[len(batch):]
		if err := n.do(ctx, http.MethodPatch, "/v1/blocks/"+url.PathEscape(blockID)+"/children", map[string]any{"children": batch}, nil, false); err != nil {
			return err
		}
	}
	return nil
}

// --- Helfer -------------------------------------------------------------------

func backoff(attempt int) time.Duration {
	d := time.Second << attempt
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func retryAfter(resp *http.Response, attempt int) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 && secs < 120 {
			return time.Duration(secs) * time.Second
		}
	}
	return backoff(attempt)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
