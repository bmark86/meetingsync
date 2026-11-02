package domain

import "net/url"

// RedactURL liefert von einer Basis-URL nur Schema://Host[:Port] – ohne Pfad,
// Query und Zugangsdaten – für Fehlertexte und Logzeilen (S-03).
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(ungültige URL)"
	}
	return u.Scheme + "://" + u.Host
}
