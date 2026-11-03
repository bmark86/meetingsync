package transcript

import (
	"testing"
	"time"
)

// TestDateFromNameFormats hält das Ergebnis für alle bekannten Namensformate fest
// (R-M3-20). Nur die mit "R-M3-20" markierten Fälle haben sich gegenüber v1.0.0
// geändert: dort begann der ISO-Treffer im Jahr eines deutschen Datums.
func TestDateFromNameFormats(t *testing.T) {
	cases := []struct {
		name            string
		y, mo, d, h, mi int
		hasTime, ok     bool
	}{
		// MacWhisper
		{"Meeting - Teams 2026-11-01 01_33_06", 2026, 11, 1, 1, 33, true, true},
		{"Meeting - Teams 2026-11-01 01_33_06.srt", 2026, 11, 1, 1, 33, true, true},
		{"Meeting - Teams 2026-11-01 01_33_06.txt", 2026, 11, 1, 1, 33, true, true},
		{"Meeting - Zoom 2026-11-01 14_05_59.vtt", 2026, 11, 1, 14, 5, true, true},
		// ISO mit und ohne Uhrzeit
		{"2026-11-01", 2026, 11, 1, 0, 0, false, true},
		{"2026-11-01.txt", 2026, 11, 1, 0, 0, false, true},
		{"2026-11-01 14-30.txt", 2026, 11, 1, 14, 30, true, true},
		{"2026_11_01.txt", 2026, 11, 1, 0, 0, false, true},
		{"2026.11.01.txt", 2026, 11, 1, 0, 0, false, true},
		{"2026-11-01T14:30.txt", 2026, 11, 1, 14, 30, true, true},
		{"2026-11-01 at 14.30.txt", 2026, 11, 1, 14, 30, true, true},
		{"2026-11-01_14-30.md", 2026, 11, 1, 14, 30, true, true},
		{"2026-11-01, 14.30.txt", 2026, 11, 1, 14, 30, true, true},
		{"Sync 2026-11-01 9-05.txt", 2026, 11, 1, 9, 5, true, true},
		// deutsch
		{"07.01.2026.txt", 2026, 1, 7, 0, 0, false, true},
		{"7.1.2026.txt", 2026, 1, 7, 0, 0, false, true},
		{"07.01.2026 08:45.txt", 2026, 1, 7, 8, 45, true, true},
		{"07.01.2026-08h45.txt", 2026, 1, 7, 8, 45, true, true},
		{"07.01.2026, 08:45.txt", 2026, 1, 7, 8, 45, true, true},
		{"07.01.2026 8.45.txt", 2026, 1, 7, 8, 45, true, true},
		// deutsch, Uhrzeit mit Trennzeichen aus dem ISO-Muster (R-M3-20)
		{"07.01.2026_08-45.txt", 2026, 1, 7, 8, 45, true, true},        // vorher 2026-08-45, ungültig
		{"Weekly 07.01.2026_08-45.txt", 2026, 1, 7, 8, 45, true, true}, // vorher 2026-08-45, ungültig
		{"07.01.2026_08-15.txt", 2026, 1, 7, 8, 15, true, true},        // vorher 2026-08-15, falsch
		{"07.01.2026-08-45.txt", 2026, 1, 7, 8, 45, true, true},        // vorher 2026-08-45, ungültig
		{"07.01.2026-08-15.txt", 2026, 1, 7, 8, 15, true, true},        // vorher 2026-08-15, falsch
		{"07.01.2026_08.45.txt", 2026, 1, 7, 8, 45, true, true},        // vorher 2026-08-45, ungültig
		{"07.01.2026.08.45.txt", 2026, 1, 7, 0, 0, false, true},        // vorher 2026-08-45, ungültig
		{"07.01.2026.08.15.txt", 2026, 1, 7, 0, 0, false, true},        // vorher 2026-08-15, falsch
		// ohne Datum
		{"Aufnahme.txt", 0, 0, 0, 0, 0, false, false},
		{"Weekly Sync.txt", 0, 0, 0, 0, 0, false, false},
		{"Aufnahme 7.txt", 0, 0, 0, 0, 0, false, false},
		{"Projekt 2026.txt", 0, 0, 0, 0, 0, false, false},
		{"20261101.txt", 0, 0, 0, 0, 0, false, false},
		{"1999-10-28.txt", 0, 0, 0, 0, 0, false, false},
		{"dir/2026-10-28/Aufnahme.txt", 0, 0, 0, 0, 0, false, false},
		// zwei Daten: ISO hat Vorrang, auch wenn es später steht
		{"2026-11-01 und 2026-10-01.txt", 2026, 11, 1, 0, 0, false, true},
		{"07.01.2026 und 2026-11-01.txt", 2026, 11, 1, 0, 0, false, true},
		{"2026-11-01 und 07.01.2026.txt", 2026, 11, 1, 0, 0, false, true},
		{"2026-13-01 und 07.01.2026.txt", 2026, 13, 1, 0, 0, false, false},
		{"31.02.2026 und 2026-11-01.txt", 2026, 11, 1, 0, 0, false, true},
		// ungültige Daten
		{"2026-13-01.txt", 2026, 13, 1, 0, 0, false, false},
		{"31.02.2026.txt", 2026, 2, 31, 0, 0, false, false},
		{"32.01.2026.txt", 2026, 1, 32, 0, 0, false, false},
		{"07.13.2026.txt", 2026, 13, 7, 0, 0, false, false},
	}
	for _, c := range cases {
		y, mo, d, h, mi, ht, ok := dateFromName(c.name)
		if y != c.y || mo != c.mo || d != c.d || h != c.h || mi != c.mi || ht != c.hasTime || ok != c.ok {
			t.Errorf("%q: got %d-%d-%d %d:%d ht=%v ok=%v, want %d-%d-%d %d:%d ht=%v ok=%v",
				c.name, y, mo, d, h, mi, ht, ok, c.y, c.mo, c.d, c.h, c.mi, c.hasTime, c.ok)
		}
	}
}

func TestMeetingTimingGermanUnderscoreTime(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 1, 7, 10, 0, 0, 0, time.UTC)
	tm := MeetingTiming("Weekly 07.01.2026_08-45.txt", mtime, 30*time.Minute, loc)
	want := time.Date(2026, 1, 7, 8, 45, 0, 0, loc)
	if tm.Estimated || !tm.HasTime || !tm.Start.Equal(want) || tm.Start.Location() != loc ||
		!tm.End.Equal(want.Add(30*time.Minute)) {
		t.Errorf("got %+v, want Start %v", tm, want)
	}
}
