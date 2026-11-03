package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Конфиг уводим во временный каталог: тесты не должны писать в реальный
// config.json пользователя (и затирать download_dir).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "clipnip-test-config")
	if err != nil {
		panic(err)
	}
	configDirOverride = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestFontsServe(t *testing.T) {
	ts := httptest.NewServer(newAPI())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fonts/pt-mono-cyrillic-400-normal.woff2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	t.Logf("status=%d bytes=%d type=%s", resp.StatusCode, len(body), resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// Проба транспорта не должна путать сетевой блок с отказом сайта:
// домен, который резолвится и слушает 443, — это «сеть в порядке», даже если
// сам сайт потом отдаст 403.
func TestProbeReachLive(t *testing.T) {
	reach := probeReach("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if reach.Verdict != reachOK {
		t.Fatalf("youtube must be reachable here, got %q (%s)", reach.Verdict, reach.Detail)
	}
}

func TestProbeReachRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "not-a-url", "://", "https://"} {
		if got := probeReach(raw); got.Verdict != reachUnknown {
			t.Errorf("probeReach(%q) = %q, want %q", raw, got.Verdict, reachUnknown)
		}
	}
}

func TestProbeReachRespectsProxyEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	if got := probeReach("https://www.youtube.com/"); got.Verdict != reachProxy {
		t.Fatalf("with proxy set must not claim reachability, got %q", got.Verdict)
	}
}

func TestReachDetailsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range []string{reachOK, reachDNS, reachTCP, reachProxy, reachUnknown} {
		d := reachDetail(v)
		if d == "" {
			t.Errorf("%s needs a human detail", v)
		}
		if seen[d] {
			t.Errorf("%s duplicates another verdict detail", v)
		}
		seen[d] = true
	}
}

func TestWaitReachDoesNotBlockForever(t *testing.T) {
	ch := make(chan reachResult) // ничего не шлём
	start := time.Now()
	got := waitReach(ch, 150*time.Millisecond)
	if got.Verdict != reachUnknown {
		t.Errorf("want unknown on timeout, got %q", got.Verdict)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waitReach blocked too long: %s", elapsed)
	}
}

func TestCookiesDisabledByDefault(t *testing.T) {
	// Поведение с выключенными cookies не должно отличаться ни на байт:
	// cookieArgs обязан быть пустым, иначе чужой конфиг утечёт в загрузку.
	setCookies(false, "chrome", "", false)
	t.Cleanup(func() { setCookies(false, "", "", false) })
	if got := cookieArgs(); len(got) != 0 {
		t.Errorf("cookies must be off by default, got %v", got)
	}
	if got := cookiesSpec(); got != "" {
		t.Errorf("spec must be empty when disabled, got %q", got)
	}
}

