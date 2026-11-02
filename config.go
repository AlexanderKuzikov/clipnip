package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// cookiesSpecRe — белый список для спецификации браузера. Пропускает
// `chrome`, `edge:Profile 1` и `chromium:C:\Users\u\AppData\...\Default`,
// но отсекает всё, что yt-dlp принял бы за флаг.
var cookiesSpecRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._:\\/()-]*$`)

type Config struct {
	DownloadDir string `json:"download_dir"`

	// Cookies: явный opt-in. Спецификация — формат самого yt-dlp
	// (`chrome`, `edge:Profile 1`, `chromium:C:\...\Default`), поэтому любой
	// Chromium-форк читается через указание пути, даже если yt-dlp не знает
	// его по имени. Ничего из этого не пишется в лог.
	CookiesEnabled bool   `json:"cookies_enabled"`
	CookiesSpec    string `json:"cookies_spec"`
	CookiesFile    string `json:"cookies_file"`
	CookiesAck     bool   `json:"cookies_ack"` // предупреждение показано

	// ComponentsURL — откуда тянутся yt-dlp/ffmpeg/deno. Пусто — работаем
	// только с тем, что уже лежит на диске (portable-папка, кэш или вшитая
	// в сборку копия). Требуется https.
	ComponentsURL string `json:"components_url"`
}

var config = struct {
	sync.RWMutex
	Config
}{}

// configDirOverride — переопределение каталога конфига. Нужно тестам:
// без этого юнит-тесты писали бы в реальный config.json пользователя
// (и затирали download_dir). В проде остаётся пустым.
var configDirOverride string

// dataDir — каталог рантайм-данных. Уважает configDirOverride: тесты не
// должны писать ни в конфиг, ни в очередь реального пользователя.
func dataDir() (string, error) {
	if configDirOverride != "" {
		return configDirOverride, nil
	}
	// CLIPNIP_DATA — отдельный рантайм-каталог для headless-прогонов:
	// диагностика не должна трогать конфиг и очередь пользователя.
	if dir := strings.TrimSpace(os.Getenv("CLIPNIP_DATA")); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "clipnip"), nil
}

func configPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func localAppDataDir() string {
	dir, _ := dataDir()
	return dir
}

func loadConfig() {
	path, err := configPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		log.Printf("config: %v; using defaults", err)
		return
	}
	config.Lock()
	config.Config = c
	config.Unlock()
}

func saveConfig() {
	path, err := configPath()
	if err != nil {
		return
	}
	config.RLock()
	data, err := json.MarshalIndent(config.Config, "", "  ")
	config.RUnlock()
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	os.Rename(tmp, path)
}

func getDownloadDir() string {
	config.RLock()
	dir := config.DownloadDir
	config.RUnlock()
	return dir
}

func setDownloadDir(dir string) {
	config.Lock()
	config.DownloadDir = dir
	config.Unlock()
	saveConfig()
}

// cookiesSpec возвращает валидную спецификацию браузера для yt-dlp.
// Пустая строка, если cookies выключены или значение не проходит проверку:
// спецификация попадает в argv, поэтому мусор в config.json не должен
// превращаться в аргументы yt-dlp.
func cookiesSpec() string {
	config.RLock()
	enabled, spec := config.CookiesEnabled, strings.TrimSpace(config.CookiesSpec)
	config.RUnlock()
	if !enabled || spec == "" || !cookiesSpecRe.MatchString(spec) {
		return ""
	}
	return spec
}

// cookiesFilePath — путь к cookies.txt, если он задан, существует и
// cookies включены. Значения самих cookies в приложение не попадают.
func cookiesFilePath() string {
	config.RLock()
	enabled, file := config.CookiesEnabled, strings.TrimSpace(config.CookiesFile)
	config.RUnlock()
	if !enabled || file == "" || !fileExists(file) {
		return ""
	}
	return file
}

func setCookies(enabled bool, spec, file string, ack bool) error {
	config.Lock()
	config.CookiesEnabled = enabled
	config.CookiesSpec = strings.TrimSpace(spec)
	config.CookiesFile = strings.TrimSpace(file)
	if ack {
		config.CookiesAck = true
	}
	config.Unlock()
	saveConfig()
	return nil
}
