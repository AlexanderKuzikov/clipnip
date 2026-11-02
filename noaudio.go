package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Режим «видео без звука». yt-dlp умеет выбрать video-only поток, но не на
// каждом сайте: иногда единственный доступный формат — мультиплексный.
// Поэтому результат проверяется ffmpeg и при наличии аудио пересобирается
// без перекодирования (-map 0 -map -0:a -c copy), как в MuteNip.

var audioStreamRe = regexp.MustCompile(`(?m)^\s*Stream #\d+:\d+.*: Audio:`)

func mediaHasAudio(ffmpeg, path string) (bool, error) {
	cmd := exec.Command(ffmpeg, "-hide_banner", "-nostdin", "-i", path)
	cmd.SysProcAttr = noWindow()
	var errBuf limitedBuffer
	cmd.Stdout = nil
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case runErr := <-done:
		if runErr == nil && errBuf.String() == "" {
			return false, errors.New("ffmpeg returned no stream information")
		}
	case <-time.After(30 * time.Second):
		killTree(cmd.Process.Pid)
		<-done
		return false, errors.New("ffmpeg probe timed out")
	}
	out := errBuf.String()
	if out == "" {
		return false, errors.New("ffmpeg returned no stream information")
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "invalid data") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "moov atom not found") {
		return false, errors.New("ffmpeg could not read the downloaded file")
	}
	return audioStreamRe.MatchString(out), nil
}

func ensureNoAudio(job *Job, path string) error {
	dir, err := binDir()
	if err != nil {
		return err
	}
	ffmpeg := ffmpegPath(dir)
	if ffmpeg == "" {
		return errors.New("ffmpeg is not available")
	}
	hasAudio, err := mediaHasAudio(ffmpeg, path)
	if err != nil {
		return fmt.Errorf("probe streams: %w", err)
	}
	if !hasAudio {
		return nil
	}
	return stripAudioTrack(job, ffmpeg, path)
}

func stripAudioTrack(job *Job, ffmpeg, path string) error {
	ext := filepath.Ext(path)
	tmp := strings.TrimSuffix(path, ext) + ".na.tmp" + ext
	os.Remove(tmp)
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-i", path,
		"-map", "0", "-map", "-0:a", "-c", "copy",
		"-loglevel", "error",
		tmp,
	}
	if err := runFFmpegWithJob(job, ffmpeg, args, 30*time.Minute); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// runFFmpegWithJob запускает ffmpeg и отдаёт его pid джобу: пауза и отмена
// должны убивать не только yt-dlp, но и постобработку.
func runFFmpegWithJob(job *Job, ffmpeg string, args []string, timeout time.Duration) error {
	cmd := exec.Command(ffmpeg, args...)
	cmd.SysProcAttr = noWindow()
	var errBuf limitedBuffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return err
	}

	job.pidMu.Lock()
	job.pid = cmd.Process.Pid
	job.pidMu.Unlock()
	defer func() {
		job.pidMu.Lock()
		job.pid = 0
		job.pidMu.Unlock()
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(errBuf.String())
			if msg != "" {
				return errors.New(msg)
			}
			return err
		}
		return nil
	case <-time.After(timeout):
		killTree(cmd.Process.Pid)
		<-done
		return errors.New("ffmpeg timed out")
	}
}
