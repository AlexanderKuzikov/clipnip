package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestPlaylistDetection(t *testing.T) {
	cases := map[string]bool{
		"https://www.youtube.com/playlist?list=PL7I7TsNvvxnN95A4teM8_Qn4-dbB0mz3l": true,
		"https://youtu.be/jbR-fKl4g94?si=6dOEwfJTJBQjf8ve":                          false,
		"https://www.youtube.com/watch?v=abc&list=PL7I7":                            false,
		"https://www.youtube.com/playlists/foo":                                     true,
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
		"HTTP Error 429: Too Many Requests": "throttle",
		"[youtube] Video: Too Many Requests": "throttle",
		// отказ отдачи: extractor отработал (в логе виден выбор форматов),
		// но видеопоток не отдали — это устаревший движок, а не перегрузка
		"unable to download video data: HTTP Error 403: Forbidden": "engine",
		"unable to download api page: HTTP Error 403: Forbidden":  "engine",
		// отказ сайта: нужен бот-чек/cookies
		"HTTP Error 403: Forbidden": "site",
		"Sign in to confirm you're not a bot. Use --cookies-from-browser": "site",
		"Sign in to confirm you’re not a bot": "site",
		"This video is age-restricted": "site",
		// фатальные
		"Requested format is not available": "fatal",
		"Video unavailable":                 "fatal",
		"This video is not available":        "fatal",
		"This video is private":             "fatal",
		"Private video":                     "fatal",
		"Unsupported URL: ftp://x":          "fatal",
		"Video unavailable in your country": "fatal",
		"HTTP Error 404: Not Found":         "fatal",
		// сеть
		"[generic] timed out":          "network",
		"stalled: no progress":         "network",
		"Read timed out after 15000ms": "network",
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
	if errorHint(errEngine) == "" {
		t.Error("stale engine needs a hint pointing at the rebuild")
	}
	if errorHint(errSite) == "" {
		t.Error("site refusal needs a hint pointing at cookies")
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
