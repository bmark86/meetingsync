// Package secret liefert den Notion-Token. Einziger Ort mit Prozessstart für
// Geheimnisse (security, macOS-Schlüsselbund); die Config-Schicht bleibt ohne exec.
package secret

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TokenSource liefert einen Token oder den Grund, warum es keinen gibt.
type TokenSource interface {
	Token() (string, error)
}

// notionToken sucht den Notion-Token. run führt ein Programm aus und liefert
// stdout; Produktion ist exec.Command(...).Output, Tests setzen einen Fake.
type notionToken struct {
	keychainService string
	fileToken       string
	run             func(name string, args ...string) ([]byte, error)
}

// NewNotion liefert die TokenSource für Notion. keychainService und fileToken
// sind notion.token_keychain_service und notion.token aus der Config.
func NewNotion(keychainService, fileToken string) TokenSource {
	return &notionToken{keychainService: keychainService, fileToken: fileToken, run: runOutput}
}

func runOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Token sucht den Token in dieser Reihenfolge:
// Umgebungsvariable NOTION_TOKEN → macOS-Schlüsselbund → Config-Datei.
// Ist ein Schlüsselbund-Dienst gesetzt, gilt nur er: ein Fehler dort bricht ab,
// der Token aus der Datei wird dann nicht benutzt.
func (n *notionToken) Token() (string, error) {
	if t := strings.TrimSpace(os.Getenv("NOTION_TOKEN")); t != "" {
		return t, nil
	}
	if s := strings.TrimSpace(n.keychainService); s != "" {
		out, err := n.run("security", "find-generic-password", "-s", s, "-w")
		if err != nil {
			return "", fmt.Errorf("Notion-Token aus dem Schlüsselbund (%s) nicht lesbar: %w", s, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if t := strings.TrimSpace(n.fileToken); t != "" {
		return t, nil
	}
	return "", errors.New("kein Notion-Token: NOTION_TOKEN setzen oder token_keychain_service bzw. token in der Config angeben")
}
