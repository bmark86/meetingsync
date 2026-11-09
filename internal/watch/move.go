package watch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Mover verschiebt Dateien ohne Überschreiben (Move). Der Nullwert ist die
// echte Implementierung; die Felder sind Nähte nur für Tests (nil: echt).
type Mover struct {
	// Rename benennt exklusiv um; nil: renamex_np mit RENAME_EXCL.
	Rename func(src, dst string) error
	// Copy kopiert den Inhalt; nil: io.Copy.
	Copy func(dst io.Writer, src io.Reader) (int64, error)
	// SyncDir synchronisiert den Zielordner; nil: fsync des Ordners.
	SyncDir func(dir string) error
}

func (m Mover) rename(src, dst string) error {
	if m.Rename != nil {
		return m.Rename(src, dst)
	}
	return renameExcl(src, dst)
}

func (m Mover) copy(dst io.Writer, src io.Reader) (int64, error) {
	if m.Copy != nil {
		return m.Copy(dst, src)
	}
	return io.Copy(dst, src)
}

func (m Mover) syncDir(dir string) error {
	if m.SyncDir != nil {
		return m.SyncDir(dir)
	}
	return syncDir(dir)
}

// renameExcl benennt src exklusiv in dst um (renamex_np mit RENAME_EXCL):
// atomar, ein vorhandenes dst bleibt unangetastet (EEXIST).
func renameExcl(src, dst string) error {
	if err := unix.RenamexNp(src, dst, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "renamex_np", Old: src, New: dst, Err: err}
	}
	return nil
}

// DirSyncError meldet: Kopie liegt in dst, das Original ist gelöscht, nur der Sync
// des Zielordners schlug fehl. Move liefert dst mit diesem Fehler.
type DirSyncError struct {
	dst string
	err error
}

func (e *DirSyncError) Error() string {
	return fmt.Sprintf("Zielordner nicht synchronisiert, trotzdem verschoben nach %s: %v", e.dst, e.err)
}

func (e *DirSyncError) Unwrap() error { return e.err }

// Move verschiebt src nach dir, ohne vorhandene Dateien zu überschreiben.
// Gleiches Volume: atomares Umbenennen mit RENAME_EXCL (ein Symlink wird als
// Symlink verschoben). Anderes Volume oder Flag nicht unterstützt: Kopie mit
// O_EXCL, Zielordner sync, dann src löschen, aber nur, wenn src noch die
// kopierte Datei ist. Ist der Name belegt, "stem (n).ext". Gelöscht wird
// außer src nur die eigene, eben angelegte Kopie (von Mark erlaubt,
// 2026-11-02), und nur nach Identitätsprüfung. Scheitert nur der Sync des
// Zielordners, geht es weiter wie sonst; dann kommen dst und ein
// *DirSyncError zurück (Mark, 2026-11-02).
func (m Mover) Move(src, dir string) (string, error) {
	if err := mkdirPrivate(dir); err != nil {
		return "", err
	}
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	name := func(i int) string {
		if i == 1 {
			return filepath.Join(dir, base)
		}
		return filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	i := 1
	for ; ; i++ {
		err := m.rename(src, name(i))
		if err == nil {
			return name(i), nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.ENOTSUP) {
			return "", err
		}
		break // anderes Volume oder RENAME_EXCL nicht unterstützt: kopieren
	}
	for ; ; i++ {
		dst := name(i)
		srcInfo, dstInfo, err := m.copyExcl(src, dst)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		// Ordnereintrag der Kopie soll einen Absturz überstehen, bevor src weg ist.
		// Scheitert das, trotzdem weiter (Kopie selbst ist synchronisiert).
		syncErr := m.syncDir(dir)
		cur, err := os.Lstat(src)
		if err != nil || !os.SameFile(cur, srcInfo) {
			msg := fmt.Sprintf("Quelle %s während des Kopierens ersetzt oder entfernt, Quelle und Kopie %s bleiben liegen", src, dst)
			if err != nil {
				msg += fmt.Sprintf(" (%v)", err)
			}
			return "", errors.New(msg)
		}
		if err := os.Remove(src); err != nil {
			// Keine Kopie je Versuch ansammeln: nur die eigene, frische Kopie löschen,
			// einmal und ohne Wiederholung.
			if rerr := removeOwn(dst, dstInfo); rerr != nil {
				return "", fmt.Errorf("Quelle nicht löschbar, Original bleibt liegen: %w; eigene Kopie %s nicht entfernt: %v", err, dst, rerr)
			}
			return "", fmt.Errorf("Quelle nicht löschbar, Original bleibt liegen, eigene Kopie entfernt: %w", err)
		}
		if syncErr != nil {
			return dst, &DirSyncError{dst: dst, err: syncErr}
		}
		return dst, nil
	}
}

// copyExcl kopiert src nach dst; dst darf noch nicht existieren (O_EXCL).
// Sync vor Close, damit die Kopie vor dem Löschen der Quelle auf Platte ist.
// Liefert die Identität von Quelle und Kopie. Schlägt das Kopieren fehl,
// löscht copyExcl die eigene Teilkopie (von Mark erlaubt, 2026-11-02).
func (m Mover) copyExcl(src, dst string) (srcInfo, dstInfo os.FileInfo, err error) {
	// O_NOFOLLOW: ein Symlink wird nicht geöffnet (ELOOP); O_NONBLOCK: ein FIFO
	// blockiert nicht, die Prüfung unten weist ihn ab.
	in, err := os.OpenFile(src, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0) //nolint:gosec // G304: Pfad aus dem Exportordner, O_NOFOLLOW und Prüfung auf reguläre Datei unten
	if errors.Is(err, unix.ELOOP) {
		return nil, nil, fmt.Errorf("Quelle %s ist keine reguläre Datei oder wurde ersetzt, nicht kopiert", src)
	}
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = in.Close() }() //nolint:errcheck // nur gelesen, Close-Fehler ohne Folgen
	if srcInfo, err = in.Stat(); err != nil {
		return nil, nil, err
	}
	// Nur eine reguläre Datei, die noch unter src liegt (kein Symlink): sonst
	// würde die Kopie bei jedem Versuch neu angelegt und bliebe liegen.
	li, err := os.Lstat(src)
	if err != nil {
		return nil, nil, err
	}
	if !li.Mode().IsRegular() || !os.SameFile(li, srcInfo) {
		return nil, nil, fmt.Errorf("Quelle %s ist keine reguläre Datei oder wurde ersetzt, nicht kopiert", src)
	}

	// Kopie 0600 (S-12); O_EXCL: die Datei ist die eigene, Chmod unabhängig von der umask.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: Pfad aus Move
	if err != nil {
		return nil, nil, err
	}
	dstInfo, err = out.Stat()
	if err == nil {
		err = out.Chmod(0o600)
	}
	if err == nil {
		_, err = m.copy(out, in)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if rerr := removeOwn(dst, dstInfo); rerr != nil {
			return nil, nil, fmt.Errorf("Kopie nach %s fehlgeschlagen: %w; Teilkopie nicht entfernt: %v", dst, err, rerr)
		}
		return nil, nil, fmt.Errorf("Kopie nach %s fehlgeschlagen, Teilkopie entfernt: %w", dst, err)
	}
	return srcInfo, dstInfo, nil
}

