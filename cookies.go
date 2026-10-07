package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Детекция браузеров для --cookies-from-browser.
//
// Пути и правила взяты из исходников yt-dlp (yt_dlp/cookies.py), а не
// выдуманы здесь: список поддерживаемых имён — brave, chrome, chromium,
// edge, opera, vivaldi, whale (все Chromium) и firefox. Специально НЕ
// перечисляем другие форки (Yandex.Browser, CentBrowser, Iridium и т.д.):
// спецификация yt-dlp принимает путь вместо имени, поэтому для любого
// Chromium-браузера работает `chromium:<путь-к-профилю>` — и поддерживать
// их поимённо не нужно.

type browserInfo struct {
	Name     string        `json:"name"`  // имя спецификации yt-dlp
	Label    string        `json:"label"` // человеческое название
	Dir      string        `json:"dir"`   // каталог User Data / профилей
	Found    bool          `json:"found"` // найден на диске
	NoProf   bool          `json:"no_profiles"`
	Profiles []profileInfo `json:"profiles"`
	// Suggest — спецификация, которую имеет смысл подставить по умолчанию
	Suggest string `json:"suggest"`
}

type profileInfo struct {
	Name  string `json:"name"`
	Spec  string `json:"spec"` // `chrome:Profile 1` или путь для chromium:<path>
	MTime string `json:"mtime"`
}

// order — предпочтение при автоподстановке: самые частые Chromium-браузеры.
var cookieBrowsers = []struct{ name, label string }{
	{"chrome", "Google Chrome"},
	{"edge", "Microsoft Edge"},
	{"firefox", "Firefox"},
	{"brave", "Brave"},
	{"vivaldi", "Vivaldi"},
	{"opera", "Opera"},
	{"chromium", "Chromium"},
	{"whale", "Naver Whale"},
}

func envDir(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return ""
}

// chromiumDirs — каталоги User Data для Chromium-семейства (из yt-dlp).
func chromiumDirs(name string) string {
	local := envDir("LOCALAPPDATA")
	roaming := envDir("APPDATA")
	switch name {
	case "brave":
		return filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data")
	case "chrome":
		return filepath.Join(local, "Google", "Chrome", "User Data")
	case "chromium":
		return filepath.Join(local, "Chromium", "User Data")
	case "edge":
		return filepath.Join(local, "Microsoft", "Edge", "User Data")
	case "opera":
		// Opera хранит профиль прямо в каталоге, профилей не поддерживает
		return filepath.Join(roaming, "Opera Software", "Opera Stable")
	case "vivaldi":
		return filepath.Join(local, "Vivaldi", "User Data")
	case "whale":
		return filepath.Join(local, "Naver", "Naver Whale", "User Data")
	}
	return ""
}

// firefoxDirs — оба корня: обычная установка и MSIX-пакет из Store
// (из yt-dlp; без второго Store-версия Firefox не читается).
func firefoxDirs() []string {
	var dirs []string
	if roaming := envDir("APPDATA"); roaming != "" {
		dirs = append(dirs, filepath.Join(roaming, "Mozilla", "Firefox", "Profiles"))
	}
	if local := envDir("LOCALAPPDATA"); local != "" {
		dirs = append(dirs, filepath.Join(local, "Packages", "Mozilla.Firefox_n80bbvh6b1yt2",
			"LocalCache", "Roaming", "Mozilla", "Firefox", "Profiles"))
	}
	return dirs
}

// profilesByMtime сортирует профили свежим первым — так же, как это делает
// сам yt-dlp, когда профиль не указан (берёт самый свежий Cookies).
func profilesByMTime(profiles []profileInfo) []profileInfo {
	sort.SliceStable(profiles, func(i, j int) bool { return profiles[i].MTime > profiles[j].MTime })
	return profiles
}

