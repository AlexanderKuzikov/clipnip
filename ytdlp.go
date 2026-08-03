package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	ytdlpURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
	ffmpegURL = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
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

func ensureBins() error {
	dir, err := binDir()
	if err != nil {
		return err
	}

	ytdlp := filepath.Join(dir, "yt-dlp.exe")
	if _, err := os.Stat(ytdlp); err != nil {
		if err := downloadFile(ytdlp, ytdlpURL); err != nil {
			return fmt.Errorf("yt-dlp download: %w", err)
		}
	}

	ffmpeg := filepath.Join(dir, "ffmpeg.exe")
	if _, err := os.Stat(ffmpeg); err != nil {
		if err := downloadFFmpeg(dir); err != nil {
			return fmt.Errorf("ffmpeg download: %w", err)
		}
	}

	return nil
}

func downloadFile(dest, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %s", resp.Status)
	}

	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, resp.Body)
	out.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func downloadFFmpeg(dir string) error {
	tmpZip := filepath.Join(dir, "ffmpeg.zip")
	if err := downloadFile(tmpZip, ffmpegURL); err != nil {
		return err
	}
	defer os.Remove(tmpZip)

	zr, err := zip.OpenReader(tmpZip)
	if err != nil {
		return err
	}
	defer zr.Close()

	want := map[string]bool{"ffmpeg.exe": true, "ffprobe.exe": true}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if !want[name] {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(dst, rc)
		rc.Close()
		dst.Close()
		if err != nil {
			return err
		}
		delete(want, name)
	}
	if len(want) > 0 {
		return errors.New("ffmpeg.zip: required binaries not found")
	}
	return nil
}

var progressTemplate = "download:%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s"

func runYtDlp(job *Job, args []string, onProgress func(progressState)) error {
	dir, err := binDir()
	if err != nil {
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
			onProgress(st)
			continue
		}
		errBuf.Write([]byte(line + "\n"))
	}

	err = cmd.Wait()
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

func infoJSON(url string) (map[string]any, error) {
	dir, err := binDir()
	if err != nil {
		return nil, err
	}
	ytdlp := filepath.Join(dir, "yt-dlp.exe")

	cmd := exec.Command(ytdlp, "--no-warnings", "--no-playlist", "--dump-single-json", url)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	cmd.SysProcAttr = noWindow()

	var outBuf bytes.Buffer
	var errBuf limitedBuffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return nil, err
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
	case <-time.After(45 * time.Second):
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
