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
	"regexp"
	"sort"
	"strings"
	"time"
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

var playlistURLRe = regexp.MustCompile(`(?i)(/playlist\?|/playlists/)`)

// cookieTestURL — стабильный ролик для пробной проверки cookies.
const cookieTestURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

// friendlyErrorBackend — серверный аналог одноимённой функции в UI.
// UI версия оставлена для показа, здесь — чтобы не тащить её в Go.
func friendlyErrorBackend(err string) string {
	lower := strings.ToLower(err)
	switch {
	case strings.Contains(lower, "unable to download video data"),
		strings.Contains(lower, "unable to download api page"):
		return "Движок не может получить поток — версия yt-dlp устарела."
	case strings.Contains(lower, "sign in to confirm"),
		strings.Contains(lower, "http error 401"),
		strings.Contains(lower, "http error 403"):
		return "Сайт отказал — нужна авторизация. Cookies не помогли."
	case strings.Contains(lower, "http error 429"):
		return "Сайт временно ограничил запросы."
	case strings.Contains(lower, "timed out"):
		return "Таймаут — проверьте доступность сайта."
	}
	// Обрезка по рунам: []byte-слайс по кириллице даёт битую последнюю букву.
	if r := []rune(err); len(r) > 140 {
		return string(r[:140]) + "..."
	}
	return err
}

func isPlaylistURL(raw string) bool {
	return playlistURLRe.MatchString(raw)
}

