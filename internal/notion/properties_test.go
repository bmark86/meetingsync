package notion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/domain"
)

var (
	ptNotes    = NotesProps{Title: "Name", Status: "Status", StatusValue: "Inbox", Projects: "Projects", Review: "Prüfen"}
	ptTasks    = TasksProps{Title: "Name", Description: "Description", DueDate: "Due Date", Notes: "Notes", Projects: "Projects", Completed: "Completed"}
	ptProjects = ProjectsProps{Title: "Name", Status: "status", DoneValue: "Done", Archive: "Archive"}
)

func ptJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Fängt: Typ-Konstanten weichen von den Namen der Notion-API ab (Payload-Schlüssel
// und Schema-Abgleich hängen daran).
func TestPropertyKindValues(t *testing.T) {
	want := map[PropertyKind]string{
		KindTitle: "title", KindRichText: "rich_text", KindStatus: "status", KindSelect: "select",
		KindRelation: "relation", KindCheckbox: "checkbox", KindDate: "date",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("%q, erwartet %q", k, s)
		}
	}
}

// Fängt: Is meldet fehlende Properties oder fremde Typen als Treffer.
func TestSchemaIs(t *testing.T) {
	s := Schema{"A": KindStatus, "B": PropertyKind("people")}
	cases := []struct {
		name  string
		kinds []PropertyKind
		want  bool
	}{
		{"A", []PropertyKind{KindStatus}, true},
		{"A", []PropertyKind{KindSelect, KindStatus}, true},
		{"A", []PropertyKind{KindSelect}, false},
		{"A", nil, false},
		{"B", []PropertyKind{KindSelect, KindStatus}, false},
		{"fehlt", []PropertyKind{KindTitle}, false},
	}
	for _, c := range cases {
		if got := s.Is(c.name, c.kinds...); got != c.want {
			t.Errorf("Is(%q, %v) = %v, erwartet %v", c.name, c.kinds, got, c.want)
		}
	}
	if Schema(nil).Is("A", KindStatus) {
		t.Error("leeres Schema darf nichts melden")
	}
}

// Fängt: falsche Reihenfolge oder falscher Text der Schema-Fehler aus Init.
func TestCheckNotesAndTasks(t *testing.T) {
	ok := Schema{"Name": KindTitle, "Status": KindStatus}
	if err := ok.CheckNotes(ptNotes); err != nil {
		t.Errorf("status: %v", err)
	}
	if err := (Schema{"Name": KindTitle, "Status": KindSelect}).CheckNotes(ptNotes); err != nil {
		t.Errorf("select: %v", err)
	}
	cases := []struct {
		s    Schema
		want string
	}{
		{Schema{"Name": KindRichText, "Status": KindStatus}, `Notes-Datenbank: Titel-Property "Name" nicht gefunden`},
		{Schema{"Status": KindStatus}, `Notes-Datenbank: Titel-Property "Name" nicht gefunden`},
		{Schema{}, `Notes-Datenbank: Titel-Property "Name" nicht gefunden`}, // Titel vor Status
		{Schema{"Name": KindTitle, "Status": KindCheckbox}, `Notes-Datenbank: Status-Property "Status" nicht gefunden`},
		{Schema{"Name": KindTitle}, `Notes-Datenbank: Status-Property "Status" nicht gefunden`},
	}
	for _, c := range cases {
		if err := c.s.CheckNotes(ptNotes); err == nil || err.Error() != c.want {
			t.Errorf("CheckNotes(%v) = %v, erwartet %q", c.s, err, c.want)
		}
	}
	if err := (Schema{"Name": KindTitle}).CheckTasks(ptTasks); err != nil {
		t.Errorf("CheckTasks: %v", err)
	}
	if err := (Schema{"Name": KindSelect}).CheckTasks(ptTasks); err == nil || err.Error() != `Tasks-Datenbank: Titel-Property "Name" nicht gefunden` {
		t.Errorf("CheckTasks = %v", err)
	}
}

