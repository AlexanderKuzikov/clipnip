package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	// ytdlpVersion — версия ожидаемого вшитого yt-dlp. Сверяется с маркером
	// рядом с распакованным бинарником: смена версии в embed принудительно
	// перераспаковывает файл (extractEmbedded иначе не перезаписывает).
	// Канал — nightly, а не stable: YouTube ломает player-клипы каждые
	// несколько недель, и фикс приезжает в master за дни. Обоснование и
	// риски — ADR 006. Пути к архивам объявлены в bins_embedded.go /
	// bins_remote.go: сборка clipnipremote ничего не вшивает.
	ytdlpVersion = "2026.09.27.232945"
	denoVersion  = "2.9.7"
)

type progressState struct {
	Percent    string
	Speed      string
	ETA        string
	Downloaded int64
	Total      int64
}

// binDirOverride — переопределение каталога бинарников. Нужно тестам, чтобы
// они не трогали реальный %LOCALAPPDATA% и не распаковывали 98 МБ.
var binDirOverride string

func binDir() (string, error) {
	if binDirOverride != "" {
		if err := os.MkdirAll(binDirOverride, 0o755); err != nil {
			return "", err
		}
		return binDirOverride, nil
	}
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
		cmd := exec.Command(ytdlpPath(), "--version")
		cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
		cmd.SysProcAttr = noWindow()
		var out bytes.Buffer
		cmd.Stdout = &out
		if cmd.Run() == nil {
			engineInfo.version = strings.TrimSpace(out.String())
		}
		engineInfo.ffmpeg = ffmpegPath(dir) != ""
		engineInfo.deno = len(jsRuntimeArgs()) > 0
	}
	return engineInfo.version, engineInfo.ffmpeg, engineInfo.deno
}

// binSpec — встроенный бинарник автономной сборки. sha256 обязателен: embed —
// единственный источник истины для содержимого, а распакованный файл лежит в
// пользовательской папке. Пустой gz означает «в этой сборке не вшит».
type binSpec struct {
	name    string
	gz      string
	version string
	sha256  string
}

// embeddedBins — компоненты, которыми ClipNip управляет сам. Порядок не важен.
// deno присутствует только в полной сборке: в облегчённой denoGz пуст, и
// компонент пропускается целиком.
var embeddedBins = []binSpec{
	{"yt-dlp.exe", ytdlpGz, ytdlpVersion, "997c00e8f8ed91431b1ddaeeae519743602beb0b6df0903194a2632e57df1b31"},
	{"deno.exe", denoGz, denoVersion, "e020f3e232bd16e33768dee528e5983349c962952051ced0a5d58ad42f5d9b33"},
	{"ffmpeg.exe", ffmpegGz, "8.1.2", "1326dde4c84ff1f96fe6b8916c5bed29e163e9b5dccf995f6f3db069d143ec5e"},
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// requiredComponents — без чего ClipNip не работает. deno сюда не входит:
// это страховка для машин без JS-рантайма, а не условие работы (проверено:
// YouTube скачивается и без него).
func requiredComponents() []string {
	return []string{"yt-dlp.exe", "ffmpeg.exe"}
}

// optionalComponents — используются, если найдены, и никогда не мешают.
func optionalComponents() []string {
	return []string{"deno.exe"}
}

// componentsProblem — человеческое описание того, чего не хватает, или "".
var componentsProblem string

// ensureBins готовит компоненты к работе: portable-папка → кэш → сервер.
//
// Недостающие компоненты НЕ фатальны: приложение обязано подняться, чтобы
// пользователь мог зайти в настройки и указать адрес сервера. Фатальная
// ошибка тут означала бы «зайди в настройки» из окна, которого нет.
func ensureBins() error {
	needed := append(requiredComponents(), optionalComponents()...)
	if err := resolveComponents(needed); err != nil {
		return err
	}
	if missing := missingComponents(requiredComponents()); len(missing) > 0 {
		componentsProblem = "Не удалось получить компоненты: " + strings.Join(missing, ", ") +
			". Укажите адрес сервера в настройках или положите их в папку clipnip-bin рядом с exe"
		log.Printf("components: %s", componentsProblem)
	} else {
		componentsProblem = ""
	}
	return nil
}

// extractEmbedded распаковывает бинарник и проверяет SHA-256 результата.
// Пустой gz означает «компонент не вшит в эту сборку» — пропускаем.
//
// Проверка обязательна по соображениям безопасности, а не только ради
// целостности: раньше файл считался годным по одному маркеру версии, то есть
// подмена файла в пользовательской папке осталась бы незамеченной и код из
// неё исполнился бы при следующем старте. Несовпадение — это перераспаковка
// из embed, а не запуск.
func extractEmbedded(dir string, b binSpec) error {
	if b.gz == "" {
		return nil
	}
	dest := filepath.Join(dir, b.name)

	if b.sha256 != "" {
		if sum, err := fileSHA256(dest); err == nil && sum == b.sha256 {
			return nil
		} else if err == nil {
			log.Printf("integrity: %s differs on disk, re-extracting from embed", b.name)
		}
	} else if _, err := os.Stat(dest); err == nil {
		return nil // без хеша доверяем файлу как есть
	}

	gz, err := assetsDir.Open(b.gz)
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
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), zr)
	out.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if b.sha256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != b.sha256 {
			os.Remove(tmp)
			return fmt.Errorf("embedded %s hash mismatch: got %s, want %s", b.name, got, b.sha256)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	if b.version != "" {
		os.WriteFile(dest+".ver", []byte(b.version), 0o644)
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
func jsRuntimeArgs() []string {
	p := componentPath("deno.exe")
	if p == "" {
		return nil
	}
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	return []string{"--js-runtimes", "deno:" + p}
}

// ffmpegPath — путь к распакованному ffmpeg, "" если его нет.
func ffmpegPath(dir string) string {
	if p := componentPath("ffmpeg.exe"); p != "" {
		return p
	}
	p := filepath.Join(dir, "ffmpeg.exe")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
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
	ytdlp := ytdlpPath()

	args = append([]string{
		"--no-warnings",
		"--newline",
		// пользовательский %APPDATA%\yt-dlp\config может подсунуть свои
		// --output/--proxy/--cookies и сломать или перехватить поведение
		"--ignore-config",
		"--progress-template", progressTemplate,
	}, args...)
	args = append(args, jsRuntimeArgs()...)

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
	ytdlp := ytdlpPath()

	args := []string{"--no-warnings", "--ignore-config", "--dump-single-json"}
	if playlist {
		// плейлист: берём первые 500 записей, таймаут шире
		args = append(args, "--flat-playlist", "--playlist-items", "1-500")
	}
	// --no-playlist намеренно НЕ передаётся: yt-dlp сам различает ссылку на
	// одно видео и на страницу с несколькими (один элемент → одно видео).
	// Принудительный флаг молча урезал такие страницы до одного ролика.
	args = append(args, jsRuntimeArgs()...)
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
	if err := ensureBins(); err != nil {
		return "", err
	}
	ytdlp := ytdlpPath()

	args := []string{"--no-warnings", "--ignore-config", "--no-playlist", "--print", "title"}
	args = append(args, jsRuntimeArgs()...)
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
