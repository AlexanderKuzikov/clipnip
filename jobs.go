package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Job struct {
	JobID    string
	URL      string
	Title    string
	Mode     string
	FormatID string

	mu       sync.RWMutex
	Status   string // queued | downloading | processing | done | error
	Stage    string
	Progress float64
	Resumed  bool
	Error    string
	Filename string
	File     string

	Downloaded int64
	Total      int64
	Speed      int64
	ETA        int64

	Retries       int       // сетевые повторы (requeue)
	NextRetryAt   time.Time // когда retry_wait → queued
	FirstRetryAt  time.Time // начало цепочки ретраев (для потолка retryTotalTimeout)
	Running       bool      // защита от двойного запуска
	Stuck         bool      // watchdog пометил зависшим (байты не растут)
	LastDataAt    time.Time // последний рост downloaded_bytes

	pidMu       sync.Mutex
	pid         int
	DownloadDir string
}

func (j *Job) isCancelled() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status == "cancelled"
}

func (j *Job) isPaused() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status == "paused"
}

func (j *Job) isStuck() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Stuck
}

// claim захватывает джоб для запуска воркером (один запуск, даже если
// джоб попал в очередь дважды — resume/requeue поверх pause).
func (j *Job) claim() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Running || j.Status != "queued" {
		return false
	}
	j.Running = true
	return true
}

func (j *Job) unclaim() {
	j.mu.Lock()
	j.Running = false
	j.mu.Unlock()
}

func (j *Job) snapshot() map[string]any {
	j.mu.RLock()
	defer j.mu.RUnlock()
	retryIn := 0
	if j.Status == "retry_wait" && !j.NextRetryAt.IsZero() {
		sec := int(time.Until(j.NextRetryAt).Seconds())
		if sec < 0 {
			sec = 0
		}
		retryIn = sec
	}
	return map[string]any{
		"status":           j.Status,
		"stage":            j.Stage,
		"progress":         j.Progress,
		"resumed":          j.Resumed,
		"error":            j.Error,
		"filename":         j.Filename,
		"downloaded_human": humanBytes(j.Downloaded),
		"total_human":      humanBytes(j.Total),
		"speed_human":      humanBytes(j.Speed),
		"eta_human":        humanETA(j.ETA),
		"retries":          j.Retries,
		"retry_in":         retryIn,
	}
}

func (j *Job) set(u func()) {
	j.mu.Lock()
	u()
	j.mu.Unlock()
}

var jobs = struct {
	sync.Mutex
	m map[string]*Job
}{m: make(map[string]*Job)}

var jobQueue = make(chan string, 1024)
var retryQueue = make(chan string, 256)

// ---- адаптивная параллельность ----

const (
	startParallel  = 8  // стартовый уровень
	maxParallel    = 10 // жёсткий потолок (YouTube режет при большом числе сессий)
	minParallel    = 1  // пол
	successStep    = 15 // +1 за N успешных подряд
	failDivisor    = 2  // ÷N при сетевом отказе
	cooldown429    = 30 * time.Second
	cooldown403    = 15 * time.Second
	maxRetries     = 2            // повторов после сетевого отказа, дальше — error
	retryBaseDelay = 5 * time.Second // backoff: 5с, 10с...
	stuckTimeout   = 60 * time.Second // watchdog: нет роста байтов дольше stuckTimeout → джоб «завис»
	retryTotalTimeout = 90 * time.Second // потолок суммарного времени в retry_wait → error
)

var adapt = struct {
	sync.Mutex
	cond          *sync.Cond
	current       int
	successes     int
	active        int
	cooldownUntil time.Time
	paused        bool
}{}

func init() {
	adapt.current = startParallel
	adapt.cond = sync.NewCond(&adapt.Mutex)
}

