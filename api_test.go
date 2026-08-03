package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
