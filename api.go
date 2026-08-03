package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func isAllowedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func newAPI() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			serveIndex(w, r)
			return
		}
		http.NotFound(w, r)
	})

	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		data, err := readEmbed("favicon.svg")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(data)
	})

	mux.HandleFunc("/fonts/", func(w http.ResponseWriter, r *http.Request) {
		data, err := readEmbed(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	})

	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			dir := getDownloadDir()
			if dir == "" {
				dir = defaultDownloadDir()
			}
			writeJSON(w, http.StatusOK, map[string]string{"download_dir": dir})

		case http.MethodPost:
			var req struct {
				DownloadDir string `json:"download_dir"`
				Browse      bool   `json:"browse"`
			}
			json.NewDecoder(r.Body).Decode(&req)

			if req.Browse {
				chosen, err := browseFolder("Choose download folder")
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				if chosen == "" {
					writeJSON(w, http.StatusOK, map[string]string{"download_dir": getEffectiveDownloadDir()})
					return
				}
				setDownloadDir(chosen)
				writeJSON(w, http.StatusOK, map[string]string{"download_dir": chosen})
				return
			}

			dir := strings.TrimSpace(req.DownloadDir)
			if dir == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Empty path"})
				return
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if err := os.MkdirAll(abs, 0o755); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			setDownloadDir(abs)
			writeJSON(w, http.StatusOK, map[string]string{"download_dir": abs})

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		}
	})

	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
			return
		}
		var req struct{ URL string `json:"url"` }
		json.NewDecoder(r.Body).Decode(&req)
		u := strings.TrimSpace(req.URL)

		if u == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No URL provided"})
			return
		}
		if !isAllowedURL(u) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only http/https URLs are allowed"})
			return
		}

		info, err := infoJSON(u)
		if err != nil {
			log.Printf("info failed url=%s: %v", u, err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"title":     str(info["title"]),
			"thumbnail": str(info["thumbnail"]),
			"duration":  num(info["duration"]),
			"uploader":  str(info["uploader"]),
			"formats":   buildQualityList(info),
		})
	})

	mux.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
			return
		}
		var req struct {
			URL      string `json:"url"`
			Mode     string `json:"mode"`
			FormatID string `json:"format_id"`
			Title    string `json:"title"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		u := strings.TrimSpace(req.URL)
		mode := strings.TrimSpace(req.Mode)
		if mode == "" {
			mode = "video"
		}
		formatID := strings.TrimSpace(req.FormatID)

		if u == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No URL provided"})
			return
		}
		if !isAllowedURL(u) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only http/https URLs are allowed"})
			return
		}
		if mode != "video" && !audioModeRe.MatchString(mode) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid mode"})
			return
		}
		if formatID != "" && !formatIDRe.MatchString(formatID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid format id"})
			return
		}
		if mode != "video" {
			formatID = ""
		}

		id := jobID(u, mode, formatID)
		dir, err := downloadsDir()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		createdNew := false
		jobs.Lock()
		job := jobs.m[id]
		if job == nil {
			job = &Job{
				JobID:       id,
				URL:         u,
				Title:       strings.TrimSpace(req.Title),
				Mode:        mode,
				FormatID:    formatID,
				Status:      "queued",
				Stage:       "queued",
				DownloadDir: dir,
			}
			jobs.m[id] = job
			createdNew = true
		}
		jobs.Unlock()

		job.mu.RLock()
		status, filename, file := job.Status, job.Filename, job.File
		job.mu.RUnlock()

		if !createdNew {
			switch status {
			case "done":
				if file != "" && fileExists(file) {
					writeJSON(w, http.StatusOK, map[string]any{
						"job_id": id, "status": "done", "existing": true,
						"filename": filename, "resumed": false,
					})
					return
				}
			case "queued", "downloading", "processing":
				writeJSON(w, http.StatusOK, map[string]any{
					"job_id": id, "status": status, "existing": true, "resumed": hasPartial(dir, id),
				})
				return
			}

			job.set(func() {
				job.Status = "queued"
				job.Stage = "queued"
				job.Error = ""
				job.DownloadDir = dir
			})
		}

		jobQueue <- id

		writeJSON(w, http.StatusOK, map[string]any{
			"job_id": id, "status": "queued", "existing": !createdNew, "resumed": hasPartial(dir, id),
		})
	})

	mux.HandleFunc("/api/status/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/status/")
		job := getJob(id)
		if job == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Job not found"})
			return
		}
		writeJSON(w, http.StatusOK, job.snapshot())
	})

	mux.HandleFunc("/api/cancel/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/cancel/")
		if !cancelJob(id) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Job not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	})

	mux.HandleFunc("/api/open/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/open/")
		job := getJob(id)
		if job == nil || job.Status != "done" || job.File == "" || !fileExists(job.File) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not ready"})
			return
		}
		openInExplorer(job.File)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/api/file/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/file/")
		job := getJob(id)
		if job == nil || job.Status != "done" || job.File == "" || !fileExists(job.File) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not ready"})
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(job.Filename))
		http.ServeFile(w, r, job.File)
	})

	return mux
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := readEmbed("index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func readEmbed(name string) ([]byte, error) {
	return fs.ReadFile(webFiles(), name)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) any {
	if f, ok := v.(float64); ok && f > 0 {
		return f
	}
	return nil
}

func intOf(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}

func floatOf(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func buildQualityList(info map[string]any) []map[string]any {
	rawFormats, _ := info["formats"].([]any)
	bestByHeight := map[int64]map[string]any{}

	for _, rf := range rawFormats {
		fm, _ := rf.(map[string]any)
		if fm == nil {
			continue
		}
		height := intOf(fm["height"])
		vcodec := strings.ToLower(str(fm["vcodec"]))
		if height <= 0 || vcodec == "none" {
			continue
		}
		cur, ok := bestByHeight[height]
		if !ok || floatOf(fm["tbr"]) > floatOf(cur["tbr"]) {
			bestByHeight[height] = fm
		}
	}

	out := []map[string]any{}
	for h, fm := range bestByHeight {
		vcodec := strings.ToLower(str(fm["vcodec"]))
		ext := strings.ToLower(str(fm["ext"]))
		compatible := ext == "mp4" || strings.Contains(vcodec, "avc") || strings.Contains(vcodec, "h264")
		label := fmt.Sprintf("%dp", h)
		if !compatible {
			label += " (alt)"
		}
		out = append(out, map[string]any{
			"id":         str(fm["format_id"]),
			"label":      label,
			"height":     h,
			"compatible": compatible,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		ci := out[i]["compatible"].(bool)
		cj := out[j]["compatible"].(bool)
		if ci != cj {
			return ci
		}
		return out[i]["height"].(int64) > out[j]["height"].(int64)
	})
	return out
}
