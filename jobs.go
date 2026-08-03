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

	pidMu       sync.Mutex
	pid         int
	DownloadDir string
}

func (j *Job) isCancelled() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status == "cancelled"
}

func (j *Job) snapshot() map[string]any {
	j.mu.RLock()
	defer j.mu.RUnlock()
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

var jobQueue = make(chan string, 64)

const workerCount = 3

func startWorkers() {
	for i := 0; i < workerCount; i++ {
		go func() {
			for id := range jobQueue {
				job := getJob(id)
				if job != nil {
					runDownload(job)
				}
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
		"--socket-timeout", "30",
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

	args := append(buildDownloadArgs(job), job.URL)

	err := runYtDlp(job, args, func(st progressState) {
		job.set(func() {
			if job.Status == "cancelled" {
				return
			}
			job.Status = "downloading"
			job.Stage = "downloading"
			job.Error = ""
			job.Downloaded = st.Downloaded
			job.Total = st.Total
			job.Speed = parseSpeed(st.Speed)
			job.ETA = parseETA(st.ETA)
			if job.Total > 0 {
				job.Progress = round1(float64(job.Downloaded) / float64(job.Total) * 100)
			} else if job.Progress == 0 {
				job.Progress = round1(parsePercent(st.Percent))
			}
		})
	})

	if err != nil {
		cancelled := strings.Contains(err.Error(), "cancelled")
		job.set(func() {
			if cancelled {
				job.Status = "cancelled"
			} else {
				job.Status = "error"
			}
			job.Stage = "error"
			job.Error = err.Error()
		})
		log.Printf("download failed job=%s url=%s: %v", job.JobID, job.URL, err)
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
	switch {
	case strings.HasSuffix(s, "kib/s"):
		mult = 1024
	case strings.HasSuffix(s, "mib/s"):
		mult = 1024 * 1024
	case strings.HasSuffix(s, "gib/s"):
		mult = 1024 * 1024 * 1024
	default:
		return 0
	}
	num := strings.TrimSpace(strings.TrimSuffix(s, "ib/s"))
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
		if job.Status == "queued" || job.Status == "downloading" || job.Status == "processing" {
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