// acquire блокирует воркера, пока не освободится пермит, не выйдет cooldown
// или не будет снята общая пауза.
func acquire() {
	adapt.Lock()
	defer adapt.Unlock()
	for {
		if adapt.paused {
			adapt.cond.Wait()
			continue
		}
		now := time.Now()
		if now.Before(adapt.cooldownUntil) && adapt.active == 0 {
			wait := adapt.cooldownUntil.Sub(now)
			adapt.Unlock()
			log.Printf("parallel: cooldown %s (429)", wait.Round(time.Second))
			time.Sleep(wait)
			adapt.Lock()
			continue
		}
		if adapt.active < adapt.current {
			adapt.active++
			return
		}
		adapt.cond.Wait()
	}
}

func pauseAll() {
	adapt.Lock()
	adapt.paused = true
	adapt.Unlock()
}

func resumeAll() {
	adapt.Lock()
	adapt.paused = false
	adapt.cond.Broadcast()
	adapt.Unlock()
}

func release() {
	adapt.Lock()
	adapt.active--
	adapt.cond.Broadcast()
	adapt.Unlock()
}

func adaptSuccess() {
	adapt.Lock()
	defer adapt.Unlock()
	adapt.successes++
	if adapt.successes >= successStep && adapt.current < maxParallel {
		adapt.successes = 0
		adapt.current++
		log.Printf("parallel: %d -> %d (%d successes in a row)", adapt.current-1, adapt.current, successStep)
		adapt.cond.Broadcast()
	}
}

func adaptFailure(kind string) {
	adapt.Lock()
	defer adapt.Unlock()
	adapt.successes = 0
	if adapt.current > minParallel {
		adapt.current /= failDivisor
		if adapt.current < minParallel {
			adapt.current = minParallel
		}
	}
	switch kind {
	case "429":
		adapt.cooldownUntil = time.Now().Add(cooldown429)
	case "403":
		adapt.cooldownUntil = time.Now().Add(cooldown403)
	}
	log.Printf("parallel: -> %d (failure: %s)", adapt.current, kind)
	adapt.cond.Broadcast()
}

func startWatchdog() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			jobs.Lock()
			ids := make([]string, 0, len(jobs.m))
			for id := range jobs.m {
				ids = append(ids, id)
			}
			jobs.Unlock()
			for _, id := range ids {
				job := getJob(id)
				if job == nil {
					continue
				}
				job.mu.RLock()
				if job.Status != "downloading" || job.LastDataAt.IsZero() {
					job.mu.RUnlock()
					continue
				}
				lastData := job.LastDataAt
				job.mu.RUnlock()
				if now.Sub(lastData) > stuckTimeout {
					log.Printf("watchdog: stuck job=%s no data for %s", id, now.Sub(lastData).Round(time.Second))
					job.pidMu.Lock()
					pid := job.pid
					job.pidMu.Unlock()
					if pid > 0 {
						killTree(pid)
					}
					job.set(func() { job.Stuck = true })
				}
			}
		}
	}()
}

func startWorkers() {
	for i := 0; i < maxParallel; i++ {
		go func() {
			for {
				var id string
				// приоритет: упавшие/резюмированные раньше новых
				select {
				case id = <-retryQueue:
				default:
					select {
					case id = <-retryQueue:
					case id = <-jobQueue:
					}
				}
				acquire()
				job := getJob(id)
				if job != nil && !job.isCancelled() && !job.isPaused() && job.claim() {
					runDownload(job)
					job.unclaim()
				}
				release()
			}
		}()
	}
}

func getJob(id string) *Job {
	jobs.Lock()
	defer jobs.Unlock()
	return jobs.m[id]
}

func jobID(url, mode, formatID string) string {
	h := sha1.Sum([]byte(url + "|" + mode + "|" + formatID))
	return hex.EncodeToString(h[:])[:10]
}

func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads", "ClipNip")
}

func getEffectiveDownloadDir() string {
	if dir := getDownloadDir(); dir != "" {
		return dir
	}
	return defaultDownloadDir()
}

