# HANDOFF — ClipNip

> Создан: 2026-08-04 ~17:30
> Причина: переполнение контекстного окна (длинная сессия: создание проекта с нуля + 10+ итераций фич/фиксов)

## Текущая задача

Проект в стабильном состоянии (v0.4.2, всё в main, запушено). Ждём фидбек пользователя: как ведёт себя большой плейлист (200+ песен) на сборке v0.4.2 — перестали ли «нескачиваемые» файлы тормозить очередь.

## Что сделано в этой сессии

- Проект создан с нуля: форк-наследник ReClip by averygan → ClipNip (Go + WebView2 + yt-dlp), репо https://github.com/AlexanderKuzikov/clipnip
- Офлайн-монобинарник ~60 MB: yt-dlp.exe/ffmpeg.exe вшиты gzip в embed, распаковка в `%LOCALAPPDATA%\clipnip\bin\` (существующие файлы НЕ перезаписываются — ручное обновление yt-dlp возможно)
- UI: плейлисты до 500 (flat-playlist), счётчик «Completed N of M (%)» + active/retrying/paused, нумерация карточек, кнопки управления СВЕРХУ (Download All / Pause All / Resume All / Cancel All), скорость и ETA в карточке, «Retrying in Ns…», «Waiting in queue…»
- Система управления: paused (kill + .part, докачка), retry_wait (backoff 5с×N, до 2 повторов, потолок суммарно 90 с), приоритетная retryQueue, claim() от двойного запуска, watchdog 60 с без роста байтов → error «Download stuck»
- Адаптивная параллельность: старт 8, потолок 10, пол 1; +1 за 15 успешных; ÷2 при сетевом отказе; cooldown 30 с (429) / 15 с (403) блокирует все новые старты
- Классификация ошибок: fatal (private/removed/404/регион/Video unavailable) не ретраится; 403/429/5xx/timeout/stalled — ретраится; format-fallback при «Requested format is not available»
- Код-ревью по пяти осям + фиксы: limitedBuffer держит хвост ошибок, atomics вместо гонок, cooldown без обхода, XSS (safeUrl для thumbnail, whitelist для format_id)
- Иконка (стрелка загрузки, оранжевая) вшита в exe + WM_SETICON; кириллица в именах файлов проверена
- Папка загрузки пользователя: `D:\` (config.json)
- Документация: AGENTS.md, docs/CONTEXT.md (v0.4.2), docs/DECISIONS.md (ADR 001-004), README (v0.4.2)

## Что осталось сделать

- [ ] Получить фидбек по плейлисту на v0.4.2; если очередь всё ещё стоит — анализировать `%LOCALAPPDATA%\clipnip\clipnip.log` (watchdog/requeue/parallel-строки)
- [ ] Open-проблемы из CONTEXT.md: speed=NA от yt-dlp (косметика), jobs in-memory (теряются при рестарте — осознанно), YouTube заблокирован в РФ
- [ ] Если пользователь захочет: обновление вшитого yt-dlp (пересобрать embed/*.gz) или UI-мелочи

## Ключевые файлы

- `jobs.go` — машина состояний джоба, очереди (jobQueue/retryQueue), адаптивность, watchdog, pause/resume/cancel*
- `ytdlp.go` — subprocess yt-dlp, прогресс-парсер (stdout, 5 полей через `|`), stall-детект 20 с, ensureBins, killTree (taskkill /T /F)
- `api.go` — HTTP-контракт (/api/info|download|status|pause|resume|cancel|*all|open|file|settings)
- `web/index.html` — весь UI (vanilla JS), карточки, счётчик
- `winutil.go` — WebView2 check (реестр), MessageBox, иконка окна, лог
- `main.go` — loopback 127.0.0.1:0, CLIPNIP_HEADLESS/CLIPNIP_PORT для тестов

## Контекст (важно помнить)

- **exec.CommandContext НЕ убивает процессы на этой машине** — только `taskkill /PID n /T /F` (killTree). Не использовать CommandContext для убийства.
- **Invoke-RestMethod падает на loopback** (прокси среды) — API тестировать только `curl.exe`.
- Тестовые скачивания — ТОЛЬКО в `%TEMP%` (через /api/settings), НЕ в `D:\` (папка пользователя с его файлами).
- YouTube в РФ заблокирован: /api/info таймаут 45 с; «Video unavailable» = fatal (осознанно, ради живости очереди).
- Пользователь: русский, «ты», архитектор, НЕ любит TODO-спам и похвалу, хочет конкретику; коммиты в main без веток; commit+push только по явной просьбе.
- Defender однажды ругался на pwsh-команды с Get-Process|Stop-Process (ClickFix-эвристика) — убивать процессы через `taskkill /IM ... /F`.

## Команды для проверки

```powershell
go vet ./...; go test ./...
go build -ldflags="-s -w -H windowsgui" -o clipnip.exe .
# headless API-тест:
$env:CLIPNIP_HEADLESS="1"; $env:CLIPNIP_PORT="8899"; .\clipnip.exe
curl.exe -s -X POST http://127.0.0.1:8899/api/download -H "Content-Type: application/json" -d '{"url":"...","mode":"video","title":""}'
```

## Следующий шаг

1. Прочитать AGENTS.md и docs/CONTEXT.md.
2. Спросить пользователя, как ведёт себя плейлист на v0.4.2.
3. Если жалобы на очередь — читать `%LOCALAPPDATA%\clipnip\clipnip.log`, смотреть строки `watchdog:`/`requeue`/`parallel:`.
4. Удалить этот HANDOFF.md и закоммитить удаление.
