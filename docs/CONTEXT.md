# ClipNip — CONTEXT

> Последнее обновление: 2026-08-04

## Статус

| Компонент | Статус | Версия/Заметка |
|-----------|--------|----------------|
| Бэкенд (Go API) | работает | v0.1.0: info, download, status, cancel, open, file |
| WebView UI | работает | встроенные шрифты, бренд ClipNip |
| yt-dlp/ffmpeg bootstrap | работает | `%LOCALAPPDATA%\clipnip\bin\`, yt-dlp 2026.07.04 |
| Прогресс | работает | stdout yt-dlp, poll 1.5 c |
| Отмена | работает | taskkill /T /F, процессы не осиротевают |
| Публикация | не начата | коммит подготовлен, репо на GitHub не создан |

## Open-проблемы

| # | Приоритет | Описание |
|---|-----------|----------|
| 1 | low | ffprobe.exe качается в bootstrap, но не используется — убрать |
| 2 | low | Прогресс-строки без speed (NA) — UI показывает пусто |
| 3 | low | Джобы in-memory — состояние теряется при рестарте (осознанно); `.part` чистятся по 24 ч |
| 4 | info | `/api/info` на YouTube (блокировка РФ) — таймаут 45 с, корректная ошибка — норма |
| 5 | info | Invoke-RestMethod падает на loopback в этой среде (прокси) — тестировать через curl |

## Журнал работ

| Дата | Изменение |
|------|-----------|
| 2026-08-04 | Баг-фиксы: limitedBuffer обрезал stdout `/api/info` на 4 КБ (→ "unexpected end of JSON input") — bytes.Buffer; CREATE_NO_WINDOW дочерним процессам (чёрный флеш-экран консоли); папка → `D:\GitHub\ClipNip` |
| 2026-08-04 | Создание ClipNip: Go+WebView, контракт из reclip/app.py, bootstrap, прогресс-парсер, kill-tree, встроенные шрифты, README/LICENSE/AGENTS.md |
| 2026-08-04 | Тесты: MP4 20-30 MB, отмена без процессов, таймаут info 45 c, GUI-окно |
| 2026-08-04 | Почищен мусор в reclip (231 MB `.part` + артефакт формата); reclip остался рабочим |

## Структура проекта

```
D:\GitHub\ClipNip\
├── main.go          # WebView + loopback-сервер (127.0.0.1:0)
├── api.go           # HTTP-контракт (наследник ReClip/app.py)
├── jobs.go          # джобы, пул 3 воркера, чистка .part
├── ytdlp.go         # yt-dlp subprocess, прогресс, bootstrap, kill-tree
├── embed.go         # //go:embed web
├── web/             # index.html, favicon.svg, fonts/ (PT Serif/Mono woff2)
├── docs/            # CONTEXT.md, DECISIONS.md
└── clipnip.exe      # сборка: -s -w -H windowsgui
```