func downloadsDir() (string, error) {
	dir := getEffectiveDownloadDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func cleanupStaleParts() {
	dir, err := downloadsDir()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	matches, _ := filepath.Glob(filepath.Join(dir, "*.part"))
	ytdlMatches, _ := filepath.Glob(filepath.Join(dir, "*.ytdl"))
	matches = append(matches, ytdlMatches...)
	for _, p := range matches {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().Before(cutoff) {
			os.Remove(p)
		}
	}
}

func hasPartial(dir, id string) bool {
	for _, pattern := range []string{id + ".*.part", id + ".part", id + ".*.ytdl", id + ".ytdl"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(matches) > 0 {
			return true
		}
	}
	return false
}

func cleanupTempFiles(dir, id string) {
	for _, pattern := range []string{id + ".*.part", id + ".part", id + ".*.ytdl", id + ".ytdl"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, p := range matches {
			os.Remove(p)
		}
	}
}

func findFinalFile(dir, id, mode string) string {
	files := []string{}
	matches, _ := filepath.Glob(filepath.Join(dir, id+".*"))
	for _, f := range matches {
		low := strings.ToLower(f)
		if strings.HasSuffix(low, ".part") || strings.HasSuffix(low, ".ytdl") {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return ""
	}

	var preferred []string
	switch mode {
	case "video":
		preferred = []string{".mp4", ".mkv", ".webm"}
	case "m4a":
		preferred = []string{".m4a", ".webm", ".opus"}
	default:
		preferred = []string{".mp3", ".m4a", ".webm", ".opus"}
	}

	sortByModTimeDesc(files)
	for _, ext := range preferred {
		for _, f := range files {
			if strings.HasSuffix(strings.ToLower(f), ext) {
				return f
			}
		}
	}
	return files[0]
}

func sortByModTimeDesc(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		fi, _ := os.Stat(paths[i])
		fj, _ := os.Stat(paths[j])
		return fi.ModTime().After(fj.ModTime())
	})
}

var (
	formatIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	audioModeRe = regexp.MustCompile(`^(m4a|mp3_128|mp3_192)$`)
)

func buildDownloadArgs(job *Job) []string {
	outtmpl := filepath.Join(job.DownloadDir, job.JobID+".%(ext)s")
	base := []string{
		"--no-playlist",
		"--output", outtmpl,
		"--continue",
		"--retries", "10",
		"--fragment-retries", "10",
		"--extractor-retries", "5",
		"--socket-timeout", "15",
	}

	switch job.Mode {
	case "video":
		if job.FormatID != "" {
			base = append(base, "--format", job.FormatID+"+bestaudio[ext=m4a]/bestaudio")
		} else {
			base = append(base, "--format",
				"bestvideo[vcodec*=avc1][ext=mp4]+bestaudio[ext=m4a]/bestvideo[ext=mp4]+bestaudio[ext=m4a]/bestvideo+bestaudio/best")
		}
		base = append(base, "--merge-output-format", "mp4")
	case "m4a":
		base = append(base, "--format", "bestaudio[ext=m4a]/bestaudio")
	default:
		quality := "128"
		if job.Mode == "mp3_192" {
			quality = "192"
		}
		base = append(base, "--format", "bestaudio[ext=m4a]/bestaudio",
			"--extract-audio", "--audio-format", "mp3", "--audio-quality", quality)
	}
	return base
}

var fatalErrRe = regexp.MustCompile(`(?i)unsupported url|private video|this video is private|video unavailable|this video is not available|not available in your country|unavailable in your country|has been removed|copyright|requested format is not available|http error 404|invalid url|sign in to confirm|age-restricted`)

// classifyError: "fatal" — ретраить бессмысленно; "429"/"403" — перегрузка (cooldown);
// "network" — временный сбой (в т.ч. ложный «Video unavailable» при блокировках).
func classifyError(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "429") || strings.Contains(lower, "too many requests"):
		return "429"
	case strings.Contains(lower, "http error 403"):
		return "403"
	case fatalErrRe.MatchString(lower):
		return "fatal"
	default:
		return "network"
	}
}

