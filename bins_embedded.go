//go:build !clipnipremote

package main

// Источники встроенных компонентов для автономной сборки. Все три файла
// лежат в embed; сверка SHA-256 — в embeddedBins (ytdlp.go).
const (
	ytdlpGz  = "embedded/yt-dlp.exe.gz"
	ffmpegGz = "embedded/ffmpeg.exe.gz"
	denoGz   = "embedded/deno.exe.gz"
)
