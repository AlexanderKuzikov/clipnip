//go:build !clipnipslim

package main

import "embed"

// assetsDir — полная сборка: интерфейс плюс все три исполняемых компонента.
// Каждый компонент упакован в gzip, а его SHA-256 сверяется при распаковке
// (см. embeddedBins в ytdlp.go).
//
//go:embed web
//go:embed embedded/yt-dlp.exe.gz
//go:embed embedded/ffmpeg.exe.gz
//go:embed embedded/deno.exe.gz
var assetsDir embed.FS
