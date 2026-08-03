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