func detectChromium(name, dir string) []profileInfo {
	// Chromium: профиль — каталог с Network/Cookies, ключ DPAPI лежит в
	// Local State уровнем выше (именно поэтому для произвольного форка
	// спецификация должна указывать на каталог профиля, а не на User Data).
	if _, err := os.Stat(filepath.Join(dir, "Local State")); err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []profileInfo{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cookies := filepath.Join(dir, e.Name(), "Network", "Cookies")
		st, err := os.Stat(cookies)
		if err != nil {
			continue
		}
		out = append(out, profileInfo{
			Name:  e.Name(),
			Spec:  name + ":" + e.Name(),
			MTime: st.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return profilesByMTime(out)
}

func detectFirefox() []profileInfo {
	out := []profileInfo{}
	for _, root := range firefoxDirs() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			db := filepath.Join(root, e.Name(), "cookies.sqlite")
			st, err := os.Stat(db)
			if err != nil {
				continue
			}
			out = append(out, profileInfo{
				Name:  e.Name(),
				Spec:  "firefox:" + e.Name(),
				MTime: st.ModTime().UTC().Format(time.RFC3339),
			})
		}
	}
	return profilesByMTime(out)
}

// detectCookieBrowsers возвращает список известных yt-dlp браузеров с
// найденными профилями. Поля Found=false не удаляем — UI должен показать,
// что браузер проверен и его нет, а не молча выкинуть.
func detectCookieBrowsers() []browserInfo {
	out := make([]browserInfo, 0, len(cookieBrowsers))
	for _, b := range cookieBrowsers {
		info := browserInfo{Name: b.name, Label: b.label, Profiles: []profileInfo{}}
		if b.name == "firefox" {
			info.Dir = strings.Join(firefoxDirs(), "; ")
			info.Profiles = detectFirefox()
		} else {
			info.Dir = chromiumDirs(b.name)
			info.NoProf = b.name == "opera"
			if info.Dir != "" {
				if _, err := os.Stat(info.Dir); err == nil {
					info.Found = true
				}
			}
			if info.Found && !info.NoProf {
				info.Profiles = detectChromium(b.name, info.Dir)
			}
		}
		if len(info.Profiles) > 0 {
			info.Found = true
			info.Suggest = info.Profiles[0].Spec
		} else if info.Found {
			info.Suggest = info.Name
		}
		out = append(out, info)
	}
	return out
}

// suggestedCookieSpec — что подставить в поле, если пользователь ничего не выбрал.
func suggestedCookieSpec() string {
	for _, b := range detectCookieBrowsers() {
		if b.Suggest != "" {
			return b.Suggest
		}
	}
	return ""
}

// cookiesActive — реально ли cookies применяются к вызовам yt-dlp.
func cookiesActive() bool {
	return cookiesSpec() != "" || cookiesFilePath() != ""
}

// cookieArgs собирает аргументы cookies для вызовов yt-dlp.
// Ничего не возвращает, если cookies выключены или не настроены.
func cookieArgs() []string {
	var out []string
	if spec := cookiesSpec(); spec != "" {
		out = append(out, "--cookies-from-browser", spec)
	}
	if file := cookiesFilePath(); file != "" {
		out = append(out, "--cookies", file)
	}
	return out
}

// humanizeCookieError переводит два задокументированных сбоя чтения cookies
// на человеческий язык. Оба неприятны тем, что yt-dlp на расшифровке DPAPI
// поднимает DownloadError с «force exit» — то есть ломает весь процесс,
// включая обычное скачивание YouTube.
func humanizeCookieError(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "could not copy chrome cookie database"):
		return "Не удалось прочитать базу cookies — она занята браузером. Закройте браузер и повторите."
	case strings.Contains(lower, "failed to decrypt with dpapi"):
		return "Не удалось расшифровать cookies через DPAPI. Обычно это другой пользователь Windows или повреждённая база."
	case strings.Contains(lower, "could not find") && strings.Contains(lower, "cookies database"):
		return "База cookies не найдена. Проверьте браузер и профиль в настройках."
	case strings.Contains(lower, "failed to load cookies"):
		return "Cookies не загрузились — проверьте настройку авторизации."
	case strings.Contains(lower, "cookies") && strings.Contains(lower, "fail"):
		return "Не удалось применить cookies. Проверьте настройку авторизации."
	}
	return ""
}
