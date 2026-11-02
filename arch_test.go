package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "meetingsync"

// archRule verbietet einem Paket (und seinen Unterpaketen) Abhängigkeiten.
// forbidden prüft jede transitive Abhängigkeit (std: Standardbibliothek).
type archRule struct {
	name      string
	applies   func(pkg string) bool
	forbidden func(dep string, std bool) bool
}

func inPkg(pkg, root string) bool { return pkg == root || strings.HasPrefix(pkg, root+"/") }

// archRules sind die Abhängigkeitsregeln der Zielarchitektur (M2).
// Neue Regeln für weitere Pakete hier anhängen.
var archRules = []archRule{
	{
		name:      "internal/domain importiert nur Standardbibliothek",
		applies:   func(p string) bool { return inPkg(p, modulePath+"/internal/domain") },
		forbidden: func(d string, std bool) bool { return !std && !inPkg(d, modulePath+"/internal/domain") },
	},
	{
		name: "Infra-Pakete (internal/* außer pipeline) importieren internal/pipeline nicht",
		applies: func(p string) bool {
			return inPkg(p, modulePath+"/internal") && !inPkg(p, modulePath+"/internal/pipeline")
		},
		forbidden: func(d string, _ bool) bool { return inPkg(d, modulePath+"/internal/pipeline") },
	},
	{
		name:    "internal/transcript importiert nur Standardbibliothek und internal/domain",
		applies: func(p string) bool { return inPkg(p, modulePath+"/internal/transcript") },
		forbidden: func(d string, std bool) bool {
			return !std && !inPkg(d, modulePath+"/internal/domain") && !inPkg(d, modulePath+"/internal/transcript")
		},
	},
	{
		name:    "internal/ledger importiert nur Standardbibliothek",
		applies: func(p string) bool { return inPkg(p, modulePath+"/internal/ledger") },
		forbidden: func(d string, std bool) bool {
			return !std && !inPkg(d, modulePath+"/internal/ledger")
		},
	},
	{
		name:    "internal/notion importiert nur Standardbibliothek und internal/domain",
		applies: func(p string) bool { return inPkg(p, modulePath+"/internal/notion") },
		forbidden: func(d string, std bool) bool {
			return !std && !inPkg(d, modulePath+"/internal/domain") && !inPkg(d, modulePath+"/internal/notion")
		},
	},
	{
		name:    "internal/llm importiert nur Standardbibliothek und internal/domain",
		applies: func(p string) bool { return inPkg(p, modulePath+"/internal/llm") },
		forbidden: func(d string, std bool) bool {
			return !std && !inPkg(d, modulePath+"/internal/domain") && !inPkg(d, modulePath+"/internal/llm")
		},
	},
	{
		name:    "internal/notify importiert nur Standardbibliothek",
		applies: func(p string) bool { return inPkg(p, modulePath+"/internal/notify") },
		forbidden: func(d string, std bool) bool {
			return !std && !inPkg(d, modulePath+"/internal/notify")
		},
	},
}

type listedPkg struct {
	ImportPath   string
	Standard     bool
	Deps         []string
	TestImports  []string
	XTestImports []string
}

// goListPackages liest alle Pakete des Moduls samt transitiven Abhängigkeiten.
// go test stellt GOROOT/bin an den Anfang von PATH, "go" ist also die
// Toolchain, mit der der Test gebaut wurde. GOPROXY=off: kein Netz.
func goListPackages(t *testing.T) []listedPkg {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go-Binary nicht gefunden: %v", err)
	}
	cmd := exec.Command(goBin, "list", "-deps", "-json", "./...") //nolint:gosec // G204: festes Programm aus GOROOT/bin, feste Argumente, keine Shell
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=", "GOTOOLCHAIN=local")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []listedPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPkg
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("go list Ausgabe: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func archViolations(pkgs []listedPkg, rules []archRule) []string {
	known := map[string]bool{} // Importpfad → Standard (laut go list)
	for _, p := range pkgs {
		known[p.ImportPath] = p.Standard
	}
	// Test-Importe stehen nicht in -deps; dort gilt: kein Punkt im ersten
	// Pfadelement und nicht das Modul selbst = Standardbibliothek.
	isStd := func(d string) bool {
		if s, ok := known[d]; ok {
			return s
		}
		first, _, _ := strings.Cut(d, "/")
		return !strings.Contains(first, ".") && !inPkg(d, modulePath)
	}
	var v []string
	for _, p := range pkgs {
		deps := append(append(append([]string{}, p.Deps...), p.TestImports...), p.XTestImports...)
		for _, r := range rules {
			if !r.applies(p.ImportPath) {
				continue
			}
			for _, d := range deps {
				if r.forbidden(d, isStd(d)) {
					v = append(v, r.name+": "+p.ImportPath+" → "+d)
				}
			}
		}
	}
	return v
}