func runDownload(job *Job) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("runDownload panic job=%s: %v", job.JobID, r)
			job.set(func() {
				job.Status = "error"
				job.Stage = "error"
				job.Error = "Internal error: " + fmt.Sprint(r)
			})
		}
	}()

	dir := job.DownloadDir
	resumed := hasPartial(dir, job.JobID)

	job.set(func() {
		job.Status = "downloading"
		job.Stage = "downloading"
		job.Error = ""
		job.Progress = 0
		job.Resumed = resumed
	})

	if job.Title == "" {
		if title, err := fetchTitle(job.URL); err == nil {
			job.set(func() { job.Title = title })
		}
	}

	onProgress := func(st progressState) {
		job.set(func() {
			if job.Status == "cancelled" || job.Status == "paused" {
				return
			}
			job.Status = "downloading"
			job.Stage = "downloading"
			job.Error = ""
			job.Downloaded = st.Downloaded
			job.Total = st.Total
			job.Speed = parseSpeed(st.Speed)
			job.ETA = parseETA(st.ETA)
			if st.Downloaded > 0 {
				job.LastDataAt = time.Now()
			}
			if job.Total > 0 {
				job.Progress = round1(float64(job.Downloaded) / float64(job.Total) * 100)
			} else if job.Progress == 0 {
				job.Progress = round1(parsePercent(st.Percent))
			}
		})
	}

	var lastErr string
	formatFallback := false
	for {
		if job.isCancelled() {
			markCancelled(job)
			return
		}
		args := append(buildDownloadArgs(job), job.URL)
		err := runYtDlp(job, args, onProgress)
		if err == nil {
			lastErr = ""
			break
		}
		lastErr = err.Error()
		if strings.Contains(lastErr, "cancelled") {
			markCancelled(job)
			return
		}
		if strings.Contains(lastErr, "killed: paused") {
			// юзер поставил на паузу — процесс убит, .part сохранён; статус мог быть
			// перетёрт последним тиком прогресса — возвращаем paused
			job.set(func() {
				job.Status = "paused"
				job.Stage = "paused"
			})
			return
		}
		if job.isStuck() {
			// watchdog пометил джоб зависшим (скачивание не идёт, байты не растут) —
			// не рекьюим, сразу в error с понятным сообщением
			job.set(func() {
				job.Status = "error"
				job.Stage = "error"
				job.Error = "Download stuck — no data received for " + stuckTimeout.Round(time.Second).String() + ". YouTube не отдаёт файл."
			})
			return
		}
		// видео воспроизводится, но выбранное качество недоступно — пробуем дефолтный формат
		if strings.Contains(strings.ToLower(lastErr), "requested format is not available") && job.FormatID != "" && !formatFallback {
			formatFallback = true
			job.set(func() { job.FormatID = "" })
			log.Printf("format fallback job=%s: retry with default format", job.JobID)
			continue
		}
		kind := classifyError(lastErr)
		if kind == "fatal" {
			break
		}
		adaptFailure(kind)

		job.set(func() { job.Retries++ })
		job.mu.RLock()
		retries := job.Retries
		job.mu.RUnlock()

		if retries > maxRetries {
			log.Printf("download failed job=%s url=%s after %d retries: %s", job.JobID, job.URL, maxRetries, lastErr)
			break
		}

		delay := time.Duration(retries) * retryBaseDelay
		log.Printf("requeue job=%s kind=%s retry=%d/%d delay=%s", job.JobID, kind, retries, maxRetries, delay.Round(time.Second))
		job.set(func() {
			// юзер мог поставить на паузу, пока мы разбирались с ошибкой
			if job.Status == "paused" {
				return
			}
			job.Status = "retry_wait"
			job.Stage = "retry_wait"
			job.Error = ""
			job.NextRetryAt = time.Now().Add(delay)
			if job.FirstRetryAt.IsZero() {
				job.FirstRetryAt = time.Now()
			}
		})
		if job.Status == "paused" {
			return
		}
		// потолок суммарного времени в retry — иначе мёртвый джоб забивает retryQueue
		job.mu.RLock()
		first := job.FirstRetryAt
		job.mu.RUnlock()
		if time.Since(first) > retryTotalTimeout {
			log.Printf("retry timeout job=%s: spent %s in retry", job.JobID, time.Since(first).Round(time.Second))
			job.set(func() {
				job.Status = "error"
				job.Stage = "error"
				job.Error = "Retry timeout — слишком долго не удавалось скачать"
			})
			return
		}
		// джоб вернётся в приоритетную очередь через backoff; слот освобождается сразу
		go func(id string, d time.Duration) {
			time.Sleep(d)
			j := getJob(id)
			if j == nil {
				return
			}
			j.mu.Lock()
			if j.Status != "retry_wait" {
				j.mu.Unlock()
				return
			}
			j.Status = "queued"
			j.Stage = "queued"
			j.mu.Unlock()
			select {
			case retryQueue <- id:
			default:
				jobQueue <- id
			}
		}(job.JobID, delay)
		return // слот освобождается; джоб сам вернётся в очередь
	}

	if lastErr != "" {
		job.set(func() {
			job.Status = "error"
			job.Stage = "error"
			job.Error = lastErr
		})
		log.Printf("download failed job=%s url=%s: %s", job.JobID, job.URL, lastErr)
		return
	}

	job.set(func() {
		job.Status = "processing"
		job.Stage = "processing"
		job.Progress = 100
	})

	final := findFinalFile(dir, job.JobID, job.Mode)
	if final == "" || !fileExists(final) {
		job.set(func() {
			job.Status = "error"
			job.Stage = "error"
			job.Error = "Download finished, but final file was not found"
		})
		log.Printf("final file not found job=%s mode=%s dir=%s", job.JobID, job.Mode, dir)
		return
	}

	ext := filepath.Ext(final)
	title := sanitizeFilename(job.Title, job.JobID)
	filename := title + ext
	cleanupTempFiles(dir, job.JobID)

	if renamed, err := renameTo(final, filename); err == nil {
		final = renamed
	}

	job.set(func() {
		job.Status = "done"
		job.Stage = "done"
		job.Progress = 100
		job.File = final
		job.Filename = filename
		job.Speed = 0
		job.ETA = 0
	})
	adaptSuccess()
}

