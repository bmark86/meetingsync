package main

import (
	"strings"
	"testing"
	"time"
)

// Normalpfad (R-M1-03 A5): kein Fehler, also weder Lücken-Felder in der
// fertig-Zeile noch ein Lücken-Hinweis im Log.
func TestNormalPassHasNoGapFields(t *testing.T) {
	e := newCrashEnv(t)
	e.app.cfg.Notion.CreateTasksFor = "all"
	e.pass(time.Now())
	if n, k := e.pages(); n != 1 || k != 2 {
		t.Fatalf("%d Notizen, %d Tasks; want 1, 2", n, k)
	}
	ln := lastLedgerLine(t, e)
	if ln["hash"] != e.hash() {
		t.Fatalf("keine fertig-Zeile: %v", ln)
	}
	if _, ok := ln["todos_fehlen"]; ok {
		t.Fatalf("todos_fehlen im Normalpfad: %v", ln)
	}
	if _, ok := ln["bloecke_fehler"]; ok {
		t.Fatalf("bloecke_fehler im Normalpfad: %v", ln)
	}
	if strings.Contains(e.log.String(), "Notiz unvollständig") {
		t.Fatalf("Lücken-Hinweis im Normalpfad:\n%s", e.log.String())
	}
}
