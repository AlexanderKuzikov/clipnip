//go:build clipnipremote || clipnipportable

package main

// Тонкие сборки ничего не вшивают — пустые пути означают, что встроенных
// копий нет. clipnipremote идёт portable → кэш → сервер; clipnipportable
// использует только clipnip-bin рядом с exe.
const (
	ytdlpGz  = ""
	ffmpegGz = ""
	denoGz   = ""
)