func TestCookieArgsComposition(t *testing.T) {
	t.Cleanup(func() { setCookies(false, "", "", false) })

	setCookies(true, "edge:Profile 1", "", false)
	got := cookieArgs()
	want := []string{"--cookies-from-browser", "edge:Profile 1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// Файл и браузер складываются вместе: yt-dlp сам объединяет обе банки.
	file := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(file, []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setCookies(true, "chrome", file, false)
	got = cookieArgs()
	if len(got) != 4 || got[0] != "--cookies-from-browser" || got[2] != "--cookies" || got[3] != file {
		t.Errorf("file + browser composition wrong: %v", got)
	}

	// Несуществующий файл молча пропускаем, а не ломаем вызов.
	setCookies(true, "chrome", filepath.Join(t.TempDir(), "nope.txt"), false)
	if got := cookieArgs(); len(got) != 2 {
		t.Errorf("missing file must be skipped, got %v", got)
	}
}

// Спецификация попадает в argv — мусорный config.json не должен превратиться
// в аргументы yt-dlp.
func TestCookiesSpecValidation(t *testing.T) {
	t.Cleanup(func() { setCookies(false, "", "", false) })
	good := []string{
		"chrome",
		"edge:Profile 1",
		`chromium:C:\Users\u\AppData\Local\Yandex\YandexBrowser\User Data\Default`,
		"firefox:abc123.default-release",
	}
	for _, s := range good {
		setCookies(true, s, "", false)
		if got := cookiesSpec(); got != s {
			t.Errorf("spec %q rejected, got %q", s, got)
		}
	}
	bad := []string{
		"--cookies",      // выглядит как флаг
		"-o/tmp/evil",    // ведущий дефис
		"chrome; rm -rf", // разделители команд
		"chrome\x00",     // NUL
		"$(whoami)",      // подстановка
	}
	for _, s := range bad {
		setCookies(true, s, "", false)
		if got := cookiesSpec(); got != "" {
			t.Errorf("spec %q must be rejected, got %q", s, got)
		}
	}
}

// Два задокументированных сбоя чтения cookies переводим в человеческий язык:
// на расшифровке DPAPI yt-dlp поднимает DownloadError с «force exit», то есть
// ломает весь процесс, включая обычное скачивание.
func TestHumanizeCookieError(t *testing.T) {
	cases := map[string]string{
		"Could not copy Chrome cookie database":         "Закройте браузер",
		"Failed to decrypt with DPAPI. See issue 10927": "DPAPI",
		"could not find chrome cookies database":        "не найдена",
		"failed to load cookies":                        "не загрузились",
	}
	for msg, want := range cases {
		got := humanizeCookieError(msg)
		if got == "" {
			t.Errorf("humanizeCookieError(%q) returned nothing", msg)
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("humanizeCookieError(%q) = %q, want substring %q", msg, got, want)
		}
	}
	// Обычные ошибки не должны переписываться
	if got := humanizeCookieError("HTTP Error 429: Too Many Requests"); got != "" {
		t.Errorf("unrelated error must pass through, got %q", got)
	}
}

func TestDetectCookieBrowsersShape(t *testing.T) {
	list := detectCookieBrowsers()
	if len(list) != len(cookieBrowsers) {
		t.Fatalf("want %d browsers, got %d", len(cookieBrowsers), len(list))
	}
	seen := map[string]bool{}
	for _, b := range list {
		if b.Name == "" || b.Label == "" {
			t.Errorf("browser without name/label: %+v", b)
		}
		if seen[b.Name] {
			t.Errorf("duplicate browser %q", b.Name)
		}
		seen[b.Name] = true
		// Suggest обязан быть валидной спецификацией, иначе автоподстановка
		// даст мусор, который config.go отвергнет.
		if b.Suggest != "" {
			setCookies(true, b.Suggest, "", false)
			if cookiesSpec() == "" {
				t.Errorf("suggest %q for %s is not a valid spec", b.Suggest, b.Name)
			}
			setCookies(false, "", "", false)
		}
	}
	for _, want := range []string{"chrome", "edge", "firefox"} {
		if !seen[want] {
			t.Errorf("yt-dlp supports %q — must be in the list", want)
		}
	}
}

// Профили сортируются свежим первым — так же, как их выбирает сам yt-dlp,
// когда профиль не указан (берёт самый свежий Cookies).
func TestProfilesSortedByMTime(t *testing.T) {
	base := t.TempDir()
	mk := func(name string, ts time.Time) {
		d := filepath.Join(base, name)
		os.MkdirAll(filepath.Join(d, "Network"), 0o755)
		os.WriteFile(filepath.Join(d, "Network", "Cookies"), []byte("x"), 0o600)
		os.Chtimes(filepath.Join(d, "Network", "Cookies"), ts, ts)
	}
	old := time.Now().Add(-72 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	mk("Profile Old", old)
	mk("Profile New", newer)

	got := profilesByMTime([]profileInfo{
		{Name: "Profile Old", MTime: old.UTC().Format(time.RFC3339)},
		{Name: "Profile New", MTime: newer.UTC().Format(time.RFC3339)},
	})
	if got[0].Name != "Profile New" {
		t.Errorf("newest profile must come first, got %q", got[0].Name)
	}
}

func TestCookiesEndpoints(t *testing.T) {
	setCookies(false, "", "", false)
	t.Cleanup(func() { setCookies(false, "", "", false) })
	ts := httptest.NewServer(newAPI())
	defer ts.Close()

	// GET: список браузеров + текущая конфигурация
	resp, err := http.Get(ts.URL + "/api/cookies")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/cookies = %d", resp.StatusCode)
	}
	var got struct {
		Config   map[string]any `json:"config"`
		Browsers []browserInfo  `json:"browsers"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("bad json: %v (%s)", err, body)
	}
	if len(got.Browsers) == 0 {
		t.Error("browsers list must not be empty")
	}

	// POST: мусорная спецификация отвергается, а не сохраняется
	bad, _ := http.Post(ts.URL+"/api/cookies", "application/json",
		strings.NewReader(`{"enabled":true,"spec":"--cookies"}`))
	badBody, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("bad spec must be rejected, got %d (%s)", bad.StatusCode, badBody)
	}
	if cookiesSpec() != "" {
		t.Error("rejected spec must not be stored")
	}

	// POST: валидная спецификация сохраняется
	ok, _ := http.Post(ts.URL+"/api/cookies", "application/json",
		strings.NewReader(`{"enabled":true,"spec":"chrome","ack":true}`))
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("valid spec rejected: %d", ok.StatusCode)
	}
	if cookiesSpec() != "chrome" {
		t.Errorf("spec not saved, got %q", cookiesSpec())
	}
}

// После появления HLS наивный выбор «max tbr на высоту» отдавал m3u8:
// битрейт у HLS всегда выше, а total_bytes у него иногда NA — прогресс-бар
// терял точность. При равной высоте должен побеждать https-DASH.
func TestQualityPrefersDashOverHLS(t *testing.T) {
	httpsFmt := map[string]any{
		"format_id": "137", "ext": "mp4", "height": 1080.0,
		"vcodec": "avc1.640028", "protocol": "https", "tbr": 3038.0,
	}
	hlsFmt := map[string]any{
		"format_id": "270", "ext": "mp4", "height": 1080.0,
		"vcodec": "avc1.640028", "protocol": "m3u8", "tbr": 4688.0,
	}

	if !betterFormat(httpsFmt, hlsFmt) {
		t.Error("https must win over m3u8 at equal height despite lower tbr")
	}
	if betterFormat(hlsFmt, httpsFmt) {
		t.Error("m3u8 must not win over https at equal height")
	}

	info := map[string]any{"formats": []any{hlsFmt, httpsFmt}}
	list := buildQualityList(info)
	if len(list) != 1 {
		t.Fatalf("want 1 quality, got %d: %v", len(list), list)
	}
	if list[0]["id"] != "137" {
		t.Errorf("want format 137 (https), got %v", list[0]["id"])
	}
}

func TestQualityFallsBackToTBR(t *testing.T) {
	// Оба формата одного протокола — решает битрейт.
	low := map[string]any{"format_id": "a", "ext": "mp4", "height": 720.0,
		"vcodec": "avc1", "protocol": "https", "tbr": 500.0}
	high := map[string]any{"format_id": "b", "ext": "mp4", "height": 720.0,
		"vcodec": "avc1", "protocol": "https", "tbr": 900.0}
	if !betterFormat(high, low) {
		t.Error("higher tbr must win within same protocol")
	}

	// Неизвестный протокол не должен ломать сравнение.
	unknownLow := map[string]any{"format_id": "c", "height": 720.0, "tbr": 100.0}
	unknownHigh := map[string]any{"format_id": "d", "height": 720.0, "tbr": 200.0}
	if !betterFormat(unknownHigh, unknownLow) {
		t.Error("unknown protocols must fall back to tbr")
	}
	if betterFormat(unknownLow, unknownHigh) {
		t.Error("unknown protocols must not ignore tbr")
	}
}

// deno указывается yt-dlp явным путём: binDir не в PATH, а по умолчанию
// yt-dlp включает только deno из PATH. Путь берётся из реестра компонентов,
// а не из каталога кэша — это же покрывает portable-папку.
func TestJsRuntimeArgsPointsAtResolvedDeno(t *testing.T) {
	dir := t.TempDir()
	resolvedPaths.Lock()
	resolvedPaths.m = map[string]string{}
	resolvedPaths.Unlock()
	t.Cleanup(func() {
		resolvedPaths.Lock()
		resolvedPaths.m = map[string]string{}
		resolvedPaths.Unlock()
	})

	if got := jsRuntimeArgs(); got != nil {
		t.Errorf("no deno resolved must yield no args, got %v", got)
	}

	deno := filepath.Join(dir, "deno.exe")
	if err := os.WriteFile(deno, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	setComponentPath("deno.exe", deno)

	got := jsRuntimeArgs()
	if len(got) != 2 || got[0] != "--js-runtimes" {
		t.Fatalf("bad js-runtime args: %v", got)
	}
	if !strings.HasPrefix(got[1], "deno:") || !strings.Contains(got[1], dir) {
		t.Errorf("must point at the resolved deno by path, got %q", got[1])
	}
}

// Пути исполняемых файлов обязаны следовать за разрешением компонентов,
// иначе portable-папка находится, а запускается пустой кэш — так и случилось
// на первом прогоне remote-сборки.
func TestYtdlpPathFollowsResolvedComponent(t *testing.T) {
	resolvedPaths.Lock()
	resolvedPaths.m = map[string]string{}
	resolvedPaths.Unlock()
	t.Cleanup(func() {
		resolvedPaths.Lock()
		resolvedPaths.m = map[string]string{}
		resolvedPaths.Unlock()
	})

	portable := filepath.Join(t.TempDir(), "yt-dlp.exe")
	setComponentPath("yt-dlp.exe", portable)
	if got := ytdlpPath(); got != portable {
		t.Errorf("ytdlpPath = %q, want portable %q", got, portable)
	}
	ff := filepath.Join(t.TempDir(), "ffmpeg.exe")
	setComponentPath("ffmpeg.exe", ff)
	if got := ffmpegPath(t.TempDir()); got != ff {
		t.Errorf("ffmpegPath must ignore the cache dir, got %q", got)
	}
}

// yt-dlp печатает сырые байты в секунду при скорости ниже 1 KiB/s;
// без этой ветки парсер молча возвращал 0 и скорость не показывалась.
func TestParseSpeedUnits(t *testing.T) {
	cases := map[string]int64{
		"512.00B/s":       512,
		"1.00KiB/s":       1024,
		"2.50MiB/s":       int64(2.5 * 1024 * 1024),
		"1.5GiB/s":        int64(1.5 * 1024 * 1024 * 1024),
		"1.00mib/s":       1024 * 1024,
		"NA":              0,
		"":                0,
		"unknown garbage": 0,
	}
	for in, want := range cases {
		if got := parseSpeed(in); got != want {
			t.Errorf("parseSpeed(%q) = %d, want %d", in, got, want)
		}
	}
}

// При склейке yt-dlp качает видео и аудио отдельными файлами: счётчики
// каждого начинаются с нуля. Проверяем накопление по компонентам.
func TestMergedProgressAccumulates(t *testing.T) {
	job := &Job{JobID: "m1", Status: "downloading", Stage: "downloading"}
	feed := func(downloaded, total int64, speed string) {
		job.set(func() {
			if total > 0 {
				if job.CurTotal > 0 && downloaded < job.CurDownloaded {
					job.DoneTotal += job.CurTotal
					job.CurDownloaded = 0
				}
				job.CurTotal = total
				job.CurDownloaded = downloaded
				job.Total = job.DoneTotal + job.CurTotal
				job.Downloaded = job.DoneTotal + job.CurDownloaded
			}
			if v := parseSpeed(speed); v > 0 {
				job.Speed = v
			}
		})
	}

	feed(50*1024*1024, 100*1024*1024, "1.00MiB/s") // видео на середине
	if job.Downloaded != 50*1024*1024 || job.Total != 100*1024*1024 {
		t.Fatalf("video component wrong: %d/%d", job.Downloaded, job.Total)
	}

	// старт второго компонента: счётчик откатился к нулю
	feed(0, 0, "NA")
	feed(1024*1024, 10*1024*1024, "NA") // аудио пошло
	want := int64(100*1024*1024 + 1024*1024)
	if job.Downloaded != want {
		t.Errorf("aggregate downloaded = %d, want %d", job.Downloaded, want)
	}
	// после старта аудио итоговый total = видео + аудио
	if job.Total != 110*1024*1024 {
		t.Errorf("total = %d, want %d", job.Total, int64(110*1024*1024))
	}

	feed(10*1024*1024, 10*1024*1024, "NA") // аудио докачано
	if job.Total != 110*1024*1024 {
		t.Errorf("final total = %d, want %d", job.Total, int64(110*1024*1024))
	}
	if job.Downloaded != 110*1024*1024 {
		t.Errorf("final downloaded = %d, want %d", job.Downloaded, int64(110*1024*1024))
	}
}

func TestQueuePersistsAndRestores(t *testing.T) {
	localDir := t.TempDir()
	oldOverride := configDirOverride
	configDirOverride = localDir
	t.Cleanup(func() {
		configDirOverride = oldOverride
		jobs.Lock()
		jobs.m = make(map[string]*Job)
		jobs.Unlock()
	})

	id := jobID("https://youtu.be/abc123", "video", "137")
	jobs.Lock()
	jobs.m[id] = &Job{
		JobID: id, URL: "https://youtu.be/abc123", Mode: "video", FormatID: "137",
		Title: "Queued Clip", Status: "queued", Stage: "queued", DownloadDir: localDir,
	}
	jobs.Unlock()
	persistQueue()

	data, err := os.ReadFile(queueFilePath())
	if err != nil {
		t.Fatalf("queue file not written: %v", err)
	}
	if !strings.Contains(string(data), "Queued Clip") {
		t.Errorf("queue file missing title: %s", data)
	}

	// Имитируем рестарт: память пуста, на диске осталась очередь
	jobs.Lock()
	jobs.m = make(map[string]*Job)
	jobs.Unlock()
	// очередь не должна переполниться от прошлых тестов
	for len(jobQueue) > 0 {
		<-jobQueue
	}

	restoreQueue()
	restored := getJob(id)
	if restored == nil {
		t.Fatal("job not restored from queue file")
	}
	if restored.Title != "Queued Clip" || restored.FormatID != "137" || restored.Mode != "video" {
		t.Errorf("restored job lost fields: %+v", restored)
	}
	if restored.Status != "queued" {
		t.Errorf("restored status = %q, want queued", restored.Status)
	}

	// Терминальные джобы в файл очереди не попадают
	restored.set(func() { restored.Status = "done" })
	persistQueue()
	if _, err := os.Stat(queueFilePath()); !os.IsNotExist(err) {
		t.Errorf("empty queue must remove the file, stat err = %v", err)
	}
}

func TestRestoreQueueSkipsGarbage(t *testing.T) {
	localDir := t.TempDir()
	oldOverride := configDirOverride
	configDirOverride = localDir
	t.Cleanup(func() {
		os.Remove(queueFilePath())
		configDirOverride = oldOverride
	})

	junk := `[{"url":"ftp://evil/x","mode":"video"},{"url":"https://ok.test/v","mode":"nonsense"}]`
	if err := os.WriteFile(queueFilePath(), []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreQueue() // не должен паниковать и не должен ничего создать

	jobs.Lock()
	defer jobs.Unlock()
	for id, j := range jobs.m {
		if j.URL == "ftp://evil/x" || j.URL == "https://ok.test/v" {
			t.Errorf("garbage entry must be skipped, got job %s", id)
		}
	}
}

func TestPartTTLIsNotTooShort(t *testing.T) {
	// `.part` на паузе должен переживать выходные: 24 ч убивали докачку
	if partTTL < 72*time.Hour {
		t.Errorf("partTTL too short: %s", partTTL)
	}
}

func TestPlaylistDetection(t *testing.T) {
	cases := map[string]bool{
		"https://www.youtube.com/playlist?list=PL7I7TsNvvxnN95A4teM8_Qn4-dbB0mz3l": true,
		"https://youtu.be/jbR-fKl4g94?si=6dOEwfJTJBQjf8ve":                         false,
		"https://www.youtube.com/watch?v=abc&list=PL7I7":                           false,
		"https://www.youtube.com/playlists/foo":                                    true,
	}
	for u, want := range cases {
		if got := isPlaylistURL(u); got != want {
			t.Errorf("isPlaylistURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestPlaylistEntries(t *testing.T) {
	info := map[string]any{
		"title": "My Playlist",
		"entries": []any{
			map[string]any{"id": "aaa111", "title": "Video One", "duration": 65.0, "thumbnail": "http://x/t1.jpg"},
			map[string]any{"id": "bbb222", "title": "Video Two", "duration": 0.0},
		},
	}
	entries := playlistEntries(info)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0]["url"] != "https://www.youtube.com/watch?v=aaa111" {
		t.Errorf("bad url: %v", entries[0]["url"])
	}
	if entries[0]["title"] != "Video One" || entries[0]["duration"] != 65.0 {
		t.Errorf("bad entry: %v", entries[0])
	}
	if entries[1]["url"] != "https://www.youtube.com/watch?v=bbb222" {
		t.Errorf("bad url2: %v", entries[1]["url"])
	}
}

func TestClassifyError(t *testing.T) {
	cases := map[string]string{
		// троттлинг
		"HTTP Error 429: Too Many Requests":  "throttle",
		"[youtube] Video: Too Many Requests": "throttle",
		// отказ передачи: форматы получены, но данные не пошли. Строка от
		// yt-dlp НЕ различает видео и аудио — так что это указание лишь на
		// «передачу отказали», а не на устаревший движок (см. errorHint)
		"unable to download video data: HTTP Error 403: Forbidden": "stream",
		"unable to download api page: HTTP Error 403: Forbidden":   "stream",
		// отказ сайта: нужен бот-чек/cookies
		"HTTP Error 403: Forbidden":                                       "site",
		"Sign in to confirm you're not a bot. Use --cookies-from-browser": "site",
		"Sign in to confirm you’re not a bot":                             "site",
		"This video is age-restricted":                                    "site",
		// фатальные
		"Requested format is not available": "fatal",
		"Video unavailable":                 "fatal",
		"This video is not available":       "fatal",
		"This video is private":             "fatal",
		"Private video":                     "fatal",
		"Unsupported URL: ftp://x":          "fatal",
		"Video unavailable in your country": "fatal",
		"HTTP Error 404: Not Found":         "fatal",
		// сеть
		"[generic] timed out":                 "network",
		"stalled: no progress":                "network",
		"Read timed out after 15000ms":        "network",
		"HTTP Error 503: Service Unavailable": "network",
	}
	for msg, want := range cases {
		if got := classifyError(msg); got != want {
			t.Errorf("classifyError(%q) = %q, want %q", msg, got, want)
		}
	}
}

// Ключевая инварианта фазы диагностики: отказ сайта и отказ отдачи НЕ трогают
// пул параллельности. Раньше 403 попадал в тот же класс, что и 429, из-за чего
// приложение само себя душило на сломанном движке.
func TestFailureClassesAffectParallelism(t *testing.T) {
	affects := []string{errThrottle, errNetwork}
	touches := []string{errSite, errStream, errFatal}
	for _, k := range affects {
		if !affectsParallelism(k) {
			t.Errorf("%s must affect parallelism", k)
		}
		if !retriable(k) {
			t.Errorf("%s must be retriable", k)
		}
	}
	for _, k := range touches {
		if affectsParallelism(k) {
			t.Errorf("%s must NOT affect parallelism", k)
		}
	}
	if retriable(errFatal) {
		t.Error("fatal is not fixed by retrying")
	}
}

// Адаптивная параллельность не должна деградировать от отказа сайта:
// раньше именно это маскировало поломку движка под «перегрузку YouTube».
func TestSiteRefusalKeepsPool(t *testing.T) {
	adapt.Lock()
	adapt.current = startParallel
	adapt.successes = 0
	adapt.cooldownUntil = time.Time{}
	adapt.Unlock()

	if affectsParallelism(classifyError("HTTP Error 403: Forbidden")) {
		t.Fatal("403 from site must not reach adaptFailure")
	}

	// троттлинг — наоборот, должен резать и брать cooldown
	adaptFailure(classifyError("HTTP Error 429: Too Many Requests"))
	if adapt.current != startParallel/2 {
		t.Fatalf("throttle must halve pool: got %d", adapt.current)
	}
	if time.Now().After(adapt.cooldownUntil) {
		t.Fatal("throttle must set cooldown")
	}

	adapt.Lock()
	adapt.current = startParallel
	adapt.successes = 0
	adapt.cooldownUntil = time.Time{}
	adapt.Unlock()
}

// Подсказки не должны обвинять движок, если он свежее порога. Именно это
// ложное утверждение показано пользователю на живом прогоне: nightly
// 2026.09.27 был назван устаревшим.
func TestErrorHint(t *testing.T) {
	setCookies(false, "", "", false)
	t.Cleanup(func() { setCookies(false, "", "", false) })

	streamHint := errorHint(errStream, "unable to download video data: HTTP Error 403", 0)
	if streamHint == "" {
		t.Fatal("stream refusal needs a hint")
	}
	if enginePossiblyStale() {
		t.Skip("engine on this machine is genuinely older than the known-good build")
	}
	if strings.Contains(streamHint, "older than the last build") {
		t.Errorf("fresh engine must not be called stale: %q", streamHint)
	}
	for _, want := range []string{"Retry", "address"} {
		if !strings.Contains(streamHint, want) {
			t.Errorf("hint should mention %q, got %q", want, streamHint)
		}
	}

	// Уже скачано — повтор не начнёт заново, и это надо сказать.
	withPartial := errorHint(errStream, "unable to download video data: HTTP Error 403", 1141274957)
	if !strings.Contains(withPartial, "nothing is lost") {
		t.Errorf("hint must mention that the partial is kept, got %q", withPartial)
	}
	if !strings.Contains(withPartial, "1.1 GB") {
		t.Errorf("hint should quote the partial size, got %q", withPartial)
	}

	stall := errorHint(errNetwork, "stalled: no progress", 241*1024*1024)
	if !strings.Contains(stall, "VPN") {
		t.Errorf("stall hint should name the VPN cause, got %q", stall)
	}

	siteHint := errorHint(errSite, "HTTP Error 403: Forbidden", 0)
	if siteHint == "" {
		t.Fatal("site refusal needs a hint pointing at cookies")
	}
	if !strings.Contains(siteHint, "cookies") {
		t.Errorf("site hint should mention cookies, got %q", siteHint)
	}

	// Cookies уже включены: совет «включи cookies» уводит по кругу.
	setCookies(true, "chrome", "", false)
	active := errorHint(errSite, "HTTP Error 403: Forbidden", 0)
	if active == siteHint {
		t.Error("hint must differ when cookies are already enabled")
	}
	if strings.Contains(active, "Enabling cookies") {
		t.Errorf("circular advice when cookies are on: %q", active)
	}

	if h := errorHint(errNetwork, "Read timed out after 15000ms", 0); h != "" {
		t.Errorf("plain network errors need no hint, got %q", h)
	}
}

func TestVersionDate(t *testing.T) {
	cases := map[string]int{
		"2026.09.27.232945": 20260927,
		"2026.08.19":        20260819,
		"2026.07.04":        20260704,
		"":                  0,
		"nightly":           0,
	}
	for in, want := range cases {
		if got := versionDate(in); got != want {
			t.Errorf("versionDate(%q) = %d, want %d", in, got, want)
		}
	}
	if !enginePossiblyStale() && versionDate("2020.01.01") >= versionDate(ytdlpKnownGoodSince) {
		t.Error("sanity: 2020 must be older than the known-good build")
	}
}

func TestPartialBytes(t *testing.T) {
	dir := t.TempDir()
	id := "job1"
	if got := partialBytes(dir, id); got != 0 {
		t.Errorf("empty dir must report 0, got %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".f137.mp4.part"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".f140.m4a.part"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := partialBytes(dir, id); got != 1500 {
		t.Errorf("want sum of both components (1500), got %d", got)
	}
}

// Хеши в binSpec переносятся руками, а обрезанный хеш не ловится сравнением
// «на глаз»: при проверке целостности он даёт заведомо ложное «подменённый
// файл». Поэтому длина и алфавит проверяются отдельно.
func TestEmbeddedHashesWellFormed(t *testing.T) {
	if len(embeddedBins) < 3 {
		t.Fatalf("want at least yt-dlp, deno, ffmpeg, got %d", len(embeddedBins))
	}
	seen := map[string]string{}
	// В тонких сборках (clipnipremote, clipnipportable) архивы не вшиты, и
	// сверять нечего — но список компонентов и их хеши как константы всё
	// равно обязаны быть в порядке.
	wantGz := ytdlpGz != ""
	for _, b := range embeddedBins {
		if b.name == "" {
			t.Errorf("bin without name: %+v", b)
		}
		if wantGz && b.gz == "" {
			t.Errorf("bin without source: %+v", b)
		}
		if b.sha256 == "" {
			t.Logf("%s: без проверки целостности (ручное обновление)", b.name)
			continue
		}
		if len(b.sha256) != 64 {
			t.Errorf("%s: hash must be 64 hex chars, got %d", b.name, len(b.sha256))
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(b.sha256) {
			t.Errorf("%s: hash must be lowercase hex", b.name)
		}
		if prev, dup := seen[b.sha256]; dup {
			t.Errorf("%s and %s share a hash — copy/paste error", b.name, prev)
		}
		seen[b.sha256] = b.name
	}
}

func TestFileSHA256DetectsChange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.exe")
	if err := os.WriteFile(p, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := fileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := fileSHA256(p)
	if first != same {
		t.Error("hash must be stable for unchanged file")
	}
	if err := os.WriteFile(p, []byte("TAMPERED"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, _ := fileSHA256(p)
	if changed == first {
		t.Error("hash must change after tampering")
	}
	if _, err := fileSHA256(filepath.Join(dir, "missing.exe")); err == nil {
		t.Error("missing file must return an error")
	}
}

func TestManifestParsing(t *testing.T) {
	// Проверяем ту же функцию, что вызывает loadManifest, а не её копию:
	// иначе тест перестаёт ловить расхождение между ними.
	good := `{"version":1,"components":[
		{"name":"yt-dlp.exe","version":"2026.09.27","file":"yt-dlp.exe","sha256":"` +
		strings.Repeat("a", 64) + `"}]}`
	var mf manifest
	if err := json.Unmarshal([]byte(good), &mf); err != nil {
		t.Fatal(err)
	}
	if err := validateManifest(&mf); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	bad := []string{
		`{"version":2,"components":[]}`,                                                                       // неизвестная версия формата
		`{"version":1,"components":[{"name":"x.exe","file":"x"}]}`,                                            // нет хеша
		`{"version":1,"components":[{"name":"x.exe","file":"x","sha256":"abc"}]}`,                             // короткий хеш
		`{"version":1,"components":[{"name":"x.exe","file":"x","sha256":"` + strings.Repeat("z", 64) + `"}]}`, // не hex
		`{"version":1,"components":[{"file":"x","sha256":"` + strings.Repeat("a", 64) + `"}]}`,                // нет имени
		`{"version":1,"components":[{"name":"x.exe","sha256":"` + strings.Repeat("a", 64) + `"}]}`,            // нет файла
	}
	for _, body := range bad {
		var m manifest
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			continue
		}
		if err := validateManifest(&m); err == nil {
			t.Errorf("manifest must be rejected: %s", body)
		}
	}
}

func TestManifestFindIsCaseInsensitive(t *testing.T) {
	mf := manifest{Components: []manifestComponent{
		{Name: "yt-dlp.exe", File: "yt-dlp.exe"},
	}}
	if _, ok := mf.find("YT-DLP.EXE"); !ok {
		t.Error("component lookup must ignore case — Windows не различает регистр имён файлов")
	}
	if _, ok := mf.find("ffmpeg.exe"); ok {
		t.Error("must not find a component that is absent")
	}
}

// Компонент без https нельзя качать: хеш приезжает по тому же каналу, что и
// файл, и сверка перестаёт что-либо значить.
// Скачанный компонент ставится на место только при совпадении хеша, и
// несовпадение не оставляет ни файла, ни мусора во временном имени.
func TestFetchAndVerifyInstallsOnlyOnMatch(t *testing.T) {
	payload := []byte("not-really-an-exe")
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp.exe")

	// неверный хеш — файл не должен появиться
	bad := manifestComponent{Name: "yt-dlp.exe", Version: "1", SHA256: strings.Repeat("b", 64)}
	if err := fetchAndVerify(srv.Client(), srv.URL, dest, bad); err == nil {
		t.Fatal("hash mismatch must fail")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("mismatched component must not be installed")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error("temp file must be cleaned up")
	}

	// верный хеш — ставится, версия записывается
	good := manifestComponent{Name: "yt-dlp.exe", Version: "2026.09.27", SHA256: sum}
	if err := fetchAndVerify(srv.Client(), srv.URL, dest, good); err != nil {
		t.Fatalf("matching component must install: %v", err)
	}
	got, err := fileSHA256(dest)
	if err != nil || got != sum {
		t.Errorf("installed file hash = %q, want %q (err %v)", got, sum, err)
	}
	if v, err := os.ReadFile(dest + ".ver"); err != nil || string(v) != "2026.09.27" {
		t.Errorf("version marker not written: %q (err %v)", v, err)
	}
}

func TestDownloadRequiresHTTPS(t *testing.T) {
	mc := manifestComponent{Name: "x.exe", File: "x.exe", SHA256: strings.Repeat("a", 64)}
	if err := downloadComponent("http://example.com/bin", mc, filepath.Join(t.TempDir(), "x.exe")); err == nil {
		t.Error("plain http must be refused for executable components")
	}
	if err := downloadComponent("https://127.0.0.1:1/bin", mc, filepath.Join(t.TempDir(), "x.exe")); err == nil {
		t.Error("unreachable host must return an error")
	}
}

// Порядок источников: portable-папка важнее кэша и сети.
func TestPortableDirDetection(t *testing.T) {
	if got := portableBinDir(); got != "" {
		t.Logf("portable dir present: %s", got)
	}
	// функция опирается на каталог exe — в тестах он есть, но clipnip-bin
	// рядом с ним не создан, поэтому ожидаем пустую строку
	if d := portableBinDir(); d != "" {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("portableBinDir returned a missing dir: %s", d)
		}
	}
}

func TestComponentsConfigRoundTrip(t *testing.T) {
	oldOverride := configDirOverride
	configDirOverride = t.TempDir()
	t.Cleanup(func() { configDirOverride = oldOverride })

	config.Lock()
	config.ComponentsURL = ""
	config.Unlock()
	if got := componentsBaseURL(); got != "" {
		t.Errorf("empty by default, got %q", got)
	}
	t.Setenv("CLIPNIP_BIN_URL", "https://from-env.example/bin")
	if got := componentsBaseURL(); got != "https://from-env.example/bin" {
		t.Errorf("env fallback broken: %q", got)
	}
	config.Lock()
	config.ComponentsURL = "https://from-config.example/bin/"
	config.Unlock()
	if got := componentsBaseURL(); got != "https://from-config.example/bin" {
		t.Errorf("config must win over env and trailing slash trimmed, got %q", got)
	}
}

// Недостающие компоненты не должны ронять приложение: иначе пользователь
// не сможет зайти в настройки и починить это (окно с ошибкой просило именно
// «укажите адрес сервера в настройках» — но настройки были недостижимы).
func TestEnsureBinsNotFatalWhenComponentsMissing(t *testing.T) {
	dir := t.TempDir()
	oldConfig, oldBin := configDirOverride, binDirOverride
	configDirOverride, binDirOverride = dir, filepath.Join(dir, "bin")
	restore := configSnapshot()
	// сборка без встроенных копий: если бы embed был доступен, компоненты
	// распаковались бы и случай «ничего нет» не воспроизвёлся бы
	origBins := embeddedBins
	embeddedBins = []binSpec{{"yt-dlp.exe", "", "", ""}, {"ffmpeg.exe", "", "", ""}}
	t.Cleanup(func() {
		configDirOverride, binDirOverride = oldConfig, oldBin
		embeddedBins = origBins
		restore()
		componentsMu.Lock()
		componentStates = nil
		componentsProblem = ""
		componentsResolved = false
		componentsMu.Unlock()
		resolvedPaths.Lock()
		resolvedPaths.m = map[string]string{}
		resolvedPaths.Unlock()
	})

	componentsMu.Lock()
	componentsProblem = ""
	componentsResolved = false
	componentsMu.Unlock()
	if err := ensureBins(); err != nil {
		t.Fatalf("missing components must not be fatal, got %v", err)
	}
	if componentsProblemText() == "" {
		t.Error("problem must be reported even when startup continues")
	}
	if componentsReady(requiredComponents()) {
		t.Error("components must not be reported ready")
	}
	if got := missingComponents(requiredComponents()); len(got) != len(requiredComponents()) {
		t.Errorf("all required components must be missing, got %v", got)
	}
}

// deno — страховка, а не условие работы: его отсутствие не должно ни ронять
// запуск, ни числиться как проблема.
func TestDenoIsOptional(t *testing.T) {
	if len(optionalComponents()) != 1 || optionalComponents()[0] != "deno.exe" {
		t.Errorf("deno must be optional, got %v", optionalComponents())
	}
	for _, name := range requiredComponents() {
		if name == "deno.exe" {
			t.Error("deno must not be required")
		}
	}
}

func configSnapshot() func() {
	config.RLock()
	c := config.Config
	config.RUnlock()
	return func() {
		config.Lock()
		config.Config = c
		config.Unlock()
	}
}

func TestPauseResumeAllNoDeadlock(t *testing.T) {
	dir := t.TempDir()
	jobs.Lock()
	jobs.m["aaa"] = &Job{JobID: "aaa", Status: "downloading", Stage: "downloading", DownloadDir: dir}
	jobs.m["bbb"] = &Job{JobID: "bbb", Status: "queued", Stage: "queued", DownloadDir: dir}
	jobs.m["ccc"] = &Job{JobID: "ccc", Status: "retry_wait", Stage: "retry_wait", DownloadDir: dir}
	jobs.m["ddd"] = &Job{JobID: "ddd", Status: "done", Stage: "done", DownloadDir: dir}
	jobs.Unlock()
	t.Cleanup(func() {
		jobs.Lock()
		delete(jobs.m, "aaa")
		delete(jobs.m, "bbb")
		delete(jobs.m, "ccc")
		delete(jobs.m, "ddd")
		jobs.Unlock()
		resumeAll()
	})

	pauseAllJobs()
	for _, id := range []string{"aaa", "bbb", "ccc"} {
		if s := jobStatus(id); s != "paused" {
			t.Fatalf("pauseAllJobs: %s want paused, got %s", id, s)
		}
	}
	if s := jobStatus("ddd"); s != "done" {
		t.Fatalf("pauseAllJobs must not touch done, got %s", s)
	}

	resumeAllJobs()
	for _, id := range []string{"aaa", "bbb", "ccc"} {
		if s := jobStatus(id); s != "queued" {
			t.Fatalf("resumeAllJobs: %s want queued, got %s", id, s)
		}
	}
}

func TestAdaptiveParallel(t *testing.T) {
	adapt.Lock()
	adapt.current = startParallel
	adapt.successes = 0
	adapt.cooldownUntil = time.Time{}
	adapt.Unlock()

	// серия сетевых отказов: 8 -> 4 -> 2 -> 1 -> пол 1
	adaptFailure(errNetwork)
	if adapt.current != 4 {
		t.Fatalf("after 1 failure want 4, got %d", adapt.current)
	}
	adaptFailure("network")
	if adapt.current != 2 {
		t.Fatalf("after 2 failures want 2, got %d", adapt.current)
	}
	adaptFailure(errNetwork)
	if adapt.current != 1 {
		t.Fatalf("after 3 failures want 1, got %d", adapt.current)
	}
	adaptFailure(errNetwork)
	if adapt.current != 1 {
		t.Fatalf("floor must be 1, got %d", adapt.current)
	}

	// троттлинг: держит пол и добавляет cooldown
	adaptFailure(errThrottle)
	if adapt.current != 1 {
		t.Fatalf("throttle keeps floor, got %d", adapt.current)
	}
	if time.Now().After(adapt.cooldownUntil) {
		t.Fatal("throttle must set cooldownUntil in the future")
	}

	// рост: successStep успешных подряд -> +1
	for i := 0; i < successStep; i++ {
		adaptSuccess()
	}
	if adapt.current != 2 {
		t.Fatalf("after %d successes want 2, got %d", successStep, adapt.current)
	}

	// потолок
	for i := 0; i < 200; i++ {
		adaptSuccess()
	}
	if adapt.current != maxParallel {
		t.Fatalf("ceiling want %d, got %d", maxParallel, adapt.current)
	}

	adapt.Lock()
	adapt.current = startParallel
	adapt.successes = 0
	adapt.cooldownUntil = time.Time{}
	adapt.Unlock()
}

func TestFindExistingFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Song One.mp3")
	write("Clip Two.mp4")
	write("Bad Name.mp3")

	if got := findExistingFile(dir, "Song One", "fb", "mp3_128"); filepath.Base(got) != "Song One.mp3" {
		t.Errorf("mp3 mode: want Song One.mp3, got %q", got)
	}
	if got := findExistingFile(dir, "Song One", "fb", "video"); got != "" {
		t.Errorf("video mode: .mp3 must not match, got %q", got)
	}
	if got := findExistingFile(dir, "Clip Two", "fb", "video"); filepath.Base(got) != "Clip Two.mp4" {
		t.Errorf("video mode: want Clip Two.mp4, got %q", got)
	}
	if got := findExistingFile(dir, "Clip Two", "fb", "m4a"); got != "" {
		t.Errorf("m4a mode: .mp4 must not match, got %q", got)
	}
	if got := findExistingFile(dir, "Missing", "fb", "video"); got != "" {
		t.Errorf("want miss for Missing, got %q", got)
	}
	if got := findExistingFile(dir, "", "fb", "video"); got != "" {
		t.Errorf("empty title must not match, got %q", got)
	}
	// небезопасные символы вычищаются так же, как в renameTo
	if got := findExistingFile(dir, "Bad: Name", "fb", "mp3_192"); filepath.Base(got) != "Bad Name.mp3" {
		t.Errorf("sanitized title must match, got %q", got)
	}
}

// X отдаёт видео как mp4 + аудио как HLS/mp4. Старый fallback
// `<id>+bestaudio[ext=m4a]/bestaudio` на таком сайте выбирал только звук:
// это и выглядело как «не тянет видео с X».
func TestBuildDownloadArgsSelectedFormatKeepsVideo(t *testing.T) {
	args := buildDownloadArgs(&Job{
		JobID: "j", DownloadDir: t.TempDir(), Mode: modeVideo, FormatID: "http-2176",
	})
	formatArg := ""
	for i, a := range args {
		if a == "--format" && i+1 < len(args) {
			formatArg = args[i+1]
		}
	}
	want := "http-2176+bestaudio[ext=m4a]/http-2176+bestaudio/http-2176"
	if formatArg != want {
		t.Fatalf("format selector = %q, want %q", formatArg, want)
	}
	if strings.HasSuffix(formatArg, "/bestaudio") {
		t.Error("fallback must not select audio-only")
	}
}

func TestBuildDownloadArgsNoAudio(t *testing.T) {
	args := buildDownloadArgs(&Job{
		JobID: "j", DownloadDir: t.TempDir(), Mode: modeVideoNoAudio, FormatID: "137",
	})
	formatArg := ""
	for i, a := range args {
		if a == "--format" && i+1 < len(args) {
			formatArg = args[i+1]
		}
	}
	if formatArg != "137" {
		t.Errorf("no-audio selected format must be the video id alone, got %q", formatArg)
	}

	args = buildDownloadArgs(&Job{JobID: "j", DownloadDir: t.TempDir(), Mode: modeVideoNoAudio})
	for i, a := range args {
		if a == "--format" && i+1 < len(args) {
			formatArg = args[i+1]
		}
	}
	if strings.Contains(formatArg, "+bestaudio") {
		t.Errorf("no-audio default format must not merge audio, got %q", formatArg)
	}
	if !strings.Contains(formatArg, "bestvideo") {
		t.Errorf("no-audio default must prefer a video stream, got %q", formatArg)
	}
}

func TestNoAudioFilenameSuffix(t *testing.T) {
	dir := t.TempDir()
	noAudio := filepath.Join(dir, "Clip"+noAudioSuffix+".mp4")
	if err := os.WriteFile(noAudio, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findExistingFile(dir, "Clip", "fb", modeVideoNoAudio); got != noAudio {
		t.Errorf("no-audio skip must find the suffixed file, got %q", got)
	}
	if got := findExistingFile(dir, "Clip", "fb", modeVideo); got != "" {
		t.Errorf("full-video mode must not treat a no-audio file as downloaded, got %q", got)
	}

	full := filepath.Join(dir, "Clip.mp4")
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findExistingFile(dir, "Clip", "fb", modeVideo); got != full {
		t.Errorf("full video must still be found, got %q", got)
	}
	if got := findExistingFile(dir, "Clip", "fb", modeVideoNoAudio); got != noAudio {
		t.Errorf("no-audio mode must keep its own suffix, got %q", got)
	}
}

func TestSanitizeFilenameKeepsUTF8(t *testing.T) {
	long := strings.Repeat("Ж", 200)
	got := sanitizeFilename(long, "fb")
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a multibyte rune: %q", got)
	}
	if len(got) > 180 {
		t.Errorf("name too long: %d bytes", len(got))
	}
}

func TestRestoreQueueNormalizesMode(t *testing.T) {
	oldOverride := configDirOverride
	configDirOverride = t.TempDir()
	t.Cleanup(func() {
		os.Remove(queueFilePath())
		configDirOverride = oldOverride
		jobs.Lock()
		for id, j := range jobs.m {
			if strings.Contains(j.URL, "example.test") {
				delete(jobs.m, id)
			}
		}
		jobs.Unlock()
		for len(jobQueue) > 0 {
			<-jobQueue
		}
	})

	entries := `[
		{"url":"https://example.test/a","mode":"","format_id":"137","title":"A"},
		{"url":"https://example.test/b","mode":"m4a","format_id":"137","title":"B"}
	]`
	if err := os.WriteFile(queueFilePath(), []byte(entries), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreQueue()

	videoID := jobID("https://example.test/a", modeVideo, "137")
	video := getJob(videoID)
	if video == nil {
		t.Fatal("empty mode must normalize to video")
	}
	if video.Mode != modeVideo || video.FormatID != "137" {
		t.Errorf("video job normalized wrong: mode=%q format=%q", video.Mode, video.FormatID)
	}

	audioID := jobID("https://example.test/b", modeM4A, "")
	audio := getJob(audioID)
	if audio == nil {
		t.Fatal("audio job not restored")
	}
	if audio.Mode != modeM4A || audio.FormatID != "" {
		t.Errorf("audio job must drop format id: mode=%q format=%q", audio.Mode, audio.FormatID)
	}
}

func TestFSList(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"alpha", "Beta", ".hidden", "$RECYCLE.BIN"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/fs/list?path="+url.QueryEscape(base), nil)
	rec := httptest.NewRecorder()
	handleFSList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Dirs []fsEntry `json:"dirs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, d := range got.Dirs {
		names = append(names, d.Name)
	}
	want := []string{"alpha", "Beta"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("dirs = %v, want %v", names, want)
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/fs/list?path=relative", nil)
	badRec := httptest.NewRecorder()
	handleFSList(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Errorf("relative path must be rejected, got %d", badRec.Code)
	}
}

func TestFSMkdir(t *testing.T) {
	parent := t.TempDir()
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/fs/mkdir", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handleFSMkdir(rec, req)
		return rec
	}

	rec := post(`{"parent":` + strconv.Quote(parent) + `,"name":"New Folder"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create failed: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(created.Path); err != nil || !info.IsDir() {
		t.Fatalf("folder not created: %q (%v)", created.Path, err)
	}

	if got := post(`{"parent":` + strconv.Quote(parent) + `,"name":"New Folder"}`).Code; got != http.StatusConflict {
		t.Errorf("duplicate must be 409, got %d", got)
	}

	for _, name := range []string{"", "..", ".hidden", "a/b", `a\\b`, "CON", "com1.txt", "bad:name", "trail."} {
		body := `{"parent":` + strconv.Quote(parent) + `,"name":` + strconv.Quote(name) + `}`
		if got := post(body).Code; got != http.StatusBadRequest {
			t.Errorf("name %q must be rejected, got %d", name, got)
		}
	}
	if got := post(`{"parent":"relative","name":"x"}`).Code; got != http.StatusBadRequest {
		t.Errorf("relative parent must be rejected, got %d", got)
	}
	if got := post(`{"parent":` + strconv.Quote(filepath.Join(parent, "missing")) + `,"name":"x"}`).Code; got != http.StatusBadRequest {
		t.Errorf("missing parent must be rejected, got %d", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/fs/mkdir", nil)
	methodRec := httptest.NewRecorder()
	handleFSMkdir(methodRec, req)
	if methodRec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET must be 405, got %d", methodRec.Code)
	}
}

func TestSameOriginRefusesCrossSite(t *testing.T) {
	ts := httptest.NewServer(sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer ts.Close()

	send := func(method string, headers map[string]string) int {
		req, _ := http.NewRequest(method, ts.URL+"/api/anything", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := send(http.MethodGet, nil); got != http.StatusOK {
		t.Errorf("local request without Origin must pass, got %d", got)
	}
	if got := send(http.MethodPost, map[string]string{"Origin": "https://evil.example"}); got != http.StatusForbidden {
		t.Errorf("cross-origin request must be refused, got %d", got)
	}
	if got := send(http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}); got != http.StatusForbidden {
		t.Errorf("cross-site fetch must be refused, got %d", got)
	}
	if got := send(http.MethodPost, map[string]string{"Origin": ts.URL}); got != http.StatusOK {
		t.Errorf("same-origin request must pass, got %d", got)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/anything", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unexpected Host header must be refused (DNS rebinding), got %d", resp.StatusCode)
	}
}

func TestProbeReachUsesURLPort(t *testing.T) {
	if envProxySet() {
		t.Skip("proxy env is set")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	if got := probeReach(ts.URL); got.Verdict != reachOK {
		t.Fatalf("http server on a custom port must be reachable, got %q (%s)", got.Verdict, got.Detail)
	}
}

// Хеш в embeddedBins обновляют руками. Если он не совпадает с содержимым
// вшитого gz, автономная сборка падает на старте — ловим это тестом.
func TestEmbeddedPayloadMatchesHash(t *testing.T) {
	for _, b := range embeddedBins {
		if b.gz == "" || b.sha256 == "" {
			continue
		}
		f, err := assetsDir.Open(b.gz)
		if err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			t.Fatalf("%s: %v", b.name, err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, zr); err != nil {
			zr.Close()
			f.Close()
			t.Fatalf("%s: %v", b.name, err)
		}
		zr.Close()
		f.Close()
		got := fmt.Sprintf("%x", h.Sum(nil))
		if got != b.sha256 {
			t.Errorf("%s: embedded payload hash %s, embeddedBins has %s", b.name, got, b.sha256)
		}
	}
}

// Настоящий ffmpeg, настоящий muxed-файл: проверяем и детект аудио, и
// пересборку без него, а не только строку аргументов.
func TestNoAudioStripWithFFmpeg(t *testing.T) {
	candidates := []string{
		filepath.Join("clipnip-bin", "ffmpeg.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "clipnip", "bin", "ffmpeg.exe"),
	}
	ffmpeg := ""
	for _, c := range candidates {
		if fileExists(c) {
			ffmpeg = c
			break
		}
	}
	if ffmpeg == "" {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "muxed.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.5",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5",
		"-shortest", "-c:v", "libx264", "-c:a", "aac", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("test fixture generation failed: %v (%s)", err, out)
	}

	info, err := probeMedia(ffmpeg, src)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasAudio {
		t.Fatal("fixture must contain audio")
	}
	if err := remuxNoAudio(&Job{JobID: "t"}, ffmpeg, src); err != nil {
		t.Fatal(err)
	}
	info, err = probeMedia(ffmpeg, src)
	if err != nil {
		t.Fatal(err)
	}
	if info.HasAudio {
		t.Error("audio track must be gone")
	}
}

// Сырой DASH-контейнер без аудио — валидный H.264, но часть плееров его не
// открывает. Ремукс обязателен даже при отсутствии аудио: он делает isom.
func TestRemuxNoAudioNormalizesDashContainer(t *testing.T) {
	candidates := []string{
		filepath.Join("clipnip-bin", "ffmpeg.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "clipnip", "bin", "ffmpeg.exe"),
	}
	ffmpeg := ""
	for _, c := range candidates {
		if fileExists(c) {
			ffmpeg = c
			break
		}
	}
	if ffmpeg == "" {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "dash.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.5",
		"-c:v", "libx264", "-an", "-brand", "dash", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("dash fixture generation failed: %v (%s)", err, out)
	}
	info, err := probeMedia(ffmpeg, src)
	if err != nil {
		t.Fatal(err)
	}
	if info.MajorBrand != "dash" {
		t.Skipf("fixture brand = %q, want dash", info.MajorBrand)
	}
	if err := remuxNoAudio(&Job{JobID: "t"}, ffmpeg, src); err != nil {
		t.Fatal(err)
	}
	info, err = probeMedia(ffmpeg, src)
	if err != nil {
		t.Fatal(err)
	}
	if info.MajorBrand == "dash" {
		t.Error("DASH container was not normalized")
	}
	if info.HasAudio {
		t.Error("video-only fixture must stay without audio")
	}
}

// Yandex не знает yt-dlp по имени, поэтому профиль должен выражаться
// chromium:<путь-к-профилю>, а не yandex:<имя>.
func TestDetectYandexProfileUsesChromiumPathSpec(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "Default")
	if err := os.MkdirAll(filepath.Join(profile, "Network"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "Network", "Cookies"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := `{"profile":{"info_cache":{"Default":{"name":"Пользователь 1"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	profiles := detectChromium("yandex", dir, true)
	if len(profiles) != 1 {
		t.Fatalf("want 1 profile, got %d", len(profiles))
	}
	wantSpec := "chromium:" + profile
	if profiles[0].Spec != wantSpec {
		t.Errorf("spec = %q, want %q", profiles[0].Spec, wantSpec)
	}
	if profiles[0].Name != "Пользователь 1" {
		t.Errorf("display name = %q", profiles[0].Name)
	}
}

func TestSnapshotCookieProfile(t *testing.T) {
	source := t.TempDir()
	profile := filepath.Join(source, "Default")
	if err := os.MkdirAll(filepath.Join(profile, "Network"), 0o755); err != nil {
		t.Fatal(err)
	}
	cookies := []byte("encrypted-cookie-db")
	if err := os.WriteFile(filepath.Join(profile, "Network", "Cookies"), cookies, 0o600); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"os_crypt":{"encrypted_key":"test"}}`)
	if err := os.WriteFile(filepath.Join(source, "Local State"), state, 0o600); err != nil {
		t.Fatal(err)
	}

	oldOverride := configDirOverride
	configDirOverride = t.TempDir()
	t.Cleanup(func() { configDirOverride = oldOverride })

	spec, err := snapshotCookieProfile("chromium:" + profile)
	if err != nil {
		t.Fatal(err)
	}
	snapshotDir, _, err := resolveCookieProfile(spec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(snapshotDir, "Network", "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(cookies) {
		t.Errorf("snapshot cookies = %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(snapshotDir), "Local State")); err != nil {
		t.Errorf("Local State not copied: %v", err)
	}
}

func TestYandexIsDetected(t *testing.T) {
	for _, b := range cookieBrowsers {
		if b.name == "yandex" {
			return
		}
	}
	t.Error("yandex must be in cookieBrowsers")
}