// Fängt: falsche Payload-Form der Notiz-Properties (Schlüssel = Typ), Status
// als falscher Typ, Projekt/Prüfen ohne passenden Typ gesetzt.
func TestNoteProperties(t *testing.T) {
	full := Schema{"Name": KindTitle, "Status": KindSelect, "Projects": KindRelation, "Prüfen": KindCheckbox}
	p := &Page{ID: "proj-1", Name: "AI-First"}
	got := ptJSON(t, NoteProperties(full, ptNotes, "T", p, true))
	want := `{"Name":{"title":[{"text":{"content":"T"},"type":"text"}]},"Projects":{"relation":[{"id":"proj-1"}]},"Prüfen":{"checkbox":true},"Status":{"select":{"name":"Inbox"}}}`
	if got != want {
		t.Errorf("voll:\n%s\nerwartet\n%s", got, want)
	}

	status := Schema{"Name": KindTitle, "Status": KindStatus, "Projects": KindRichText, "Prüfen": KindCheckbox}
	got = ptJSON(t, NoteProperties(status, ptNotes, "T", p, false))
	want = `{"Name":{"title":[{"text":{"content":"T"},"type":"text"}]},"Prüfen":{"checkbox":false},"Status":{"status":{"name":"Inbox"}}}`
	if got != want {
		t.Errorf("status:\n%s\nerwartet\n%s", got, want)
	}

	got = ptJSON(t, NoteProperties(full, ptNotes, "", nil, false))
	want = `{"Name":{"title":[]},"Prüfen":{"checkbox":false},"Status":{"select":{"name":"Inbox"}}}`
	if got != want {
		t.Errorf("ohne Projekt:\n%s\nerwartet\n%s", got, want)
	}

	got = ptJSON(t, NoteProperties(Schema{"Status": PropertyKind("people")}, ptNotes, "T", p, true))
	want = `{"Name":{"title":[{"text":{"content":"T"},"type":"text"}]}}`
	if got != want {
		t.Errorf("minimal:\n%s\nerwartet\n%s", got, want)
	}
}

// Fängt: falsche Payload-Form der To-do-Properties, Beschreibung ohne Owner,
// Fälligkeit ohne Deadline, Verknüpfungen ohne passenden Typ.
func TestTaskProperties(t *testing.T) {
	full := Schema{"Name": KindTitle, "Description": KindRichText, "Due Date": KindDate, "Notes": KindRelation, "Projects": KindRelation, "Completed": KindCheckbox}
	act := domain.Action{Aufgabe: "A", Owner: "Mark", Deadline: "2026-10-30"}
	got := ptJSON(t, TaskProperties(full, ptTasks, act, "N", "page-1", &Page{ID: "proj-1"}))
	want := `{"Completed":{"checkbox":false},"Description":{"rich_text":[{"text":{"content":"Aus Meeting: N · Owner: Mark"},"type":"text"}]},` +
		`"Due Date":{"date":{"start":"2026-10-30"}},"Name":{"title":[{"text":{"content":"A"},"type":"text"}]},` +
		`"Notes":{"relation":[{"id":"page-1"}]},"Projects":{"relation":[{"id":"proj-1"}]}}`
	if got != want {
		t.Errorf("voll:\n%s\nerwartet\n%s", got, want)
	}

	got = ptJSON(t, TaskProperties(full, ptTasks, domain.Action{Aufgabe: "A"}, "N", "page-1", nil))
	want = `{"Completed":{"checkbox":false},"Description":{"rich_text":[{"text":{"content":"Aus Meeting: N"},"type":"text"}]},` +
		`"Name":{"title":[{"text":{"content":"A"},"type":"text"}]},"Notes":{"relation":[{"id":"page-1"}]}}`
	if got != want {
		t.Errorf("ohne Owner/Deadline/Projekt:\n%s\nerwartet\n%s", got, want)
	}

	wrong := Schema{"Name": KindTitle, "Description": KindTitle, "Due Date": KindRichText, "Notes": KindCheckbox, "Projects": KindSelect, "Completed": KindStatus}
	got = ptJSON(t, TaskProperties(wrong, ptTasks, act, "N", "page-1", &Page{ID: "proj-1"}))
	want = `{"Name":{"title":[{"text":{"content":"A"},"type":"text"}]}}`
	if got != want {
		t.Errorf("falsche Typen:\n%s\nerwartet\n%s", got, want)
	}
}

// Fängt: Schema-Typen werden falsch gelesen; Datenbank-ID wird nicht auf die
// erste Data Source aufgelöst; collection://-Präfix bleibt im Pfad.
func TestResolveDataSource(t *testing.T) {
	ncSleeps(t, true)
	n, s := ncNotion(t,
		ncReply{status: 200, body: `{"properties":{"Name":{"type":"title"},"Status":{"type":"status"},"Wer":{"type":"people"}}}`},
	)
	id, schema, err := n.ResolveDataSource(context.Background(), "collection://ds-1 ")
	if err != nil || id != "ds-1" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if len(schema) != 3 || schema["Name"] != KindTitle || schema["Status"] != KindStatus || schema["Wer"] != PropertyKind("people") {
		t.Errorf("Schema = %v", schema)
	}
	if s.reqs[0].URL.Path != "/v1/data_sources/ds-1" {
		t.Errorf("Pfad = %q", s.reqs[0].URL.Path)
	}

	n, s = ncNotion(t,
		ncReply{status: 400, body: `{"code":"validation_error","message":"keine Data Source"}`},
		ncReply{status: 200, body: `{"data_sources":[{"id":"ds-2"},{"id":"ds-3"}]}`},
		ncReply{status: 200, body: `{"properties":{"Name":{"type":"title"}}}`},
	)
	id, schema, err = n.ResolveDataSource(context.Background(), "db-1")
	if err != nil || id != "ds-2" || schema["Name"] != KindTitle {
		t.Fatalf("über Datenbank: id = %q, Schema = %v, err = %v", id, schema, err)
	}
	if got := []string{s.reqs[0].URL.Path, s.reqs[1].URL.Path, s.reqs[2].URL.Path}; strings.Join(got, " ") != "/v1/data_sources/db-1 /v1/databases/db-1 /v1/data_sources/ds-2" {
		t.Errorf("Pfade = %v", got)
	}

	n, _ = ncNotion(t,
		ncReply{status: 400, body: `{"code":"validation_error","message":"erster"}`},
		ncReply{status: 200, body: `{"data_sources":[]}`},
	)
	if id, schema, err = n.ResolveDataSource(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "erster") || id != "" || schema != nil {
		t.Errorf("ohne Data Source: id = %q, Schema = %v, err = %v (erwartet erster Fehler)", id, schema, err)
	}
}

