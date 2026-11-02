package notion

import (
	"fmt"
	"slices"

	"meetingsync/internal/domain"
)

// PropertyKind ist der Typ einer Notion-Property, so wie die API ihn nennt:
// "type" im Schema einer Data Source und Schlüssel des Werts im Payload.
type PropertyKind string

// Die Property-Typen, die meetingsync liest oder schreibt. Andere Typen der
// API (z. B. people) kommen unverändert durch und passen auf keine Konstante.
const (
	KindTitle    PropertyKind = "title"
	KindRichText PropertyKind = "rich_text"
	KindStatus   PropertyKind = "status"
	KindSelect   PropertyKind = "select"
	KindRelation PropertyKind = "relation"
	KindCheckbox PropertyKind = "checkbox"
	KindDate     PropertyKind = "date"
)

// value baut den Property-Wert {"<typ>": v}. Einzige Stelle, an der aus dem
// Typ ein JSON-Schlüssel wird.
func (k PropertyKind) value(v any) map[string]any { return map[string]any{string(k): v} }

// filter baut eine Filterbedingung {"property": name, "<typ>": cond}.
func (k PropertyKind) filter(name string, cond any) map[string]any {
	f := k.value(cond)
	f["property"] = name
	return f
}

// Schema ordnet jeder Property einer Data Source ihren Typ zu (Name → Typ).
type Schema map[string]PropertyKind

// Is meldet, ob die Property name einen der Typen kinds hat. Eine fehlende
// Property hat keinen Typ.
func (s Schema) Is(name string, kinds ...PropertyKind) bool {
	k, ok := s[name]
	return ok && slices.Contains(kinds, k)
}

// NotesProps sind die Property-Namen der Notes-Datenbank (Felder wie in der
// Konfiguration, damit main per Typumwandlung übergeben kann).
type NotesProps struct {
	Title       string
	Status      string
	StatusValue string
	Projects    string
	Review      string
}

// TasksProps sind die Property-Namen der Tasks-Datenbank.
type TasksProps struct {
	Title       string
	Description string
	DueDate     string
	Notes       string
	Projects    string
	Completed   string
}

// ProjectsProps sind die Property-Namen der Projects-Datenbank.
type ProjectsProps struct {
	Title     string
	Status    string
	DoneValue string
	Archive   string
}

// CheckNotes prüft, ob die Notes-Datenbank Titel und Status (status oder
// select) unter den konfigurierten Namen hat.
func (s Schema) CheckNotes(p NotesProps) error {
	if !s.Is(p.Title, KindTitle) {
		return fmt.Errorf("Notes-Datenbank: Titel-Property %q nicht gefunden", p.Title)
	}
	if !s.Is(p.Status, KindStatus, KindSelect) {
		return fmt.Errorf("Notes-Datenbank: Status-Property %q nicht gefunden", p.Status)
	}
	return nil
}

// CheckTasks prüft, ob die Tasks-Datenbank den Titel unter dem konfigurierten
// Namen hat.
func (s Schema) CheckTasks(p TasksProps) error {
	if !s.Is(p.Title, KindTitle) {
		return fmt.Errorf("Tasks-Datenbank: Titel-Property %q nicht gefunden", p.Title)
	}
	return nil
}

// NoteProperties baut die Properties der Meeting-Notiz. Status, Projekt und
// Prüfen-Checkbox nur, wenn die Datenbank sie mit passendem Typ hat.
func NoteProperties(s Schema, p NotesProps, title string, project *Page, review bool) map[string]any {
	props := map[string]any{p.Title: KindTitle.value(rt(title))}
	if s.Is(p.Status, KindStatus, KindSelect) {
		props[p.Status] = s[p.Status].value(map[string]any{"name": p.StatusValue})
	}
	if project != nil && s.Is(p.Projects, KindRelation) {
		props[p.Projects] = KindRelation.value([]any{map[string]any{"id": project.ID}})
	}
	if s.Is(p.Review, KindCheckbox) {
		props[p.Review] = KindCheckbox.value(review)
	}
	return props
}

// TaskProperties baut die Properties eines To-dos, verknüpft mit der Notiz
// noteID und ggf. dem Projekt. Optionale Felder nur bei passendem Typ.
func TaskProperties(s Schema, p TasksProps, act domain.Action, noteTitle, noteID string, project *Page) map[string]any {
	props := map[string]any{p.Title: KindTitle.value(rt(act.Aufgabe))}
	if s.Is(p.Description, KindRichText) {
		desc := "Aus Meeting: " + noteTitle
		if act.Owner != "" {
			desc += " · Owner: " + act.Owner
		}
		props[p.Description] = KindRichText.value(rt(desc))
	}
	if act.Deadline != "" && s.Is(p.DueDate, KindDate) {
		props[p.DueDate] = KindDate.value(map[string]any{"start": act.Deadline})
	}
	if s.Is(p.Notes, KindRelation) {
		props[p.Notes] = KindRelation.value([]any{map[string]any{"id": noteID}})
	}
	if project != nil && s.Is(p.Projects, KindRelation) {
		props[p.Projects] = KindRelation.value([]any{map[string]any{"id": project.ID}})
	}
	if s.Is(p.Completed, KindCheckbox) {
		props[p.Completed] = KindCheckbox.value(false)
	}
	return props
}
