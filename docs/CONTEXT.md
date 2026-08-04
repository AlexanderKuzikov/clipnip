# ClipNip — CONTEXT

> Последнее обновление: 2026-08-04

## Статус

| Компонент | Статус | Версия/Заметка |
|-----------|--------|----------------|
| Бэкенд (Go API) | работает | v0.5.0: пропуск уже скачанных файлов (skipped), force-обход |
| WebView UI | работает | счётчик «Completed N of M», кнопки сверху, нумерация, skipped-карточки |
| Пропуск скачанных | работает | детект по title+ext режима в папке загрузки; «Download anyway»; skipped входит в Completed |
| Адаптивная параллельность | работает | старт 8 → потолок 10; +1/15 успешных; ÷2 при сбое; cooldown 429=30с / 403=15с (блокирует все старты) |
| Авторетраи | работает | requeue с backoff 5с×N (до 2 повторов, потолок 90 с); watchdog 60 с; фатальные не ретраятся |
| Плейлисты | работает | до 500 позиций, таймаут info 90 с |
| Монобинарник | работает | exe ~60 MB, офлайн |
| Публикация | опубликовано | https://github.com/AlexanderKuzikov/clipnip |

## Open-проблемы

| # | Приоритет | Описание |
|---|-----------|----------|
| 1 | med | Плейлист не проходит /api/info с HTTP 403 (лог 2026-08-04 19:02, `PLTmSZlNaUBAA`) — проверить, воспроизводится ли; возможно бан IP после массовой сессии |
| 2 | low | Скорость скачивания не всегда отображается (yt-dlp отдаёт NA) |
| 3 | low | Джобы in-memory — состояние теряется при рестарте; `.part` чистятся по 24 ч |
| 4 | low | Лог-шум: финальный фейл пишется дважды (jobs.go requeue-лог + error-лог); cooldown логирует по строке на каждый заблокированный старт; «retry timeout: spent 13m» включает ожидание слота в retryQueue, а не только retry_wait |
| 5 | info | YouTube блокирован в сети РФ — /api/info таймаут 45 с; качать через доступные сайты |
| 6 | info | Invoke-RestMethod падает на loopback в этой среде (прокси) — тестировать через curl |

## Журнал работ

| Дата | Изменение |
|------|-----------|
| 2026-08-04 | v0.5.0: пропуск уже скачанных файлов. Перед стартом yt-dlp проверка `sanitizeFilename(title)` + расширение режима в папке загрузки → статус `skipped` (без сети); «Download anyway» (флаг force в /api/download, одноразовый); skipped входит в Completed + отдельная пометка в счётчике; `preferredExts` вынесена из findFinalFile. ADR 005. Проверено: go vet/test, headless-прогон skip→force→downloading |
| 2026-08-04 | v0.4.2: код-ревью по пяти осям. Исправлено: limitedBuffer держал голову вместо хвоста (реальные ошибки yt-dlp терялись); data race на lastProgress/stalled → atomics; cooldown 429/403 обходился при active>0 → теперь блокирует все старты; XSS: thumbnail в `<img src>` без валидации → safeUrl (только http/https), format_id в onclick → whitelist-regex; regex в var (sanitizeFilename, errStrip). ADR 004: модель управления загрузками |
| 2026-08-04 | v0.4.1: система управления — фикс deadlock в Pause All/Resume All, Pause All паузит и ожидающие; приоритетная retry-очередь; claim(); retry_wait с отсчётом; 403 ретраится; onProgress не перетирает paused |
| 2026-08-04 | v0.4.0: пауза/продолжение per-item (kill + докачка через .part) и общая (pauseall/resumeall, acquire ждёт); отмена удаляет недокачанное (cancelall); старт параллельности 8 (потолок 10); фикс parseSpeed; маркированные ошибки «killed: paused»; UI: кнопки Pause/Resume/Cancel, Pause All/Resume All/Cancel All, общий прогресс-бар, скорость в карточке |
| 2026-08-04 | v0.3.1: жёсткий лимит параллельности (старт 3, потолок 6 — YouTube режет при большом числе сессий); сетевой отказ больше не убивает джоб — requeue с backoff 10с×N (до 5 повторов), кнопка Retry только для фатальных; UI «Waiting in queue…»; сброс Retries при ручном повторе |
| 2026-08-04 | v0.3.0: адаптивная параллельность (семафор-пермиты), авторетраи + классификация ошибок (fatal/429/network), stall-детект 20 с, очередь 1024 + неблокирующий enqueue, плейлисты до 500, socket-timeout 15, UI-счётчик «Completed N of M». Проверено: 30 джобов на живом соединении (0 отказов, рост 5→8), юнит-тесты адаптации/классификации |
| 2026-08-04 | Иконка: стрелка загрузки в фирменном оранжевом, вшита в exe + окно/панель задач |
| 2026-08-04 | v0.2.2: поддержка плейлистов (лимит 100), окно ссылок ×4, фикс JS const, YouTube thumbs из id |
| 2026-08-04 | v0.2.1: переносимость — WebView2 check, файловый лог, логирование ошибок, self-heal, recover |
| 2026-08-04 | v0.2.0: офлайн-монобинарник (yt-dlp/ffmpeg gzip в exe), фикс прогресса, Apply убрана |
| 2026-08-04 | v0.1.1: папка загрузки, имя файла из клипа, футер убран, «Free Youtube Downloader», капсы |
| 2026-08-04 | Баг-фиксы: limitedBuffer 4 КБ; CREATE_NO_WINDOW; папка → `D:\GitHub\ClipNip` |
| 2026-08-04 | Создание ClipNip: Go+WebView, контракт из reclip/app.py, kill-tree, шрифты вшиты |
| 2026-08-04 | Почищен мусор в reclip (231 MB `.part`); reclip остался рабочим |

## Структура проекта

```
D:\GitHub\ClipNip\
├── main.go          # WebView + loopback-сервер (127.0.0.1:0)
├── api.go           # HTTP-контракт (наследник ReClip/app.py)
├── jobs.go          # джобы, адаптивная параллельность, ретраи, очередь
├── ytdlp.go         # yt-dlp subprocess, прогресс, stall-детект, kill-tree
├── config.go        # конфиг (%LOCALAPPDATA%\clipnip\config.json)
├── winutil.go       # WebView2 check, MessageBox, иконка окна, лог
├── embed.go         # //go:embed web embedded
├── web/             # index.html, favicon.svg, fonts/
├── icon/            # clipnip.svg, clipnip.ico
├── rsrc.syso        # ресурсы exe (иконка)
├── docs/            # CONTEXT.md, DECISIONS.md
└── clipnip.exe      # сборка: -s -w -H windowsgui
```
