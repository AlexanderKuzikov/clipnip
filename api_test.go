package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// deno должен попадать в binDir и указываться yt-dlp явным путём:
// binDir не в PATH, а по умолчанию yt-dlp включает только deno из PATH.
func TestJsRuntimeArgsPointsAtEmbeddedDeno(t *testing.T) {
	dir := t.TempDir()
	if got := jsRuntimeArgs(dir); got != nil {
		t.Errorf("no deno on disk must yield no args, got %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "deno.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := jsRuntimeArgs(dir)
	if len(got) != 2 || got[0] != "--js-runtimes" {
		t.Fatalf("bad js-runtime args: %v", got)
	}
	if !strings.HasPrefix(got[1], "deno:") || !strings.Contains(got[1], dir) {
		t.Errorf("must point at the embedded deno by path, got %q", got[1])
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
	t.Cleanup(func() { configDirOverride = oldOverride })

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
		// отказ отдачи: extractor отработал (в логе виден выбор форматов),
		// но видеопоток не отдали — это устаревший движок, а не перегрузка
		"unable to download video data: HTTP Error 403: Forbidden": "engine",
		"unable to download api page: HTTP Error 403: Forbidden":   "engine",
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
	touches := []string{errSite, errEngine, errFatal}
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
	if retriable(errEngine) {
		t.Error("stale engine is not fixed by retrying")
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

func TestErrorHint(t *testing.T) {
	setCookies(false, "", "", false)
	t.Cleanup(func() { setCookies(false, "", "", false) })

	if errorHint(errEngine) == "" {
		t.Error("stale engine needs a hint pointing at the rebuild")
	}
	siteHint := errorHint(errSite)
	if siteHint == "" {
		t.Fatal("site refusal needs a hint pointing at cookies")
	}
	if !strings.Contains(siteHint, "cookies") {
		t.Errorf("site hint should mention cookies, got %q", siteHint)
	}

	// Cookies уже включены: совет «включи cookies» уводит по кругу.
	setCookies(true, "chrome", "", false)
	active := errorHint(errSite)
	if active == siteHint {
		t.Error("hint must differ when cookies are already enabled")
	}
	if strings.Contains(active, "Enabling cookies") {
		t.Errorf("circular advice when cookies are on: %q", active)
	}

	if h := errorHint(errNetwork); h != "" {
		t.Errorf("network errors need no hint, got %q", h)
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
