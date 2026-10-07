//go:build clipnipslim

package main

import "embed"

// assetsDir — облегчённая сборка: интерфейс плюс yt-dlp и ffmpeg, без deno.
// Исключение компонента уменьшает exe примерно на 42 МБ и убирает один
// встроенный PE, а вместе с ним часть сигнатуры «дроппера», на которую
// реагируют эвристики антивирусов. Собирается как clipnip-slim.exe.
//
//go:embed web
//go:embed embedded/yt-dlp.exe.gz
//go:embed embedded/ffmpeg.exe.gz
var assetsDir embed.FS
