package secret

// Tests der Token-Suche (R-M2-07): jeder Zweig der Reihenfolge NOTION_TOKEN →
// Schlüsselbund → Config-Datei mit Fake-Runner, nie das echte security.

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// fakeRun zeichnet die Aufrufe (Pfad + Argumente) und die Umgebungen auf und
// liefert out/err.
type fakeRun struct {
	calls [][]string
	envs  [][]string
	out   []byte
	err   error
}

func (f *fakeRun) run(path string, env []string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{path}, args...))
	f.envs = append(f.envs, env)
	return f.out, f.err
}

func source(service, file string, f *fakeRun) *notionToken {
	return &notionToken{keychainService: service, fileToken: file, run: f.run}
}

const wantFehlt = "kein Notion-Token: NOTION_TOKEN setzen oder token_keychain_service bzw. token in der Config angeben"

// Fängt: falsche Reihenfolge, fehlendes Trimmen, Rückfall auf die Datei nach Schlüsselbund-Fehler.
func TestNotionTokenOrder(t *testing.T) {
	kcErr := errors.New("exit status 44")
	tests := []struct {
		name      string
		env       string
		service   string
		file      string
		fake      fakeRun
		want      string
		wantErr   string
		wantCalls [][]string
	}{
		{name: "Umgebung gewinnt vor allem", env: " env-tok\n", service: "svc", file: "file-tok", want: "env-tok"},
		{name: "Umgebung nur Leerzeichen zählt nicht", env: "  ", file: "file-tok", want: "file-tok"},
		{
			name: "Schlüsselbund ok, Ausgabe getrimmt", service: " svc ", fake: fakeRun{out: []byte("  kc-tok\n")}, want: "kc-tok",
			wantCalls: [][]string{{"/usr/bin/security", "find-generic-password", "-s", "svc", "-w"}},
		},
		{
			name: "Schlüsselbund gesetzt, Token in der Datei wird nicht benutzt", service: "svc", file: "file-tok",
			fake: fakeRun{out: []byte("kc-tok\n")}, want: "kc-tok",
			wantCalls: [][]string{{"/usr/bin/security", "find-generic-password", "-s", "svc", "-w"}},
		},
		{
			name: "Schlüsselbund-Fehler bricht ab, kein Rückfall auf die Datei", service: "svc", file: "file-tok",
			fake: fakeRun{out: []byte("kc-tok"), err: kcErr}, wantErr: "Notion-Token aus dem Schlüsselbund (svc) nicht lesbar: exit status 44",
			wantCalls: [][]string{{"/usr/bin/security", "find-generic-password", "-s", "svc", "-w"}},
		},
		{name: "kein Dienst: Token aus der Datei, getrimmt", file: " file-tok\n", want: "file-tok"},
		{name: "Dienst nur Leerzeichen: Token aus der Datei", service: "  ", file: "file-tok", want: "file-tok"},
		{name: "nichts gesetzt", wantErr: wantFehlt},
		{name: "Datei-Token nur Leerzeichen", file: "  ", wantErr: wantFehlt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NOTION_TOKEN", tc.env)
			f := tc.fake
			got, err := source(tc.service, tc.file, &f).Token()
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr || got != "" {
					t.Fatalf("err = %v (Token leer: %v), want %q", err, got == "", tc.wantErr)
				}
				if tc.fake.err != nil && !errors.Is(err, tc.fake.err) {
					t.Fatalf("Ursache nicht eingepackt: %v", err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("err = %v, Token richtig: %v", err, got == tc.want)
			}
			if !reflect.DeepEqual(f.calls, tc.wantCalls) {
				t.Fatalf("Aufrufe = %q, want %q", f.calls, tc.wantCalls)
			}
		})
	}
}

// Fängt: NewNotion ohne echten Runner oder mit vertauschten Feldern.
func TestNewNotion(t *testing.T) {
	src, ok := NewNotion("svc", "file-tok").(*notionToken)
	if !ok {
		t.Fatalf("NewNotion liefert %T", src)
	}
	if src.keychainService != "svc" || src.fileToken != "file-tok" || src.run == nil {
		t.Fatalf("Felder falsch gesetzt (Runner gesetzt: %v)", src.run != nil)
	}
	if reflect.ValueOf(src.run).Pointer() != reflect.ValueOf(runOutput).Pointer() {
		t.Fatal("Runner ist nicht runOutput")
	}
}

// Fängt: security erbt die volle Umgebung (Geheimnisse, fremder PATH) oder
// bekommt HOME/USER/LANG nicht. NOTION_TOKEN nur aus Leerzeichen zählt nicht als
// Token, darum läuft der Schlüsselbund-Zweig und der Wert darf nicht mitgehen.
func TestKeychainMinimalEnv(t *testing.T) {
	t.Setenv("NOTION_TOKEN", "   ")
	t.Setenv("HOME", "/Users/test")
	t.Setenv("USER", "test")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("PATH", "/tmp/boese:/usr/bin")
	t.Setenv("LLM_API_KEY", "geheim")
	f := &fakeRun{out: []byte("kc-tok")}
	if _, err := source("svc", "", f).Token(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"HOME=/Users/test", "USER=test", "LANG=de_DE.UTF-8", "PATH=/usr/bin:/bin"}}
	if !reflect.DeepEqual(f.envs, want) {
		t.Fatalf("Umgebung = %q, want %q", f.envs, want)
	}
}

// Fängt: nicht gesetzte Variablen werden als leere Einträge mitgegeben.
func TestKeychainMinimalEnvOnlySetVars(t *testing.T) {
	t.Setenv("NOTION_TOKEN", "")
	t.Setenv("HOME", "/Users/test")
	t.Setenv("USER", "")
	t.Setenv("LANG", "")
	if err := os.Unsetenv("USER"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("LANG"); err != nil {
		t.Fatal(err)
	}
	f := &fakeRun{out: []byte("kc-tok")}
	if _, err := source("svc", "", f).Token(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"HOME=/Users/test", "PATH=/usr/bin:/bin"}}
	if !reflect.DeepEqual(f.envs, want) {
		t.Fatalf("Umgebung = %q, want %q", f.envs, want)
	}
}

// Fängt: runOutput liefert nicht stdout oder setzt die Umgebung nicht.
// Harmloses Programm, nie security.
func TestRunOutput(t *testing.T) {
	out, err := runOutput("/bin/echo", nil, "-n", "x y")
	if err != nil || string(out) != "x y" {
		t.Fatalf("runOutput = %q, %v", out, err)
	}
	t.Setenv("MEETINGSYNC_ELTERN", "nicht-weitergeben")
	out, err = runOutput("/usr/bin/env", []string{"A=1", "PATH=/usr/bin:/bin"})
	if err != nil || string(out) != "A=1\nPATH=/usr/bin:/bin\n" {
		t.Fatalf("runOutput(env) = %q, %v", out, err)
	}
}
