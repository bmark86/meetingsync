package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Notion ist ein schlanker Client für die Notion-API (Version 2025-09-03,
// Datenbanken werden über ihre Data Sources angesprochen).
type Notion struct {
	base    string
	version string
	token   string
	http    *http.Client
}

func NewNotion(cfg NotionConfig, token string) *Notion {
	return &Notion{
		base:    strings.TrimRight(cfg.BaseURL, "/"),
		version: cfg.APIVersion,
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

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
func (n *Notion) do(ctx context.Context, method, path string, in, out any, idempotent bool) error {
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
			if idempotent && attempt < 3 && sleepFn(ctx, backoff(attempt)) {
				continue
			}
			return transient(fmt.Errorf("Notion nicht erreichbar: %w", err))
		}
		body, _ := io.ReadAll(resp.Body) //nolint:errcheck // Lesefehler fuehrt unten zu Statusfehler ohne Details
		_ = resp.Body.Close()            //nolint:errcheck // Body ist bereits gelesen, Close-Fehler ohne Folgen

		if resp.StatusCode == http.StatusTooManyRequests ||
			(idempotent && resp.StatusCode >= 500) {
			if attempt < 4 && sleepFn(ctx, retryAfter(resp, attempt)) {
				continue
			}
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
				return transient(ae)
			case resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404:
				// Token oder Freigabe fehlt: Dateien sollen warten, nicht im Fehlerordner landen.
				return transient(fmt.Errorf("%w – Token gültig? Ist die Integration mit der Datenbank verbunden?", ae))
			}
			return ae
		}
		if out != nil {
			return json.Unmarshal(body, out)
		}
		return nil
	}
}

// ResolveDataSource akzeptiert eine Data-Source-ID oder eine Datenbank-ID
// und liefert Data-Source-ID plus Property-Typen (Name → Typ).
func (n *Notion) ResolveDataSource(ctx context.Context, id string) (string, map[string]string, error) {
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
	if derr := n.do(ctx, http.MethodGet, "/v1/databases/"+id, nil, &db, true); derr == nil && len(db.DataSources) > 0 {
		dsID := db.DataSources[0].ID
		props, err2 := n.dataSourceProps(ctx, dsID)
		return dsID, props, err2
	}
	return "", nil, err
}

func (n *Notion) dataSourceProps(ctx context.Context, id string) (map[string]string, error) {
	var out struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := n.do(ctx, http.MethodGet, "/v1/data_sources/"+id, nil, &out, true); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(out.Properties))
	for k, v := range out.Properties {
		m[k] = v.Type
	}
	return m, nil
}

type pageRef struct {
	ID   string
	Name string
}

// ActiveProjects liefert alle Projekte, die nicht erledigt und nicht archiviert sind.
func (n *Notion) ActiveProjects(ctx context.Context, dsID string, p ProjectsProps, types map[string]string) ([]pageRef, error) {
	var filters []any
	if t := types[p.Status]; (t == "status" || t == "select") && p.DoneValue != "" {
		filters = append(filters, map[string]any{"property": p.Status, t: map[string]any{"does_not_equal": p.DoneValue}})
	}
	if types[p.Archive] == "checkbox" {
		filters = append(filters, map[string]any{"property": p.Archive, "checkbox": map[string]any{"equals": false}})
	}
	body := map[string]any{"page_size": 100}
	switch len(filters) {
	case 0:
	case 1:
		body["filter"] = filters[0]
	default:
		body["filter"] = map[string]any{"and": filters}
	}
	var res []pageRef
	for page := 0; page < 20; page++ {
		var out struct {
			Results []struct {
				ID         string                     `json:"id"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"results"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		if err := n.do(ctx, http.MethodPost, "/v1/data_sources/"+dsID+"/query", body, &out, true); err != nil {
			return nil, err
		}
		for _, r := range out.Results {
			if name := titleText(r.Properties[p.Title]); name != "" {
				res = append(res, pageRef{ID: r.ID, Name: name})
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

// errUnvollstaendig: die Seite existiert, aber nicht alle Blöcke wurden
// angehängt. CreatePage liefert dann ID und URL mit diesem Fehler.
var errUnvollstaendig = errors.New("Seite angelegt, aber nicht der ganze Inhalt")

// CreatePage legt eine Seite in einer Data Source an. Notion nimmt höchstens
// 100 Blöcke pro Request; der Rest wird angehängt. Scheitert das Anhängen,
// kommen ID und URL zusammen mit einem Fehler zurück, der errUnvollstaendig
// umhüllt (die Seite existiert, ein neuer Versuch gäbe eine Dublette).
func (n *Notion) CreatePage(ctx context.Context, dsID string, props map[string]any, children []any) (string, string, error) {
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
		return "", "", err
	}
	if len(rest) > 0 {
		if err := n.AppendChildren(ctx, out.ID, rest); err != nil {
			return out.ID, out.URL, fmt.Errorf("%w: %w", errUnvollstaendig, err)
		}
	}
	return out.ID, out.URL, nil
}

func (n *Notion) AppendChildren(ctx context.Context, blockID string, children []any) error {
	for len(children) > 0 {
		batch := children
		if len(batch) > 100 {
			batch = children[:100]
		}
		children = children[len(batch):]
		if err := n.do(ctx, http.MethodPatch, "/v1/blocks/"+blockID+"/children", map[string]any{"children": batch}, nil, false); err != nil {
			return err
		}
	}
	return nil
}

// --- Fehlerklassen & Helfer ----------------------------------------------------

type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

// transient markiert Fehler, bei denen sich Warten lohnt (LM Studio aus,
// kein Netz, Rate-Limit). Solche Dateien bleiben liegen und werden später erneut versucht.
func transient(err error) error { return transientError{err} }

func isTransient(err error) bool {
	var t transientError
	return errors.As(err, &t) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

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

// sleepFn ist die Warte-Funktion von do; nur Tests tauschen sie aus.
var sleepFn = sleepCtx

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