// removeOwn löscht path nur, wenn es noch die selbst angelegte Datei own ist.
func removeOwn(path string, own os.FileInfo) error {
	if own == nil {
		return errors.New("Identität der eigenen Kopie unbekannt")
	}
	cur, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(cur, own) {
		return fmt.Errorf("%s ist nicht mehr die eigene Kopie", path)
	}
	return os.Remove(path)
}

// mkdirPrivate legt dir samt fehlender Elternordner an wie os.MkdirAll.
// Nur die hier neu angelegten Ordner bekommen 0700, unabhängig von der umask
// (S-12); vorhandene behalten ihre Rechte (OD-10). Gleiche Funktion in
// internal/ledger (dort nur Standardbibliothek erlaubt).
func mkdirPrivate(dir string) error {
	var missing []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err == nil {
			if !fi.IsDir() {
				return &os.PathError{Op: "mkdir", Path: d, Err: unix.ENOTDIR}
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		err := os.Mkdir(missing[i], 0o700)
		if errors.Is(err, os.ErrExist) {
			continue // inzwischen von anderer Seite angelegt: Rechte nicht anfassen
		}
		if err != nil {
			return err
		}
		if err := os.Chmod(missing[i], 0o700); err != nil { //nolint:gosec // G302: Ordner, 0700 nötig zum Betreten; nur der eben angelegte
			return err
		}
	}
	// wie os.MkdirAll: der Pfad muss am Ende ein Ordner sein (folgt Symlinks)
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return &os.PathError{Op: "mkdir", Path: dir, Err: unix.ENOTDIR}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: Zielordner aus der eigenen Konfiguration, nur für fsync geöffnet
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// WriteFehlerTxt legt dst.fehler.txt mit Zeitpunkt at und Ursache an, ohne
// einen vorhandenen Hinweis zu überschreiben; ist der Name belegt,
// "dst (n).fehler.txt".
func WriteFehlerTxt(dst string, at time.Time, cause error) error {
	data := []byte(fmt.Sprintf("%s\n%v\n", at.Format(time.RFC3339), cause))
	name := dst + ".fehler.txt"
	for i := 2; ; i++ {
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: Pfad aus Mover.Move unter failed_dir
		if errors.Is(err, os.ErrExist) {
			name = fmt.Sprintf("%s (%d).fehler.txt", dst, i)
			continue
		}
		if err != nil {
			return err
		}
		// 0600 (S-12); O_EXCL: die Datei ist die eigene, Chmod unabhängig von der umask.
		err = f.Chmod(0o600)
		if err == nil {
			_, err = f.Write(data)
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
}
