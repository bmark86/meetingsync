// Package secret liefert den Notion-Token und den LLM-API-Key. Einziger Ort mit
// Prozessstart für Geheimnisse (security, macOS-Schlüsselbund); die Config-Schicht
// bleibt ohne exec.
package secret

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// securityPath ist der feste Pfad von security (kein PATH-Lookup, Befund S-10).
const securityPath = "/usr/bin/security"

// TokenSource liefert einen Token oder den Grund, warum es keinen gibt.
type TokenSource interface {
	Token() (string, error)
}

// source sucht ein Geheimnis: Umgebungsvariable envName → Schlüsselbund → Config.
// label nennt das Geheimnis in Fehlertexten, missing ist der Fehler, wenn nichts
// gesetzt ist (nil: kein Geheimnis ist erlaubt). run führt ein Programm mit genau
// der Umgebung env aus und liefert stdout; Produktion ist runOutput, Tests setzen
// einen Fake.
type source struct {
	envName         string
	label           string
	missing         error
	keychainService string
	fileToken       string
	run             func(path string, env []string, args ...string) ([]byte, error)
}

// NewNotion liefert die TokenSource für Notion. keychainService und fileToken
// sind notion.token_keychain_service und notion.token aus der Config.
func NewNotion(keychainService, fileToken string) TokenSource {
	return &source{
		envName:         "NOTION_TOKEN",
		label:           "Notion-Token",
		missing:         errors.New("kein Notion-Token: NOTION_TOKEN setzen oder token_keychain_service bzw. token in der Config angeben"),
		keychainService: keychainService,
		fileToken:       fileToken,
		run:             runOutput,
	}
}

// NewLLM liefert die TokenSource für den LLM-API-Key. keychainService und
// fileKey sind llm.api_key_keychain_service und llm.api_key aus der Config.
// Ohne Key liefert Token "" und keinen Fehler (LM Studio braucht meist keinen).
func NewLLM(keychainService, fileKey string) TokenSource {
	return &source{
		envName:         "LLM_API_KEY",
		label:           "LLM-API-Key",
		keychainService: keychainService,
		fileToken:       fileKey,
		run:             runOutput,
	}
}

func runOutput(path string, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command(path, args...) //nolint:gosec // G204: einziger Aufrufer ist Token mit der Konstante securityPath; Argumente gehen nicht durch eine Shell
	cmd.Env = env
	return cmd.Output()
}

// minimalEnv liefert die Umgebung für security: HOME, USER und LANG aus dem
// eigenen Prozess (nur wenn gesetzt) und ein fester PATH. HOME und USER
// reichen, um den Anmelde-Schlüsselbund zu finden. Alles andere, etwa
// NOTION_TOKEN oder LLM_API_KEY, wird nicht weitergegeben.
func minimalEnv() []string {
	var env []string
	for _, k := range []string{"HOME", "USER", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, "PATH=/usr/bin:/bin")
}

// Token sucht das Geheimnis in dieser Reihenfolge:
// Umgebungsvariable (NOTION_TOKEN bzw. LLM_API_KEY) → macOS-Schlüsselbund → Config-Datei.
// Ist ein Schlüsselbund-Dienst gesetzt, gilt nur er: ein Fehler dort bricht ab,
// der Wert aus der Datei wird dann nicht benutzt. Fehlertexte nennen nie den Wert.
func (n *source) Token() (string, error) {
	if t := strings.TrimSpace(os.Getenv(n.envName)); t != "" {
		return t, nil
	}
	if s := strings.TrimSpace(n.keychainService); s != "" {
		out, err := n.run(securityPath, minimalEnv(), "find-generic-password", "-s", s, "-w")
		if err != nil {
			return "", fmt.Errorf("%s aus dem Schlüsselbund (%s) nicht lesbar: %w", n.label, s, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if t := strings.TrimSpace(n.fileToken); t != "" {
		return t, nil
	}
	return "", n.missing
}
