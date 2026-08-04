package main

import (
	"io"
	"net/http"
	"net/http/httptest"
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
		"HTTP Error 429: Too Many Requests":                       "429",
		"Requested format is not available":                        "fatal",
		"Video unavailable":                                        "fatal",
		"Unsupported URL: ftp://x":                                 "fatal",
		"[generic] timed out":                                      "network",
		"stalled: no progress":                                     "network",
		"Read timed out after 15000ms":                             "network",
		"HTTP Error 503: Service Unavailable":                      "network",
		"This video is private":                                    "fatal",
		"Video unavailable in your country":                        "fatal",
	}
	for msg, want := range cases {
		if got := classifyError(msg); got != want {
			t.Errorf("classifyError(%q) = %q, want %q", msg, got, want)
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
	adaptFailure("network")
	if adapt.current != 4 {
		t.Fatalf("after 1 failure want 4, got %d", adapt.current)
	}
	adaptFailure("network")
	if adapt.current != 2 {
		t.Fatalf("after 2 failures want 2, got %d", adapt.current)
	}
	adaptFailure("network")
	if adapt.current != 1 {
		t.Fatalf("after 3 failures want 1, got %d", adapt.current)
	}
	adaptFailure("network")
	if adapt.current != 1 {
		t.Fatalf("floor must be 1, got %d", adapt.current)
	}

	// 429: ещё и cooldown
	adaptFailure("429")
	if adapt.current != 1 {
		t.Fatalf("429 keeps floor, got %d", adapt.current)
	}
	if time.Now().After(adapt.cooldownUntil) {
		t.Fatal("429 must set cooldownUntil in the future")
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
