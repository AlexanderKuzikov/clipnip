package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	ytdlpGz  = "embedded/yt-dlp.exe.gz"
	ffmpegGz = "embedded/ffmpeg.exe.gz"
)

type progressState struct {
	Percent  string
	Speed    string
	ETA      string
	Downloaded int64
	Total    int64
}

func binDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "clipnip", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureBins распаковывает yt-dlp и ffmpeg из embed при первом запуске.
// Существующие файлы на диске не перезаписываются (ручное обновление возможно).
func ensureBins() error {
	dir, err := binDir()
	if err != nil {
		return err
	}
	if err := extractEmbedded(dir, ytdlpGz, "yt-dlp.exe"); err != nil {
		return fmt.Errorf("yt-dlp extract: %w", err)
	}
	if err := extractEmbedded(dir, ffmpegGz, "ffmpeg.exe"); err != nil {
		return fmt.Errorf("ffmpeg extract: %w", err)
	}
	return nil
}

func extractEmbedded(dir, gzPath, destName string) error {
	dest := filepath.Join(dir, destName)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}

	gz, err := assetsDir.Open(gzPath)
	if err != nil {
		return err
	}
	defer gz.Close()

	zr, err := gzip.NewReader(gz)
	if err != nil {
		return err
	}
	defer zr.Close()

	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, zr)
	out.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

var progressTemplate = "download:%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s"

const stallTimeout = 20 * time.Second

func runYtDlp(job *Job, args []string, onProgress func(progressState)) error {
	dir, err := binDir()
	if err != nil {
		return err
	}
	// самовосстановление: антивирус мог удалить бинарники
	if err := ensureBins(); err != nil {
		return err
	}
	ytdlp := filepath.Join(dir, "yt-dlp.exe")

	args = append([]string{
		"--no-warnings",
		"--newline",
		"--progress-template", progressTemplate,
	}, args...)

	cmd := exec.Command(ytdlp, args...)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	cmd.Dir = job.DownloadDir
	cmd.SysProcAttr = noWindow()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf limitedBuffer
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return err
	}
	job.pidMu.Lock()
	job.pid = cmd.Process.Pid
	job.pidMu.Unlock()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// stall-детект: нет прогресса дольше stallTimeout — процесс завис, убиваем
	lastProgress := time.Now()
	stalled := false
	stopWatch := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if time.Since(lastProgress) > stallTimeout && !job.isCancelled() {
					job.pidMu.Lock()
					pid := job.pid
					job.pidMu.Unlock()
					killTree(pid)
					stalled = true
					return
				}
			case <-stopWatch:
				return
			}
		}
	}()

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, "|")
		if len(parts) == 5 && strings.Contains(parts[0], "%") {
			st := progressState{
				Percent: strings.TrimSpace(parts[0]),
				Speed:   strings.TrimSpace(parts[1]),
				ETA:     strings.TrimSpace(parts[2]),
			}
			st.Downloaded = parseNAInt(parts[3])
			st.Total = parseNAInt(parts[4])
			lastProgress = time.Now()
			onProgress(st)
			continue
		}
		errBuf.Write([]byte(line + "\n"))
	}
	close(stopWatch)

	err = cmd.Wait()
	if job.isPaused() {
		return errors.New("killed: paused")
	}
	if stalled {
		return errors.New("stalled: no progress")
	}
	if job.isCancelled() {
		return errors.New("cancelled")
	}
	if err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg != "" {
			msg = regexp.MustCompile(`(?m)^ERROR:\s*`).ReplaceAllString(msg, "")
			return errors.New(strings.TrimSpace(msg))
		}
	}
	return err
}

type limitedBuffer struct{ buf []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(b.buf) < 4096 {
		b.buf = append(b.buf, p...)
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return string(b.buf) }

func parseNAInt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "NA") {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func infoJSON(url string, playlist bool) (map[string]any, error) {
	if err := ensureBins(); err != nil {
		return nil, err
	}
	dir, err := binDir()
	if err != nil {
		return nil, err
	}
	ytdlp := filepath.Join(dir, "yt-dlp.exe")

	args := []string{"--no-warnings", "--dump-single-json"}
	if playlist {
		// плейлист: берём первые 500 записей, таймаут шире
		args = append(args, "--flat-playlist", "--playlist-items", "1-500")
	} else {
		args = append(args, "--no-playlist")
	}
	args = append(args, url)

	cmd := exec.Command(ytdlp, args...)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	cmd.SysProcAttr = noWindow()

	var outBuf bytes.Buffer
	var errBuf limitedBuffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	timeout := 45 * time.Second
	if playlist {
		timeout = 90 * time.Second
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(errBuf.String())
			if msg != "" {
				msg = regexp.MustCompile(`(?m)^ERROR:\s*`).ReplaceAllString(msg, "")
				return nil, errors.New(strings.TrimSpace(msg))
			}
			return nil, err
		}
	case <-time.After(timeout):
		killTree(cmd.Process.Pid)
		<-done
		return nil, errors.New("info request timed out")
	}

	var info map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("parse info json: %w", err)
	}
	return info, nil
}

// fetchTitle получает название клипа отдельным тихим вызовом (без скачивания).
func fetchTitle(url string) (string, error) {
	dir, err := binDir()
	if err != nil {
		return "", err
	}
	ytdlp := filepath.Join(dir, "yt-dlp.exe")

	cmd := exec.Command(ytdlp, "--no-warnings", "--no-playlist", "--print", "title", url)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	cmd.SysProcAttr = noWindow()

	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		killTree(cmd.Process.Pid)
		<-done
	}
	return strings.TrimSpace(outBuf.String()), nil
}

func noWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func killTree(pid int) {
	if pid <= 0 {
		return
	}
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = noWindow()
	cmd.Run()
}
