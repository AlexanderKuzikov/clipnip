package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Разрешение компонентов (yt-dlp, ffmpeg, deno) из трёх источников по порядку:
//
//  1. portable-папка рядом с exe — для переносимой поставки, без сети;
//  2. кэш в %LOCALAPPDATA%\clipnip\bin — то, что уже скачано;
//  3. сервер пользователя — загрузка с проверкой SHA-256 из манифеста.
//
// Порядок не случаен: офлайн-поставка важнее свежести, а кэш важнее сети.
// Вшитые в exe копии (сборка по умолчанию) работают как четвёртый, самый
// последний источник — см. embeddedBins.

// manifestVersion — текущая версия формата манифеста. Неизвестная версия
// отвергается: лучше не работать, чем поставить не то.
const manifestVersion = 1

// manifestTimeout — загрузка компонента (до 100 МБ) плюс запас.
const manifestTimeout = 10 * time.Minute

type manifestComponent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type manifest struct {
	Version    int                 `json:"version"`
	Components []manifestComponent `json:"components"`
}

var errNoSource = errors.New("component source not configured")

// componentsBaseURL — адрес, откуда тянутся компоненты. Задаётся в настройках
// (components_url) или переменной окружения CLIPNIP_BIN_URL. Пусто — работаем
// только с тем, что уже есть на диске.
func componentsBaseURL() string {
	config.RLock()
	u := strings.TrimSpace(config.ComponentsURL)
	config.RUnlock()
	if u == "" {
		u = strings.TrimSpace(os.Getenv("CLIPNIP_BIN_URL"))
	}
	return strings.TrimRight(u, "/")
}

// portableBinDir — папка компонентов рядом с exe. Если она есть, приложение
// полностью переносимое: сеть не нужна ни при первом запуске, ни при
// обновлении (обновление — замена файлов в этой папке).
func portableBinDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(exe), "clipnip-bin")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir
	}
	return ""
}

// resolvedPaths — где реально лежит каждый компонент после resolveComponents.
// Раньше вызовы yt-dlp строили путь в кэше напрямую, поэтому portable-папка
// находилась, но запускалась пустая кэш-версия.
var resolvedPaths = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// componentPath — фактический путь к компоненту. Пусто, если не разрешён.
func componentPath(name string) string {
	resolvedPaths.Lock()
	defer resolvedPaths.Unlock()
	return resolvedPaths.m[name]
}

func setComponentPath(name, path string) {
	resolvedPaths.Lock()
	resolvedPaths.m[name] = path
	resolvedPaths.Unlock()
}

// ytdlpPath — путь к движку, откуда он реально будет запущен.
func ytdlpPath() string {
	if p := componentPath("yt-dlp.exe"); p != "" {
		return p
	}
	dir, err := binDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "yt-dlp.exe")
}

// componentState — что известно о компоненте после разрешения.
type componentState struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"` // portable | cache | download | embed | absent
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
}

var componentStates []componentState

