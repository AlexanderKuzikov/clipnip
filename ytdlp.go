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
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	ytdlpGz  = "embedded/yt-dlp.exe.gz"
	ffmpegGz = "embedded/ffmpeg.exe.gz"
	denoGz   = "embedded/deno.exe.gz"

	// ytdlpVersion — версия вшитого yt-dlp. Сверяется с маркером рядом с
	// распакованным бинарником: смена версии в embed принудительно
	// перераспаковывает файл (extractEmbedded иначе не перезаписывает).
	ytdlpVersion = "2026.08.19"
	denoVersion  = "2.9.7"
)

type progressState struct {
	Percent    string
	Speed      string
	ETA        string
	Downloaded int64
	Total      int64
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

// engineInfo — версия и путь распакованного движка. Показывается в UI,
// потому что главный источник «всё сломалось» — устаревший yt-dlp, а не сеть.
var engineInfo = struct {
	sync.Mutex
	version string
	ffmpeg  bool
	deno    bool
	done    bool
}{}

// probeEngine один раз спрашивает версию у движка (~1.5 с), результат кэшируется.
func probeEngine() (string, bool, bool) {
	engineInfo.Lock()
	defer engineInfo.Unlock()
	if engineInfo.done {
		return engineInfo.version, engineInfo.ffmpeg, engineInfo.deno
	}
	engineInfo.done = true

	dir, err := binDir()
	if err == nil {
		cmd := exec.Command(filepath.Join(dir, "yt-dlp.exe"), "--version")
		cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
		cmd.SysProcAttr = noWindow()
		var out bytes.Buffer
		cmd.Stdout = &out
		if cmd.Run() == nil {
			engineInfo.version = strings.TrimSpace(out.String())
		}
		engineInfo.ffmpeg = ffmpegPath(dir) != ""
		engineInfo.deno = len(jsRuntimeArgs(dir)) > 0
	}
	return engineInfo.version, engineInfo.ffmpeg, engineInfo.deno
}

// ensureBins распаковывает yt-dlp и ffmpeg из embed при первом запуске.
func ensureBins() error {
	dir, err := binDir()
	if err != nil {
		return err
	}
	if err := extractEmbedded(dir, ytdlpGz, "yt-dlp.exe", ytdlpVersion); err != nil {
		return fmt.Errorf("yt-dlp extract: %w", err)
	}
	if err := extractEmbedded(dir, ffmpegGz, "ffmpeg.exe", ""); err != nil {
		return fmt.Errorf("ffmpeg extract: %w", err)
	}
	if err := extractEmbedded(dir, denoGz, "deno.exe", denoVersion); err != nil {
		return fmt.Errorf("deno extract: %w", err)
	}
	return nil
}

// jsRuntimeArgs указывает yt-dlp, где лежит вшитый deno.
//
// deno нужен для yt-dlp-ejs (JS-челленджи YouTube). Проверено на этой машине:
// без этого флага yt-dlp тоже находит deno — он ищет рантайм рядом со своим
// собственным exe, а binDir это и есть каталог с yt-dlp. То есть флаг не
// обязателен, а страховка от смены этого поведения. Оставлять нечем: node на
// машине отсутствует (yt-dlp пишет "node (unavailable)"), так что если бы
// deno не подхватился автоматически, JS-рантайма не было бы вообще никакого.
func jsRuntimeArgs(dir string) []string {
	p := filepath.Join(dir, "deno.exe")
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	return []string{"--js-runtimes", "deno:" + p}
}

// ffmpegPath — путь к распакованному ffmpeg, "" если его нет.
func ffmpegPath(dir string) string {
	p := filepath.Join(dir, "ffmpeg.exe")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func extractEmbedded(dir, gzPath, destName, version string) error {
	dest := filepath.Join(dir, destName)
	marker := dest + ".ver"

	if version != "" {
		if cur, err := os.ReadFile(marker); err == nil && string(cur) == version {
			if _, err := os.Stat(dest); err == nil {
				return nil
			}
		}
	} else if _, err := os.Stat(dest); err == nil {
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
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	if version != "" {
		os.WriteFile(marker, []byte(version), 0o644)
	}
	return nil
}

var errStripRe = regexp.MustCompile(`(?m)^ERROR:\s*`)

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
		// пользовательский %APPDATA%\yt-dlp\config может подсунуть свои
		// --output/--proxy/--cookies и сломать или перехватить поведение
		"--ignore-config",
		"--progress-template", progressTemplate,
	}, args...)
	args = append(args, jsRuntimeArgs(dir)...)

	// склейка видео+аудио идёт через ffmpeg из binDir, а не из PATH
	if ff := ffmpegPath(dir); ff != "" {
		args = append([]string{"--ffmpeg-location", ff}, args...)
	}
	// cookies добавляются последними: явная настройка пользователя должна
	// перебивать всё, что выше
	args = append(args, cookieArgs()...)

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
	lastProgress := atomic.Int64{}
	lastProgress.Store(time.Now().UnixNano())
	stalled := atomic.Bool{}
	stopWatch := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				last := time.Unix(0, lastProgress.Load())
				if time.Since(last) > stallTimeout && !job.isCancelled() {
					job.pidMu.Lock()
					pid := job.pid
					job.pidMu.Unlock()
					killTree(pid)
					stalled.Store(true)
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
			lastProgress.Store(time.Now().UnixNano())
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
	if stalled.Load() {
		return errors.New("stalled: no progress")
	}
	if job.isCancelled() {
		return errors.New("cancelled")
	}
	if err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg != "" {
			msg = errStripRe.ReplaceAllString(msg, "")
			msg = strings.TrimSpace(msg)
			if hint := humanizeCookieError(msg); hint != "" {
				return errors.New(hint)
			}
			return errors.New(msg)
		}
	}
	return err
}

type limitedBuffer struct{ buf []byte }

// Write держит ХВОСТ вывода: реальные ошибки yt-dlp приходят в конце,
// а первые 4КБ обычно съедает stdout-шум.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > 8192 {
		b.buf = b.buf[len(b.buf)-4096:]
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

	args := []string{"--no-warnings", "--ignore-config", "--dump-single-json"}
	if playlist {
		// плейлист: берём первые 500 записей, таймаут шире
		args = append(args, "--flat-playlist", "--playlist-items", "1-500")
	}
	// --no-playlist намеренно НЕ передаётся: yt-dlp сам различает ссылку на
	// одно видео и на страницу с несколькими (один элемент → одно видео).
	// Принудительный флаг молча урезал такие страницы до одного ролика.
	args = append(args, jsRuntimeArgs(dir)...)
	args = append(args, cookieArgs()...)
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
				msg = errStripRe.ReplaceAllString(msg, "")
				msg = strings.TrimSpace(msg)
				if hint := humanizeCookieError(msg); hint != "" {
					return nil, errors.New(hint)
				}
				return nil, errors.New(msg)
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

	args := []string{"--no-warnings", "--ignore-config", "--no-playlist", "--print", "title"}
	args = append(args, jsRuntimeArgs(dir)...)
	args = append(args, cookieArgs()...)
	args = append(args, url)

	cmd := exec.Command(ytdlp, args...)
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
