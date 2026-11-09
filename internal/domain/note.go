// Package domain enthält die Fachtypen von meetingsync und die Fehlerklassen.
// Es importiert nur die Standardbibliothek und kein anderes Projektpaket.
package domain

import (
	"fmt"
	"strings"
	"time"
)

// MeetingNote ist genau das, was das lokale Modell liefern soll.
// Die Felder folgen dem Notion-Template "📋 TEMPLATE - Meeting Notes".
type MeetingNote struct {
	Titel            string    `json:"titel"`
	Typ              string    `json:"typ"`
	Teilnehmer       []string  `json:"teilnehmer"`
	KontextZiel      string    `json:"kontext_ziel"`
	Themen           []Thema   `json:"themen"`
	Entscheidungen   []string  `json:"entscheidungen"`
	NextActions      []Action  `json:"next_actions"`
	OffeneFragen     []string  `json:"offene_fragen"`
	NaechstesMeeting Naechstes `json:"naechstes_meeting"`
	Projekt          string    `json:"projekt"`
	PruefGruende     []string  `json:"pruef_gruende"`
	Pruefen          bool      `json:"pruefen"`
}

type Thema struct {
	Titel  string   `json:"titel"`
	Punkte []string `json:"punkte"`
}

type Action struct {
	Aufgabe  string `json:"aufgabe"`
	Owner    string `json:"owner"`
	Deadline string `json:"deadline"`
	FuerMich bool   `json:"fuer_mich"`
}

type Naechstes struct {
	Datum  string   `json:"datum"`
	Themen []string `json:"themen"`
}

// NoteMeta enthält alles, was nicht vom Modell kommt.
type NoteMeta struct {
	Timing        Timing
	SourceFiles   []string
	ReviewReasons []string
	Transcript    string // nur gesetzt, wenn include_transcript aktiv ist
}

// Kind beschreibt, was in einer Exportdatei steckt.
type Kind int

const (
	KindUnsupported Kind = iota
	KindTranscript
	KindSummary
)

func (k Kind) String() string {
	switch k {
	case KindTranscript:
		return "Transkript"
	case KindSummary:
		return "Zusammenfassung"
	default:
		return "nicht unterstützt"
	}
}

// Timing beschreibt, wann das Meeting stattfand.
type Timing struct {
	Start     time.Time
	End       time.Time // Null, wenn unbekannt
	HasTime   bool
	Estimated bool
}

var weekdaysShort = []string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}

func (t Timing) DateLabel() string {
	return fmt.Sprintf("%s (%s)", t.Start.Format("02.01.2006"), weekdaysShort[t.Start.Weekday()])
}

func (t Timing) TimeLabel() string {
	if !t.HasTime {
		return "–"
	}
	var s string
	switch {
	case !t.End.IsZero() && !t.End.Equal(t.Start):
		s = fmt.Sprintf("%s–%s (%d min)", t.Start.Format("15:04"), t.End.Format("15:04"), int(t.End.Sub(t.Start).Minutes()+0.5))
	case t.Estimated:
		s = "ca. " + t.Start.Format("15:04") + " (Export)"
	default:
		s = t.Start.Format("15:04")
	}
	if t.Estimated && !strings.Contains(s, "Export") {
		s += ", geschätzt"
	}
	return s
}