// resolveComponents готовит компоненты к работе. Для каждого выбирается первый
// доступный источник; скачивание происходит только если ничего не найдено.
func resolveComponents(needed []string) error {
	componentStates = nil
	resolvedPaths.Lock()
	resolvedPaths.m = map[string]string{}
	resolvedPaths.Unlock()
	base := componentsBaseURL()
	portable := portableBinDir()

	var mf *manifest
	if base != "" {
		loaded, err := loadManifest(base)
		if err != nil {
			log.Printf("components: manifest unavailable: %v", err)
		} else {
			mf = loaded
		}
	}

	cache, err := binDir()
	if err != nil {
		return err
	}
	os.MkdirAll(cache, 0o755)

	for _, name := range needed {
		st := componentState{Name: name}

		// 1. portable — доверяем как есть: это файлы рядом с exe, положенные
		//    туда человеком вместе с приложением.
		if portable != "" {
			p := filepath.Join(portable, name)
			if _, err := os.Stat(p); err == nil {
				st.Source, st.Path = "portable", p
				if sum, err := fileSHA256(p); err == nil {
					st.SHA256 = sum
				}
				setComponentPath(name, st.Path)
				componentStates = append(componentStates, st)
				continue
			}
		}

		// 2. встроенная копия (автономная сборка). При пустом gz компонент
		//    этой сборкой не вшит — ветка пропускается целиком.
		for _, b := range embeddedBins {
			if b.name != name || b.gz == "" {
				continue
			}
			dest := filepath.Join(cache, name)
			if b.sha256 != "" {
				if sum, err := fileSHA256(dest); err == nil && sum == b.sha256 {
					st.Source, st.Path, st.SHA256, st.Version = "cache", dest, sum, b.version
					break
				}
				// кэша нет или он не совпадает — распаковываем из embed
				if err := extractEmbedded(cache, b); err != nil {
					log.Printf("components: %s extract failed: %v", name, err)
					break
				}
				st.Source, st.Path, st.SHA256, st.Version = "embed", dest, b.sha256, b.version
				break
			}
			if _, err := os.Stat(dest); err == nil {
				st.Source, st.Path = "cache", dest
				break
			}
		}
		if st.Source != "" {
			setComponentPath(name, st.Path)
			componentStates = append(componentStates, st)
			continue
		}

		// 3. сервер: нужен манифест с хешем, иначе качать нечего —
		//    принять исполняемый файл без проверки нельзя.
		if mf == nil {
			componentStates = append(componentStates, st) // absent
			continue
		}
		mc, ok := mf.find(name)
		if !ok {
			log.Printf("components: %s is not in the manifest", name)
			setComponentPath(name, st.Path)
			componentStates = append(componentStates, st)
			continue
		}
		dest := filepath.Join(cache, name)
		if sum, err := fileSHA256(dest); err == nil && sum == mc.SHA256 {
			st.Source, st.Path, st.SHA256, st.Version = "cache", dest, sum, mc.Version
			setComponentPath(name, st.Path)
			componentStates = append(componentStates, st)
			continue
		}
		if err := downloadComponent(base, mc, dest); err != nil {
			log.Printf("components: %s download failed: %v", name, err)
			setComponentPath(name, st.Path)
			componentStates = append(componentStates, st)
			continue
		}
		log.Printf("components: %s %s downloaded from %s", name, mc.Version, base)
		st.Source, st.Path, st.SHA256, st.Version = "download", dest, mc.SHA256, mc.Version
		setComponentPath(name, st.Path)
		componentStates = append(componentStates, st)
	}
	return nil
}

func (m *manifest) find(name string) (manifestComponent, bool) {
	for _, c := range m.Components {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return manifestComponent{}, false
}

func loadManifest(base string) (*manifest, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(base + "/manifest.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest HTTP %d", resp.StatusCode)
	}
	// манифест ограниченного размера: защита от мусора вместо json
	var mf manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&mf); err != nil {
		return nil, err
	}
	if mf.Version != manifestVersion {
		return nil, fmt.Errorf("unsupported manifest version %d", mf.Version)
	}
	for _, c := range mf.Components {
		if c.Name == "" || c.File == "" || len(c.SHA256) != 64 {
			return nil, fmt.Errorf("malformed manifest entry for %q", c.Name)
		}
	}
	return &mf, nil
}

// downloadComponent кладёт файл в кэш атомарно: сначала во временный, затем
// сверка хеша и только потом rename. Повреждённая загрузка никогда не
// становится рабочим бинарником.
func downloadComponent(base string, mc manifestComponent, dest string) error {
	ref, err := url.Parse(base)
	if err != nil {
		return err
	}
	// Без TLS подменять исполняемый файл нельзя: манифест с хешем приезжает
	// по тому же каналу, что и файл, и сверка ничего не значит.
	if ref.Scheme != "https" {
		return fmt.Errorf("components URL must be https, got %q", ref.Scheme)
	}
	fileURL, err := url.JoinPath(base, mc.File)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: manifestTimeout}
	return fetchAndVerify(client, fileURL, dest, mc)
}

// fetchAndVerify — сама механика: скачать, посчитать хеш на лету, и только при
// совпадении переместить на место. Вынесена отдельно от проверки схемы, чтобы
// её можно было проверить тестом на обычном httptest-сервере.
func fetchAndVerify(client *http.Client, fileURL, dest string, mc manifestComponent) error {
	resp, err := client.Get(fileURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), resp.Body)
	out.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != mc.SHA256 {
		// несовпадение не оставляет ни файла, ни временного мусора
		os.Remove(tmp)
		return fmt.Errorf("hash mismatch: got %s, want %s", got, mc.SHA256)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	if mc.Version != "" {
		os.WriteFile(dest+".ver", []byte(mc.Version), 0o644)
	}
	return nil
}

// componentsReady — все нужные компоненты на месте.
func componentsReady(needed []string) bool {
	for _, n := range needed {
		found := false
		for _, st := range componentStates {
			if st.Name == n && st.Source != "" && st.Source != "absent" {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// missingComponents — чего не хватает, для сообщения пользователю.
func missingComponents(needed []string) []string {
	var out []string
	for _, n := range needed {
		found := false
		for _, st := range componentStates {
			if st.Name == n && st.Source != "" && st.Source != "absent" {
				found = true
				break
			}
		}
		if !found {
			out = append(out, n)
		}
	}
	return out
}
