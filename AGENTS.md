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
  - `GET/POST /api/settings` — папка загрузки (`download_dir` или `browse`: нативный диалог SHBrowseForFolderW)
- `config.go` — конфиг в `%LOCALAPPDATA%\clipnip\config.json`; папка загрузки хранится там.
- `jobs.go` — джобы в памяти, адаптивная параллельность (старт 8, потолок 10, пол 1; +1 за 15 успешных; ÷2 при сетевой ошибке; cooldown 30 с при 429 / 15 с при 403 блокирует все новые старты), очередь 1024 + приоритетная retryQueue; сетевой отказ → requeue с backoff 5с×N (до 2 повторов, потолок суммарно 90 с); watchdog 60 с без роста байтов → kill + error; кнопка Retry только для фатальных; чистка `.part` старше 24 ч; имя файла — title из /api/info (фолбэк: fetchTitle, 15 c), переименование с защитой от коллизий `(1)`.
- `ytdlp.go` — subprocess yt-dlp, прогресс-парсер, stall-детект (20 с без прогресса → kill+retry), распаковка из embed, kill-tree. Плейлисты: `--flat-playlist --playlist-items 1-500`, таймаут 90 с.
- `embedded/*.gz` — gzip-архивы yt-dlp.exe и ffmpeg.exe, вшиты через `//go:embed`. Распаковка в `%LOCALAPPDATA%\clipnip\bin\` при первом запуске (ensureBins). Существующие файлы на диске не перезаписываются.

## Обновление вшитых бинарников

1. Скачать свежие `yt-dlp.exe` и `ffmpeg.exe` (GitHub / gyan.dev).
2. Gzip их в `embedded/` (имена: `yt-dlp.exe.gz`, `ffmpeg.exe.gz`), например:
   `gzip -k yt-dlp.exe && mv yt-dlp.exe.gz embedded/`
3. Пересобрать exe. ffmpeg обновлять не обязательно (yt-dlp обновляется чаще).

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
7. **`--print` без модификатора WHEN подразумевает `--simulate`** — yt-dlp НЕ скачает. И `--print after_move:title` ТОЖЕ глушит прогресс-вывод (проверено) — имя брать из `/api/info` или тихим `yt-dlp --print title URL` до скачивания.
8. **UI: без КАПС** — `text-transform: none`; тексты в обычном регистре (требование пользователя).
9. **Бинарники вшиты (офлайн)**: никаких скачиваний в рантайме — GitHub/gyan.dev блокируются в РФ. Обновление — только пересборкой.

## Места хранения

- Бинарники (распакованные): `%LOCALAPPDATA%\clipnip\bin\` (yt-dlp.exe, ffmpeg.exe)
- Конфиг: `%LOCALAPPDATA%\clipnip\config.json`
- Скачанное: выбранная пользователем папка (по умолчанию `%USERPROFILE%\Downloads\ClipNip\`)
- Docker/venv отсутствуют намеренно — проект десктопный, один exe.