func playlistEntries(info map[string]any) []map[string]any {
	rawEntries, _ := info["entries"].([]any)
	out := []map[string]any{}
	for _, re := range rawEntries {
		e, _ := re.(map[string]any)
		if e == nil {
			continue
		}
		id := str(e["id"])
		videoURL := str(e["url"])
		if !strings.HasPrefix(videoURL, "http") {
			if id == "" {
				continue
			}
			videoURL = "https://www.youtube.com/watch?v=" + id
		}
		thumb := str(e["thumbnail"])
		if thumb == "" && id != "" {
			// --flat-playlist не отдаёт превью; для YouTube адрес детерминирован
			thumb = "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
		}
		out = append(out, map[string]any{
			"url":       videoURL,
			"title":     str(e["title"]),
			"duration":  num(e["duration"]),
			"thumbnail": thumb,
		})
	}
	return out
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

	mux.HandleFunc("/api/engine", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
			return
		}
		version, hasFFmpeg, hasDeno := probeEngine()
		writeJSON(w, http.StatusOK, map[string]any{
			"version":  version,
			"embedded": ytdlpVersion,
			"stale":    version != "" && version != ytdlpVersion,
			"ffmpeg":   hasFFmpeg,
			"deno":     hasDeno,
		})
	})

	mux.HandleFunc("/api/reach", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		u := strings.TrimSpace(req.URL)
		if !isAllowedURL(u) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only http/https URLs are allowed"})
			return
		}
		writeJSON(w, http.StatusOK, probeReach(u))
	})

	mux.HandleFunc("/api/cookies", func(w http.ResponseWriter, r *http.Request) {
		config.RLock()
		current := map[string]any{
			"enabled":   config.CookiesEnabled,
			"spec":      config.CookiesSpec,
			"file":      config.CookiesFile,
			"ack":       config.CookiesAck,
			"active":    cookiesSpec() != "" || cookiesFilePath() != "",
			"suggested": suggestedCookieSpec(),
		}
		config.RUnlock()

		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{
				"config":   current,
				"browsers": detectCookieBrowsers(),
			})

		case http.MethodPost:
			var req struct {
				Enabled bool   `json:"enabled"`
				Spec    string `json:"spec"`
				File    string `json:"file"`
				Ack     bool   `json:"ack"`
			}
			json.NewDecoder(r.Body).Decode(&req)

			spec := strings.TrimSpace(req.Spec)
			if spec != "" && !cookiesSpecRe.MatchString(spec) {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "Некорректная спецификация браузера. Пример: chrome или edge:Profile 1",
				})
				return
			}
			if err := setCookies(req.Enabled, spec, req.File, req.Ack); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			config.RLock()
			current["enabled"] = config.CookiesEnabled
			current["spec"] = config.CookiesSpec
			current["file"] = config.CookiesFile
			current["ack"] = config.CookiesAck
			config.RUnlock()
			current["active"] = cookiesSpec() != "" || cookiesFilePath() != ""
			writeJSON(w, http.StatusOK, map[string]any{"config": current})

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		}
	})

	// Пробная авторизация: сухой запуск /api/info с текущими cookies.
	// Нужен потому, что сбой расшифровки DPAPI у yt-dlp роняет весь процесс
	// («force exit»), то есть включённые cookies способны сломать вообще всё.
	mux.HandleFunc("/api/cookies/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		target := strings.TrimSpace(req.URL)
		if target == "" {
			target = cookieTestURL
		}
		if !isAllowedURL(target) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only http/https URLs are allowed"})
			return
		}

		_, err := infoJSON(target, false)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "detail": "Cookies применены, доступ получен.",
			})
			return
		}
		msg := err.Error()
		if hint := humanizeCookieError(msg); hint != "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": hint})
			return
		}
		kind := classifyError(msg)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "kind": kind, "detail": friendlyErrorBackend(msg), "error": msg,
		})
	})

	// Хвост лога: диагностика не должна требовать захода в AppData.
	mux.HandleFunc("/api/log", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			data, err := os.ReadFile(logFilePath())
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]string{
					"path": logFilePath(), "tail": "log is empty",
				})
				return
			}
			const maxTail = 24 * 1024
			tail := string(data)
			if len(tail) > maxTail {
				tail = "...(truncated)...\n" + tail[len(tail)-maxTail:]
			}
			writeJSON(w, http.StatusOK, map[string]string{"path": logFilePath(), "tail": tail})

		case http.MethodPost:
			if err := openLogFile(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		}
	})

	// Статус компонентов: откуда взят каждый и что с ним не так.
	mux.HandleFunc("/api/components", func(w http.ResponseWriter, r *http.Request) {
		snapshot := func() map[string]any {
			return map[string]any{
				"url":        componentsBaseURL(),
				"portable":   portableBinDir(),
				"components": componentStates,
				"ready":      componentsReady(requiredComponents()),
				"missing":    missingComponents(requiredComponents()),
				"problem":    componentsProblem,
			}
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, snapshot())

		case http.MethodPost:
			var req struct {
				URL     string `json:"url"`
				Refresh bool   `json:"refresh"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if u := strings.TrimSpace(req.URL); u != "" {
				parsed, err := url.Parse(u)
				if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{
						"error": "Нужен полный https-адрес, например https://example.com/clipnip",
					})
					return
				}
				config.Lock()
				config.ComponentsURL = strings.TrimRight(u, "/")
				config.Unlock()
				saveConfig()
			}
			// refresh=true — переподключиться сразу, без перезапуска:
			// именно этот путь восстанавливает приложение, у которого сняли
			// папку с компонентами
			if req.Refresh {
				ensureBins()
			}
			writeJSON(w, http.StatusOK, snapshot())

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		}
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
		var req struct {
			URL string `json:"url"`
		}
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

		isPlaylist := isPlaylistURL(u)
		// Проба сети идёт параллельно извлечению метаданных: на заблокированном
		// домене yt-dlp будет висеть до 45 с, а вердикт пробы готов за секунды.
		reachCh := probeReachAsync(u)
		info, err := infoJSON(u, isPlaylist)
		if err != nil {
			reach := waitReach(reachCh, 6*time.Second)
			log.Printf("info failed url=%s reach=%s: %v", u, reach.Verdict, err)
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":        err.Error(),
				"reach":        reach.Verdict,
				"reach_detail": reach.Detail,
			})
			return
		}

		if entries := playlistEntries(info); len(entries) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"playlist":       true,
				"playlist_title": str(info["title"]),
				"entries":        entries,
			})
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
			Force    bool   `json:"force"`
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
				job.Retries = 0
				job.Stuck = false
				job.FirstRetryAt = time.Time{}
				job.RetryWait = 0
				job.DownloadDir = dir
				job.Force = req.Force
			})
		}

		select {
		case jobQueue <- id:
		default:
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "Queue is full, try again later",
			})
			return
		}

		persistQueue()
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

	mux.HandleFunc("/api/pause/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/pause/")
		if !pauseJob(id) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Job not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
	})

	mux.HandleFunc("/api/resume/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/resume/")
		if !resumeJob(id) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Job not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
	})

	mux.HandleFunc("/api/pauseall", func(w http.ResponseWriter, r *http.Request) {
		pauseAllJobs()
		writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
	})

	mux.HandleFunc("/api/resumeall", func(w http.ResponseWriter, r *http.Request) {
		resumeAllJobs()
		writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
	})

	mux.HandleFunc("/api/cancelall", func(w http.ResponseWriter, r *http.Request) {
		cancelAllJobs()
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

// buildQualityList собирает список качеств из форматов yt-dlp.
//
// В yt-dlp 2026.08+ прогрессивных форматов (18/22) больше нет — остались
// DASH (https) и HLS (m3u8). Наивный выбор «max tbr на высоту» стабильно
// отдавал m3u8: у HLS битрейт всегда выше, а у него total_bytes иногда NA,
// из-за чего прогресс-бар теряет точность. Поэтому при равной высоте
// https-DASH предпочтительнее, и выбор идёт уже по tbr.
// betterFormat: https-DASH предпочтительнее m3u8, дальше по tbr.
// Высота уже совпадает — сравниваем только формат одного уровня.
func betterFormat(cand, cur map[string]any) bool {
	ch, chOK := isHLSFormat(cand)
	uh, uhOK := isHLSFormat(cur)
	if chOK && uhOK && ch != uh {
		// протоколы известны и различаются: https выигрывает у m3u8,
		// поэтому «лучше» здесь — обратное признаку «кандидат m3u8»
		return !ch
	}
	return floatOf(cand["tbr"]) > floatOf(cur["tbr"])
}

func isHLSFormat(fm map[string]any) (hls bool, known bool) {
	switch strings.ToLower(str(fm["protocol"])) {
	case "https":
		return false, true
	case "m3u8", "m3u8_native", "http":
		return true, true
	}
	return false, false
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
		if !ok || betterFormat(fm, cur) {
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
