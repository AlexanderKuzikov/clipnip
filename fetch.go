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

var (
	componentsMu       sync.Mutex
	componentStates    []componentState
	componentsProblem  string
	componentsResolved bool
)

// componentsSnapshot отдаёт согласованную копию состояния для HTTP-слоя.
// componentStates меняется только под componentsMu: раньше чтение и запись
// шли параллельно, а resolve запускался из каждого воркера.
func componentsSnapshot() ([]componentState, bool, []string, string) {
	componentsMu.Lock()
	defer componentsMu.Unlock()
	states := append([]componentState(nil), componentStates...)
	return states, componentsReadyLocked(requiredComponents()),
		missingComponentsLocked(requiredComponents()), componentsProblem
}

func componentsProblemText() string {
	componentsMu.Lock()
	defer componentsMu.Unlock()
	return componentsProblem
}

// ensureBins разрешает компоненты один раз за процесс. Повторные вызовы из
// каждого скачивания больше не пересчитывают SHA-256 всех файлов и не ходят
// за манифестом: это было и медленно, и небезопасно при параллельных воркерах.
func ensureBins() error {
	componentsMu.Lock()
	defer componentsMu.Unlock()
	if componentsResolved {
		return nil
	}
	if err := resolveComponentsLocked(); err != nil {
		return err
	}
	componentsResolved = true
	return nil
}

// refreshComponents — явная перепроверка (кнопка Apply в настройках).
// Сбрасывает кэш probeEngine, иначе /api/engine до перезапуска показывал бы
// «движка нет», даже когда компоненты уже скачаны.
func refreshComponents() error {
	componentsMu.Lock()
	defer componentsMu.Unlock()
	componentsResolved = false
	if err := resolveComponentsLocked(); err != nil {
		return err
	}
	componentsResolved = true
	resetEngineProbe()
	return nil
}

// resolveComponentsLocked вызывается под componentsMu.
func resolveComponentsLocked() error {
	needed := append(requiredComponents(), optionalComponents()...)
	base := componentsBaseURL()
	portable := portableBinDir()

	cache, err := binDir()
	if err != nil {
		return err
	}
	os.MkdirAll(cache, 0o755)

	// Если portable-папка или встроенные копии содержат всё нужное, сеть не
	// трогаем вообще: локальная поставка важнее свежести движка, а лишний
	// десятисекундный таймаут манифеста на старте никому не нужен.
	allLocal := portable != ""
	if allLocal {
		for _, name := range needed {
			if _, err := os.Stat(filepath.Join(portable, name)); err != nil {
				allLocal = false
				break
			}
		}
	}
	if !allLocal {
		allLocal = true
		for _, name := range needed {
			b, ok := embeddedByName(name)
			if !ok || b.gz == "" {
				allLocal = false
				break
			}
		}
	}

	var mf *manifest
	if allowRemoteFetch && base != "" && !allLocal {
		loaded, err := loadManifest(base)
		if err != nil {
			log.Printf("components: manifest unavailable: %v", err)
		} else {
			mf = loaded
		}
	}

	states := make([]componentState, 0, len(needed))
	paths := make(map[string]string, len(needed))
	for _, name := range needed {
		st := resolveComponent(name, portable, cache, mf, base)
		if st.Source != "absent" && st.Path != "" {
			paths[name] = st.Path
		}
		states = append(states, st)
	}

	resolvedPaths.Lock()
	resolvedPaths.m = paths
	resolvedPaths.Unlock()

	componentStates = states
	if missing := missingComponentsLocked(requiredComponents()); len(missing) > 0 {
		how := "Положите их в папку clipnip-bin рядом с exe"
		if allowRemoteFetch {
			how = "Укажите адрес сервера в настройках или положите их в папку clipnip-bin рядом с exe"
		}
		componentsProblem = "Не удалось получить компоненты: " + strings.Join(missing, ", ") + ". " + how
		log.Printf("components: %s", componentsProblem)
	} else {
		componentsProblem = ""
	}
	return nil
}

func embeddedByName(name string) (binSpec, bool) {
	for _, b := range embeddedBins {
		if b.name == name {
			return b, true
		}
	}
	return binSpec{}, false
}