// Fängt: Filter mit falschem Typ-Schlüssel, Filter ohne passenden Typ, kein
// "and" bei zwei Filtern, Seiten ohne Titel in der Liste, Paginierung.
func TestActiveProjects(t *testing.T) {
	page := `{"results":[{"id":"p1","properties":{"Name":{"title":[{"plain_text":" AI-"},{"plain_text":"First "}]}}},` +
		`{"id":"p2","properties":{"Name":{"title":[]}}},{"id":"p3","properties":{}}],"has_more":true,"next_cursor":"c2"}`
	last := `{"results":[{"id":"p4","properties":{"Name":{"title":[{"plain_text":"Sirius"}]}}}],"has_more":false}`
	n, s := ncNotion(t, ncReply{status: 200, body: page}, ncReply{status: 200, body: last})
	types := Schema{"Name": KindTitle, "status": KindSelect, "Archive": KindCheckbox}
	got, err := n.ActiveProjects(context.Background(), "proj-ds", ptProjects, types)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Page{ID: "p1", Name: "AI-First"}) || got[1] != (Page{ID: "p4", Name: "Sirius"}) {
		t.Errorf("Projekte = %+v", got)
	}
	if s.reqs[0].URL.Path != "/v1/data_sources/proj-ds/query" {
		t.Errorf("Pfad = %q", s.reqs[0].URL.Path)
	}
	wantFilter := `"filter":{"and":[{"property":"status","select":{"does_not_equal":"Done"}},{"checkbox":{"equals":false},"property":"Archive"}]}`
	if !strings.Contains(s.bodies[0], wantFilter) || strings.Contains(s.bodies[0], "start_cursor") {
		t.Errorf("Body 1 = %s", s.bodies[0])
	}
	if !strings.Contains(s.bodies[1], `"start_cursor":"c2"`) {
		t.Errorf("Body 2 = %s", s.bodies[1])
	}

	cases := []struct {
		name  string
		types Schema
		props ProjectsProps
		want  string
	}{
		{"nur status", Schema{"status": KindStatus}, ptProjects, `{"filter":{"property":"status","status":{"does_not_equal":"Done"}},"page_size":100}`},
		{"nur Archiv", Schema{"status": KindRichText, "Archive": KindCheckbox}, ptProjects, `{"filter":{"checkbox":{"equals":false},"property":"Archive"},"page_size":100}`},
		{"ohne DoneValue", Schema{"status": KindStatus}, ProjectsProps{Title: "Name", Status: "status"}, `{"page_size":100}`},
		{"ohne Schema", nil, ptProjects, `{"page_size":100}`},
	}
	for _, c := range cases {
		n, s := ncNotion(t, ncReply{status: 200, body: `{"results":[]}`})
		if _, err := n.ActiveProjects(context.Background(), "d", c.props, c.types); err != nil {
			t.Fatal(err)
		}
		if s.bodies[0] != c.want {
			t.Errorf("%s: Body = %s, erwartet %s", c.name, s.bodies[0], c.want)
		}
	}

	ncSleeps(t, false)
	n, _ = ncNotion(t, ncReply{status: 500})
	if p, err := n.ActiveProjects(context.Background(), "d", ptProjects, nil); err == nil || p != nil {
		t.Errorf("Fehler erwartet, bekam %v, %v", p, err)
	}
}

// Fängt: Platzhalter im Titelformat werden falsch ersetzt oder nicht getrimmt.
func TestFormatTitle(t *testing.T) {
	tm := domain.Timing{Start: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)}
	n := &domain.MeetingNote{Titel: "Sirius Sync", Typ: "Team"}
	if got := FormatTitle(" {date} – {title} ({typ}) {date_de} ", n, tm); got != "2026-10-28 – Sirius Sync (Team) 28.10.2026" {
		t.Errorf("FormatTitle = %q", got)
	}
}

// Fängt: Deadline-Format in der Next-Actions-Tabelle (ungültig → leer).
func TestDeadlineLabel(t *testing.T) {
	if got := deadlineLabel("2026-10-30"); got != "30.10.2026" {
		t.Errorf("gültig: %q", got)
	}
	if got := deadlineLabel("bis Freitag"); got != "" {
		t.Errorf("ungültig: %q", got)
	}
}
