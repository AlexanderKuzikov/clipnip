# ClipNip — CONTEXT

> Последнее обновление: 2026-08-04

## Статус

| Компонент | Статус | Версия/Заметка |
|-----------|--------|----------------|
| Бэкенд (Go API) | работает | v0.2.1: логирование, самовосстановление |
| WebView UI | работает | встроенные шрифты, без капсов, без прокрутки |
| Монобинарник | работает | exe ~60 MB: yt-dlp + ffmpeg вшиты (gzip), без сети |
| Переносимость | работает | проверка WebView2 Runtime + понятное сообщение; файловый лог; self-heal бинарников |
| Папка загрузки | работает | config.json + Browse, автоприменение |
| Имя файла | работает | title из /api/info → переименование на диске |
| Прогресс | работает | Connecting… + индитерминант при 0%, байты/ETA, elapsed |
| Отмена | работает | taskkill /T /F |
| Публикация | опубликовано | https://github.com/AlexanderKuzikov/clipnip |

## Open-проблемы

| # | Приоритет | Описание |
|---|-----------|----------|
| 1 | low | Скорость скачивания не всегда отображается (yt-dlp отдаёт NA) |
| 2 | low | Джобы in-memory — состояние теряется при рестарте; `.part` чистятся по 24 ч |
| 3 | info | YouTube блокирован в сети РФ — /api/info таймаут 45 с; качать через доступные сайты |
| 4 | info | Invoke-RestMethod падает на loopback в этой среде (прокси) — тестировать через curl |

## Журнал работ

| Дата | Изменение |
|------|-----------|
| 2026-08-04 | v0.2.2: поддержка плейлистов (URL `/playlist?` → entries → карточки по видео, лимит 100, таймаут 90 c); окно ссылок ×4 выше (220px); тесты плейлистов |
| 2026-08-04 | v0.2.1: переносимость — WebView2 check, файловый лог, логирование ошибок, self-heal, recover; UI: «Connecting…», отступы, Save to ниже |
| 2026-08-04 | v0.2.0: офлайн-монобинарник (yt-dlp/ffmpeg gzip в exe), фикс прогресса (`--print` глушил вывод), кнопка Apply убрана |
| 2026-08-04 | v0.1.1: выбор папки загрузки, имя файла из клипа, футер убран, «Free Youtube Downloader», капсы убраны |
| 2026-08-04 | Баг-фиксы: limitedBuffer 4 КБ; CREATE_NO_WINDOW; папка → `D:\GitHub\ClipNip` |
| 2026-08-04 | Создание ClipNip: Go+WebView, контракт из reclip/app.py, прогресс-парсер, kill-tree, шрифты вшиты |
| 2026-08-04 | Почищен мусор в reclip (231 MB `.part`); reclip остался рабочим |

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
