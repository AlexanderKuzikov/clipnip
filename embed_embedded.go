//go:build !clipnipremote

package main

import "embed"

// assetsDir — автономная сборка (по умолчанию). Интерфейс плюс все три
// компонента, упакованные в gzip. Приложение работает без сети вообще:
// компоненты распаковываются в %LOCALAPPDATA%\clipnip\bin и сверяются по
// SHA-256 (см. embeddedBins в ytdlp.go).
//
//go:embed web
//go:embed embedded/yt-dlp.exe.gz
//go:embed embedded/ffmpeg.exe.gz
//go:embed embedded/deno.exe.gz
var assetsDir embed.FS