// Fängt: falsche Abhängigkeitsrichtung zwischen den Paketen (M2-Exit-Kriterium).
func TestArchDependencyDirection(t *testing.T) {
	pkgs := goListPackages(t)
	found := false
	for _, p := range pkgs {
		if p.ImportPath == modulePath+"/internal/domain" {
			found = true
		}
	}
	if !found {
		t.Fatalf("go list liefert %s/internal/domain nicht", modulePath)
	}
	for _, v := range archViolations(pkgs, archRules) {
		t.Error(v)
	}
}

// Fängt: Regeln, die nie anschlagen (z. B. falscher Präfix).
func TestArchRulesDetectViolations(t *testing.T) {
	pkgs := []listedPkg{
		{ImportPath: "errors", Standard: true},
		{ImportPath: "go.yaml.in/yaml/v3"},
		{ImportPath: modulePath + "/internal/domain", Deps: []string{"errors", modulePath + "/internal/llm", "go.yaml.in/yaml/v3"}, TestImports: []string{"testing"}},
		{ImportPath: modulePath + "/internal/notion", Deps: []string{modulePath + "/internal/domain", "go.yaml.in/yaml/v3"}, XTestImports: []string{modulePath + "/internal/pipeline"}},
		{ImportPath: modulePath + "/internal/pipeline", Deps: []string{modulePath + "/internal/domain", modulePath + "/internal/notion"}},
		{ImportPath: modulePath + "/internal/transcript", Deps: []string{"errors", modulePath + "/internal/domain", modulePath + "/internal/notion", "go.yaml.in/yaml/v3"}},
		{ImportPath: modulePath + "/internal/ledger", Deps: []string{"errors", modulePath + "/internal/domain", "go.yaml.in/yaml/v3"}},
		{ImportPath: modulePath + "/internal/llm", Deps: []string{"errors", modulePath + "/internal/domain", modulePath + "/internal/notion", "go.yaml.in/yaml/v3"}},
		{ImportPath: modulePath + "/internal/notify", Deps: []string{"os/exec", modulePath + "/internal/domain", "go.yaml.in/yaml/v3"}},
		{ImportPath: modulePath, Deps: []string{modulePath + "/internal/pipeline"}},
	}
	got := archViolations(pkgs, archRules)
	want := []string{
		archRules[0].name + ": " + modulePath + "/internal/domain → " + modulePath + "/internal/llm",
		archRules[0].name + ": " + modulePath + "/internal/domain → go.yaml.in/yaml/v3",
		archRules[1].name + ": " + modulePath + "/internal/notion → " + modulePath + "/internal/pipeline",
		archRules[4].name + ": " + modulePath + "/internal/notion → go.yaml.in/yaml/v3",
		archRules[4].name + ": " + modulePath + "/internal/notion → " + modulePath + "/internal/pipeline",
		archRules[2].name + ": " + modulePath + "/internal/transcript → " + modulePath + "/internal/notion",
		archRules[2].name + ": " + modulePath + "/internal/transcript → go.yaml.in/yaml/v3",
		archRules[3].name + ": " + modulePath + "/internal/ledger → " + modulePath + "/internal/domain",
		archRules[3].name + ": " + modulePath + "/internal/ledger → go.yaml.in/yaml/v3",
		archRules[5].name + ": " + modulePath + "/internal/llm → " + modulePath + "/internal/notion",
		archRules[5].name + ": " + modulePath + "/internal/llm → go.yaml.in/yaml/v3",
		archRules[6].name + ": " + modulePath + "/internal/notify → " + modulePath + "/internal/domain",
		archRules[6].name + ": " + modulePath + "/internal/notify → go.yaml.in/yaml/v3",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Verstöße =\n%s\nerwartet\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
