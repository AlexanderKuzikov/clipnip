package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Сохранение очереди между запусками. Сохраняется только состав заданий
// (ссылка, режим, качество, заголовок), а не стейт-машина: после рестарта
// джобы возвращаются в очередь и докачиваются через `.part`. Полная
// персистентность состояния сознательно отклонена (ADR 001) — она дороже
// самой фичи, а `.part` и так переживает перезапуск.

type queueEntry struct {
	JobID string `json:"job_id"`
	URL   string `json:"url"`
	Mode  string `json:"mode"`
	// FormatID — пустая строка означает «качество по умолчанию»
	FormatID string `json:"format_id"`
	Title    string `json:"title"`
}

var persistMu sync.Mutex

func queueFilePath() string {
	return filepath.Join(localAppDataDir(), "queue.json")
}

// persistQueue перезаписывает файл очереди текущим составом незавершённых
// джобов. Вызывается на каждом изменении состава, поэтому запись должна быть
// дешёвой и безопасной при параллельных запросах.
func persistQueue() {
	persistMu.Lock()
	defer persistMu.Unlock()

	jobs.Lock()
	entries := make([]queueEntry, 0, len(jobs.m))
	for id, j := range jobs.m {
		j.mu.RLock()
		terminal := j.Status == "done" || j.Status == "error" ||
			j.Status == "cancelled" || j.Status == "skipped"
		if !terminal {
			entries = append(entries, queueEntry{
				JobID: id, URL: j.URL, Mode: j.Mode,
				FormatID: j.FormatID, Title: j.Title,
			})
		}
		j.mu.RUnlock()
	}
	jobs.Unlock()

	path := queueFilePath()
	if len(entries) == 0 {
		// очередь пуста — файла быть не должно
		os.Remove(path)
		return
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	os.Rename(tmp, path)
}

// restoreQueue возвращает незавершённые задания в очередь. Вызывается до
// старта воркеров, поэтому они сразу подхватятся. Дубликаты (джоб уже есть в
// памяти) пропускаются.
func restoreQueue() {
	data, err := os.ReadFile(queueFilePath())
	if err != nil {
		return
	}
	var entries []queueEntry
	if json.Unmarshal(data, &entries) != nil {
		return
	}

	dir, err := downloadsDir()
	if err != nil {
		log.Printf("queue restore: %v", err)
		return
	}

	restored := 0
	for _, e := range entries {
		mode := orDefault(e.Mode, "video")
		if !isAllowedURL(e.URL) || (mode != "video" && !audioModeRe.MatchString(mode)) {
			continue
		}
		id := jobID(e.URL, e.Mode, e.FormatID)

		jobs.Lock()
		_, exists := jobs.m[id]
		if !exists {
			jobs.m[id] = &Job{
				JobID:       id,
				URL:         e.URL,
				Title:       e.Title,
				Mode:        e.Mode,
				FormatID:    e.FormatID,
				Status:      "queued",
				Stage:       "queued",
				DownloadDir: dir,
			}
		}
		jobs.Unlock()
		if exists {
			continue
		}
		select {
		case jobQueue <- id:
			restored++
		default:
			log.Printf("queue restore: full, dropping %s", id)
		}
	}
	if restored > 0 {
		log.Printf("queue restore: %d job(s) returned to queue", restored)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
