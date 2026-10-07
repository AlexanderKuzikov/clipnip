//go:build clipnipremote

package main

// Сборка с компонентами на сервере ничего не вшивает — пустые пути означают,
// что встроенных копий нет и resolveComponents идёт по порядку:
// portable-папка → кэш → сервер.
const (
	ytdlpGz  = ""
	ffmpegGz = ""
	denoGz   = ""
)
