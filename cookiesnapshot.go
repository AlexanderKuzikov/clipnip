package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Снимок cookies для Chromium-профиля. Браузер держит Network\Cookies без
// sharing, поэтому при запущенном Chrome/Edge/Yandex yt-dlp не может даже
// скопировать базу. Снимок делается один раз при закрытом браузере, после
// чего ClipNip читает копию и работает при открытом браузере.
//
// Снимок содержит только Local State и зашифрованную DPAPI базу cookies:
// открытого текста на диск не попадает.

var (
	errCookieDBLocked      = errors.New("cookie database is locked by the browser")
	errSnapshotUnsupported = errors.New("cookie snapshot supports Chromium profiles only")
)

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

var snapshotMu sync.Mutex

// resolveCookieProfile превращает спецификацию yt-dlp в каталог профиля и
// каталог с Local State (у Chromium он уровнем выше профиля).
func resolveCookieProfile(spec string) (profileDir, localStateDir string, err error) {
	spec = strings.TrimSpace(spec)
	browser, rest, _ := strings.Cut(spec, ":")
	browser = strings.ToLower(strings.TrimSpace(browser))
	rest = strings.TrimSpace(rest)

	switch browser {
	case "chromium":
		if rest == "" {
			return "", "", errors.New("profile is not specified")
		}
		if filepath.IsAbs(rest) {
			profileDir = filepath.Clean(rest)
		} else {
			base := chromiumDirs("chromium")
			if base == "" {
				return "", "", errors.New("Chromium is not installed")
			}
			profileDir = filepath.Join(base, rest)
		}
	case "chrome", "edge", "brave", "vivaldi", "whale":
		if rest == "" {
			return "", "", errors.New("choose a browser profile first")
		}
		base := chromiumDirs(browser)
		if base == "" {
			return "", "", fmt.Errorf("%s is not installed", browser)
		}
		profileDir = filepath.Join(base, rest)
	case "opera":
		base := chromiumDirs("opera")
		if base == "" {
			return "", "", errors.New("Opera is not installed")
		}
		if rest == "" {
			profileDir = base
		} else {
			profileDir = filepath.Join(base, rest)
		}
	default:
		return "", "", fmt.Errorf("%w: %s", errSnapshotUnsupported, browser)
	}

	if !filepath.IsAbs(profileDir) {
		return "", "", errors.New("profile path must be absolute")
	}
	localStateDir = profileDir
	if !fileExists(filepath.Join(profileDir, "Local State")) {
		localStateDir = filepath.Dir(profileDir)
	}
	if !fileExists(filepath.Join(localStateDir, "Local State")) {
		return "", "", errors.New("Local State not found next to the profile")
	}
	if !fileExists(filepath.Join(profileDir, "Network", "Cookies")) {
		return "", "", errors.New("profile has no Cookies database")
	}
	return profileDir, localStateDir, nil
}

// snapshotCookieProfile копирует минимальный Chromium-профиль в каталог
// ClipNip и возвращает спецификацию chromium:<путь-к-снимку>.
func snapshotCookieProfile(spec string) (string, error) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	profileDir, localStateDir, err := resolveCookieProfile(spec)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(profileDir))))
	root := filepath.Join(localAppDataDir(), "cookies", hex.EncodeToString(sum[:8]))
	tmp := root + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "Default", "Network"), 0o700); err != nil {
		return "", err
	}
	cleanup := func() { os.RemoveAll(tmp) }

	if err := copyCookieFile(filepath.Join(localStateDir, "Local State"), filepath.Join(tmp, "Local State")); err != nil {
		cleanup()
		return "", snapshotCopyError(err)
	}
	for _, name := range []string{"Cookies", "Cookies-journal", "Cookies-wal", "Cookies-shm"} {
		src := filepath.Join(profileDir, "Network", name)
		if !fileExists(src) {
			continue
		}
		if err := copyCookieFile(src, filepath.Join(tmp, "Default", "Network", name)); err != nil {
			cleanup()
			return "", snapshotCopyError(err)
		}
	}

	os.RemoveAll(root)
	if err := os.Rename(tmp, root); err != nil {
		cleanup()
		return "", err
	}
	return "chromium:" + filepath.Join(root, "Default"), nil
}

func copyCookieFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func snapshotCopyError(err error) error {
	if errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation) {
		return fmt.Errorf("%w: close the browser and try again", errCookieDBLocked)
	}
	return err
}
