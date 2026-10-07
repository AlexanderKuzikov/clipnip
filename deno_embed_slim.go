//go:build clipnipslim

package main

// denoGz пуст в облегчённой сборке: deno не вшивается, ClipNip стартует и
// работает без него, а использует рантайм, если тот уже есть в системе.
//
// Сборка: go build -tags clipnipslim -ldflags="-s -w -H windowsgui" -o clipnip-slim.exe .
//
// Терять нечего: deno добавлялся как страховка для машин без JS-рантайма,
// а YouTube стабильно скачивается и без него (проверено). Выигрыш — exe
// на 42 МБ тоньше и на один встроенный PE меньше.
const denoGz = ""
