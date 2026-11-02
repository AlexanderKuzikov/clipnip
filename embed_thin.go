//go:build clipnipremote || clipnipportable

package main

import "embed"

// assetsDir — тонкая сборка. Вшит только интерфейс, поэтому exe около 8 МБ
// вместо 107 и не содержит ни одного PE: сигнатура «дроппера», на которую
// реагируют эвристики антивирусов, просто не из чего построить.
// Компоненты берутся из portable-папки рядом с exe, из кэша или с сервера
// пользователя (см. fetch.go).
//
//go:embed web
var assetsDir embed.FS