// resolveComponent выбирает источник по порядку: portable → встроенная копия →
// кэш/сервер. Пустой Source означает, что компонента нет.
func resolveComponent(name, portable, cache string, mf *manifest, base string) componentState {
	st := componentState{Name: name}
	cacheTrusted := true

	// 1. portable — доверяем как есть: это файлы рядом с exe, положенные
	//    туда человеком вместе с приложением.
	if portable != "" {
		p := filepath.Join(portable, name)
		if _, err := os.Stat(p); err == nil {
			st.Source, st.Path = "portable", p
			if sum, err := fileSHA256(p); err == nil {
				st.SHA256 = sum
			}
			return st
		}
	}

	// 2. встроенная копия (автономная сборка). При пустом gz компонент
	//    этой сборкой не вшит — ветка пропускается целиком.
	if b, ok := embeddedByName(name); ok && b.gz != "" {
		dest := filepath.Join(cache, name)
		if b.sha256 != "" {
			if sum, err := fileSHA256(dest); err == nil && sum == b.sha256 {
				st.Source, st.Path, st.SHA256, st.Version = "cache", dest, sum, b.version
				return st
			}
			if err := extractEmbedded(cache, b); err == nil {
				st.Source, st.Path, st.SHA256, st.Version = "embed", dest, b.sha256, b.version
				return st
			}
			// Встроенная копия — эталон. Если распаковать не удалось, кэш с
			// несовпавшим хешем доверять нельзя: только сервер может исправить.
			cacheTrusted = false
			log.Printf("components: %s extract failed, falling back to cache/server", name)
		} else if _, err := os.Stat(dest); err == nil {
			st.Source, st.Path = "cache", dest
			if sum, err := fileSHA256(dest); err == nil {
				st.SHA256 = sum
			}
			return st
		}
	}

	// portable-сборка не использует ни кэш, ни сервер: только папка рядом.
	if !allowRemoteFetch {
		return st
	}

	dest := filepath.Join(cache, name)

	// 3. сервер: нужен манифест с хешем, иначе качать нечего — принять
	//    исполняемый файл без проверки нельзя. Если манифеста нет, но файл
	//    уже лежит в кэше, работаем офлайн: он был проверен при установке.
	if mf != nil {
		if mc, ok := mf.find(name); ok {
			if sum, err := fileSHA256(dest); err == nil && sum == mc.SHA256 {
				st.Source, st.Path, st.SHA256, st.Version = "cache", dest, sum, mc.Version
				return st
			}
			if err := downloadComponent(base, mc, dest); err == nil {
				log.Printf("components: %s %s downloaded from %s", name, mc.Version, base)
				st.Source, st.Path, st.SHA256, st.Version = "download", dest, mc.SHA256, mc.Version
				return st
			} else {
				log.Printf("components: %s download failed: %v", name, err)
			}
			if _, err := os.Stat(dest); err == nil && cacheTrusted {
				st.Source, st.Path, st.Version = "cache", dest, mc.Version
				if sum, err := fileSHA256(dest); err == nil {
					st.SHA256 = sum
				}
				return st
			}
			return st
		}
		log.Printf("components: %s is not in the manifest", name)
	}

	if _, err := os.Stat(dest); err == nil && cacheTrusted {
		st.Source, st.Path = "cache", dest
		if sum, err := fileSHA256(dest); err == nil {
			st.SHA256 = sum
		}
		return st
	}
	return st
}

func (m *manifest) find(name string) (manifestComponent, bool) {
	for _, c := range m.Components {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return manifestComponent{}, false
}

// manifestFetchTimeout — короткий таймаут: манифест нужен на старте, и
// недоступный сервер не должен задерживать запуск на полминуты.
const manifestFetchTimeout = 10 * time.Second

func loadManifest(base string) (*manifest, error) {
	ref, err := url.Parse(base)
	if err != nil || ref.Scheme != "https" || ref.Host == "" {
		return nil, fmt.Errorf("components URL must be https")
	}
	client := &http.Client{Timeout: manifestFetchTimeout}
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
	if err := validateManifest(&mf); err != nil {
		return nil, err
	}
	return &mf, nil
}

func validateManifest(mf *manifest) error {
	if mf.Version != manifestVersion {
		return fmt.Errorf("unsupported manifest version %d", mf.Version)
	}
	for _, c := range mf.Components {
		if c.Name == "" || c.File == "" {
			return fmt.Errorf("malformed manifest entry for %q", c.Name)
		}
		if raw, err := hex.DecodeString(c.SHA256); err != nil || len(raw) != 32 {
			return fmt.Errorf("malformed manifest entry for %q", c.Name)
		}
	}
	return nil
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
	// Размер из манифеста не даёт серверу заполнить диск бесконечным потоком;
	// +1 байт — чтобы отличить «ровно Size» от «больше Size».
	var body io.Reader = resp.Body
	if mc.Size > 0 {
		body = io.LimitReader(resp.Body, mc.Size+1)
	}
	n, err := io.Copy(io.MultiWriter(out, h), body)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if mc.Size > 0 && n > mc.Size {
		os.Remove(tmp)
		return fmt.Errorf("component is larger than the manifest says: %d > %d", n, mc.Size)
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
	componentsMu.Lock()
	defer componentsMu.Unlock()
	return componentsReadyLocked(needed)
}

func componentsReadyLocked(needed []string) bool {
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
	componentsMu.Lock()
	defer componentsMu.Unlock()
	return missingComponentsLocked(needed)
}

func missingComponentsLocked(needed []string) []string {
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