func markCancelled(job *Job) {
	job.set(func() {
		job.Status = "cancelled"
		job.Stage = "error"
		job.Error = "cancelled by user"
	})
}

func renameTo(path, filename string) (string, error) {
	dest := filepath.Join(filepath.Dir(path), filename)
	if strings.EqualFold(dest, path) {
		return path, nil
	}
	if _, err := os.Stat(dest); err == nil {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		for i := 1; ; i++ {
			dest = filepath.Join(filepath.Dir(path), fmt.Sprintf("%s (%d)%s", base, i, ext))
			if _, err := os.Stat(dest); os.IsNotExist(err) {
				break
			}
		}
	}
	if err := os.Rename(path, dest); err != nil {
		return path, err
	}
	return dest, nil
}

func openInExplorer(path string) error {
	return exec.Command("explorer", "/select,", path).Start()
}

func parseSpeed(s string) int64 {
	if s == "" || strings.EqualFold(s, "NA") {
		return 0
	}
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	var num string
	switch {
	case strings.HasSuffix(s, "kib/s"):
		mult = 1024
		num = strings.TrimSpace(strings.TrimSuffix(s, "kib/s"))
	case strings.HasSuffix(s, "mib/s"):
		mult = 1024 * 1024
		num = strings.TrimSpace(strings.TrimSuffix(s, "mib/s"))
	case strings.HasSuffix(s, "gib/s"):
		mult = 1024 * 1024 * 1024
		num = strings.TrimSpace(strings.TrimSuffix(s, "gib/s"))
	default:
		return 0
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return int64(v * float64(mult))
}

func parseETA(s string) int64 {
	if s == "" || strings.EqualFold(s, "NA") {
		return 0
	}
	parts := strings.Split(strings.TrimSpace(s), ":")
	var total int64
	for _, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	return total
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

func humanBytes(n int64) string {
	if n <= 0 {
		return ""
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	idx := 0
	for f >= 1024 && idx < len(units)-1 {
		f /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d %s", int(f), units[idx])
	}
	return fmt.Sprintf("%.1f %s", f, units[idx])
}

func humanETA(s int64) string {
	if s <= 0 {
		return ""
	}
	h, rem := s/3600, s%3600
	m, sec := rem/60, rem%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

func sanitizeFilename(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
	}
	name = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1F]`).ReplaceAllString(name, "")
	name = strings.Join(strings.Fields(name), " ")
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if name == "" {
		return fallback
	}
	if len(name) > 180 {
		name = name[:180]
	}
	return strings.TrimRight(name, ". ")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func killTreeOnCancel(job *Job) {
	job.pidMu.Lock()
	pid := job.pid
	job.pidMu.Unlock()
	if pid > 0 {
		killTree(pid)
	}
}

func cancelJob(id string) bool {
	job := getJob(id)
	if job == nil {
		return false
	}
	job.set(func() {
		if job.Status == "queued" || job.Status == "downloading" || job.Status == "processing" || job.Status == "paused" {
			job.Status = "cancelled"
			job.Stage = "error"
			job.Error = "cancelled by user"
		}
	})
	killTreeOnCancel(job)
	// отменил сам — хвост .part не нужен, убираем мусор
	cleanupTempFiles(job.DownloadDir, id)
	return true
}

func pauseJob(id string) bool {
	job := getJob(id)
	if job == nil {
		return false
	}
	job.set(func() {
		if job.Status == "queued" || job.Status == "downloading" || job.Status == "processing" || job.Status == "retry_wait" {
			job.Status = "paused"
			job.Stage = "paused"
		}
	})
	killTreeOnCancel(job)
	return true
}

func resumeJob(id string) bool {
	job := getJob(id)
	if job == nil {
		return false
	}
	job.mu.Lock()
	if job.Status != "paused" {
		job.mu.Unlock()
		return false
	}
	job.Status = "queued"
	job.Stage = "queued"
	job.Error = ""
	job.Stuck = false
	job.FirstRetryAt = time.Time{}
	job.mu.Unlock()

	select {
	case retryQueue <- id:
	default:
		jobQueue <- id
	}
	return true
}

func jobStatus(id string) string {
	job := getJob(id)
	if job == nil {
		return ""
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	return job.Status
}

func pauseAllJobs() {
	pauseAll()
	jobs.Lock()
	ids := make([]string, 0, len(jobs.m))
	for id := range jobs.m {
		ids = append(ids, id)
	}
	jobs.Unlock()
	for _, id := range ids {
		if s := jobStatus(id); s == "queued" || s == "downloading" || s == "processing" || s == "retry_wait" {
			pauseJob(id)
		}
	}
}

func resumeAllJobs() {
	resumeAll()
	jobs.Lock()
	ids := make([]string, 0, len(jobs.m))
	for id := range jobs.m {
		ids = append(ids, id)
	}
	jobs.Unlock()
	for _, id := range ids {
		if jobStatus(id) == "paused" {
			resumeJob(id)
		}
	}
}

func cancelAllJobs() {
	jobs.Lock()
	ids := make([]string, 0, len(jobs.m))
	for id := range jobs.m {
		ids = append(ids, id)
	}
	jobs.Unlock()
	for _, id := range ids {
		cancelJob(id)
	}
}
