# ClipNip — инструкции для AI-агентов

Desktop-загрузчик медиа (Go + WebView2 + yt-dlp). Наследник ReClip by averygan.

## Commands

- build: `go build -ldflags="-s -w -H windowsgui" -o clipnip.exe .`
- test: `go test ./...`
- vet: `go vet ./...`
- headless-API: `$env:CLIPNIP_HEADLESS="1"; $env:CLIPNIP_PORT="8899"; .\clipnip.exe` → `http://127.0.0.1:8899/` (через curl — Invoke-RestMethod на loopback падает из-за прокси)

## Conventions

- Коммиты прямо в main, повелительное наклонение, ≤72 символа.
- UI — в `web/`, вшивается через `//go:embed web`; шрифты локальные (без внешних CDN).
- Сборка ТОЛЬКО с `-H windowsgui` — иначе чёрное консольное окно.

## Structure

- `main.go` — WebView + loopback-сервер; `CLIPNIP_PORT`/`CLIPNIP_HEADLESS` — для отладки.
- `api.go` — HTTP-контракт (унаследован от ReClip/app.py):
  - `POST /api/info` — метаданные + качества (таймаут 45 c)
  - `POST /api/download` — очередь (дедуп sha1 url|mode|format_id)
  - `GET /api/status/<id>`; `POST /api/cancel/<id>`; `GET /api/open/<id>`; `GET /api/file/<id>`
- `jobs.go` — джобы в памяти, пул 3 воркера, чистка `.part` старше 24 ч при старте.
- `ytdlp.go` — subprocess yt-dlp, прогресс-парсер, bootstrap, kill-tree.

## Do NOT touch

- `%LOCALAPPDATA%\clipnip\` и `%USERPROFILE%\Downloads\ClipNip\` — рантайм-данные.
- Документация в `docs/` — только по правилам системы документации.

## Documentation rules

- После работы — обнови `docs/CONTEXT.md`.
- Архитектурное решение — в `docs/DECISIONS.md`.
- Переиспользуемые грабли (Go+WebView, yt-dlp) — в `D:\GitHub\knowledge\go-webview-desktop.md`.

## Грабли (проверено на этой машине)

1. **Прогресс yt-dlp идёт в stdout** (не stderr!), строки `0.0%|speed|eta|bytes|bytes` без префикса. `--progress-template "download:..."` — `download:` это тип, а не префикс вывода.
2. **exec.CommandContext НЕ убивает процессы на этой системе** (Go 1.26/Windows — зависает навсегда). Убийство — только `taskkill /PID <pid> /T /F` (killTree в ytdlp.go). Это же касается таймаута `/api/info` — таймер + killTree.
3. **Google Fonts из head убран** — блокировал первый рендер (чёрный экран). Шрифты PT Serif/Mono лежат в `web/fonts/`, отдаются с `/fonts/`.
4. **WebView — Navigate на loopback http** (`127.0.0.1:0`), без SetHtml и без биндингов Go↔JS: весь UI ходит по fetch на тот же сервер.
5. **CREATE_NO_WINDOW** (0x08000000, SysProcAttr) — обязателен для дочерних console-процессов (yt-dlp, taskkill) из GUI-приложения: иначе Windows показывает чёрное окно консоли.
6. **stdout /api/info не обрезать**: полный JSON YouTube >4 КБ — буфер без лимита.

## Места хранения

- Бинарники: `%LOCALAPPDATA%\clipnip\bin\` (yt-dlp.exe, ffmpeg.exe, ffprobe.exe)
- Скачанное: `%USERPROFILE%\Downloads\ClipNip\`
- Docker/venv отсутствуют намеренно — проект десктопный, один exe.
