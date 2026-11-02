# ClipNip — инструкции для AI-агентов

Desktop-загрузчик медиа (Go + WebView2 + yt-dlp). Наследник ReClip by averygan.

## Commands

- build: `go build -ldflags="-s -w -H windowsgui" -o clipnip.exe .` (автономная сборка, ~112 МБ, всё внутри)
- build-remote: `go build -tags clipnipremote -ldflags="-s -w -H windowsgui" -o clipnip-remote.exe .` (**~8.1 МБ, ни одного встроенного PE**; компоненты — portable-папка / кэш / сервер)
- build-portable: `go build -tags clipnipportable -ldflags="-s -w -H windowsgui" -o clipnip-portable.exe .` (**~7.6 МБ; сетевой загрузки нет вообще** — только `clipnip-bin` рядом с exe; собирается под ложные срабатывания антивирусов, ADR 012)
- test: `go test ./...` (и `-tags clipnipremote` / `-tags clipnipportable` при правках embed)
- vet: `go vet ./...`
- headless-API: `$env:CLIPNIP_HEADLESS="1"; $env:CLIPNIP_PORT="8899"; $env:CLIPNIP_DATA="$env:TEMP\clipnip-data"; $env:CLIPNIP_INSTANCE="test"; .\clipnip.exe` → `http://127.0.0.1:8899/` (через curl — Invoke-RestMethod на loopback падает из-за прокси). `CLIPNIP_DATA` уводит конфиг/очередь/лог/кэш компонентов из реального `%LOCALAPPDATA%\clipnip`; `CLIPNIP_INSTANCE` даёт отдельный single-instance mutex, чтобы headless не конфликтовал с запущенным GUI. Без отдельной папки загрузки два экземпляра всё равно подерутся за файлы.

## Conventions

- Коммиты прямо в main, повелительное наклонение, ≤72 символа.
- UI — в `web/`, вшивается через `//go:embed web`; шрифты локальные (без внешних CDN).
- Сборка ТОЛЬКО с `-H windowsgui` — иначе чёрное консольное окно.
- Набор встроенных бинарников зависит от тега сборки: `assetsDir` живёт в `embed_embedded.go` (все три) / `embed_thin.go` (только интерфейс; теги `clipnipremote || clipnipportable`), пути к архивам — в `bins_embedded.go` / `bins_thin.go`. Встраивать всё через `//go:embed embedded` нельзя: каталог тянется целиком.
- Тег `clipnipportable` отличается от `clipnipremote` одной константой `allowRemoteFetch` (`fetchflag_off.go` / `fetchflag_on.go`). В portable ветка загрузки unreachable, поэтому линкер выкидывает `loadManifest` и `downloadComponent` — в exe не остаётся строки `manifest.json`. **Это и есть смысл тега; проверять надо строками, а не размером.**

## Structure

- `main.go` — WebView + loopback-сервер; `CLIPNIP_PORT`/`CLIPNIP_HEADLESS` — для отладки.
- `fs.go` — постоянная навигация по папкам (контракт из MuteNip): `GET /api/fs/drives` (локальные диски, без сетевых), `GET /api/fs/list?path=` (ленивый список подпапок) и `POST /api/fs/mkdir` (создание одной папки; разделители, `.`/`..`, зарезервированные Windows-имена и хвостовые точки/пробелы отвергаются). UI — левая панель, выбор папки кликом сохраняет `download_dir`, кнопка `new folder` создаёт папку сразу в выбранной.
- `noaudio.go` — режим `video_noaudio`: после скачивания проверяет наличие аудио через ffmpeg и при необходимости пересобирает файл `-map 0 -map -0:a -c copy` без перекодирования. Для video-only потока — no-op.
- `api.go` — HTTP-контракт (унаследован от ReClip/app.py):
  - `POST /api/info` — метаданные + качества (таймаут 45 c)
  - `POST /api/download` — очередь (дедуп sha1 url|mode|format_id)
  - `GET /api/status/<id>`; `POST /api/cancel/<id>`; `GET /api/open/<id>`; `GET /api/file/<id>`
  - `GET/POST /api/settings` — папка загрузки (`download_dir` или `browse`: нативный диалог SHBrowseForFolderW)
  - `POST /api/engine` — версия движка, наличие ffmpeg и deno, флаг `stale`
  - `POST /api/reach` — вердикт транспортной доступности домена
  - `GET/POST /api/cookies` — детект браузеров + настройка авторизации
  - `POST /api/cookies/test` — пробная авторизация (сухой `/api/info`)
  - `GET /api/log` — хвост лога; `POST /api/log` — открыть файл в проводнике
  - При ошибке `/api/info` отдаёт `reach` + `reach_detail` (UI показывает их важнее текста yt-dlp)
- `config.go` — конфиг в `%LOCALAPPDATA%\clipnip\config.json`; `download_dir` + cookies (`cookies_enabled`/`cookies_spec`/`cookies_file`/`cookies_ack`). `configDirOverride` — переопределение каталога конфига, используется тестами (`TestMain`), чтобы юнит-тесты не писали в реальный конфиг.
- `cookies.go` — автодетект браузеров по путям из исходников yt-dlp, профили свежим первым; `cookieArgs()` для вызовов yt-dlp; `humanizeCookieError` (два задокументированных сбоя чтения cookies → человеческий язык). Спецификация = формат самого yt-dlp: `chrome`, `edge:Profile 1`, `chromium:C:\...\Default`.
- `jobs.go` — джобы в памяти; **таксономия ошибок 5 классов** (`classifyError`): `throttle` / `engine` (extractor отработал, поток не отдали → движок устарел) / `site` (отказ сайта: бот-чек, нет авторизации) / `fatal` / `network`. `affectsParallelism` — только throttle+network режут пул и берут cooldown (30 с при 429); `retriable` — кроме engine и fatal; `errorHint` → actionable-текст в UI. Адаптивная параллельность (старт 8, потолок 10, пол 1; +1 за 15 успешных; ÷2 при throttle/network), очередь 1024 + приоритетная retryQueue; сетевой отказ → requeue с backoff 5с×N (до 2 повторов, потолок суммарно 90 с); watchdog 60 с без роста байтов → kill + error; кнопка Retry только для фатальных; чистка `.part` старше 24 ч; пропуск уже скачанных: перед стартом yt-dlp проверка `title` + расширение режима в папке загрузки → статус `skipped` (обход — флаг `force` в /api/download, «Download anyway»); имя файла — title из /api/info (фолбэк: fetchTitle, 15 c), переименование с защитой от коллизий `(1)`.
- `ytdlp.go` — subprocess yt-dlp, прогресс-парсер, stall-детект (20 с без прогресса → kill+retry), распаковка из embed, kill-tree, `probeEngine()` (версия движка, кэш, вызывается из main и `/api/engine`). Плейлисты: `--flat-playlist --playlist-items 1-500`, таймаут 90 с. `--ignore-config` во всех трёх вызовах.
- `netprobe.go` — `probeReach` (DNS + TCP:443), `probeReachAsync` (гонка с `/api/info`), `waitReach`, `envProxySet`. Вердикты: `ok` / `dns` / `tcp` / `proxy` / `unknown`.
- `persist.go` — `queue.json` (состав очереди, не стейт-машина): `persistQueue` (вызывается из `defer` в `runDownload`, из `cancelJob` и при постановке в очередь), `restoreQueue` (до старта воркеров, с валидацией записей).
- `embedded/*.gz` — gzip-архивы yt-dlp.exe, ffmpeg.exe и deno.exe, вшиты через `//go:embed`. Распаковка в `%LOCALAPPDATA%\clipnip\bin\` при первом запуске (ensureBins). **Каждый компонент сверяется по SHA-256 с эталоном из `embeddedBins`; несовпадение → перераспаковка из embed, запуск подменённого файла исключён.** Сверить хеш надо после замены `.gz`, иначе старт упадёт. ffmpeg сверху не проверяется — под него оставлен ручной путь обновления. Подробности — `SECURITY.md`.
- `jsRuntimeArgs()` — `--js-runtimes deno:<путь>`; deno нужен для yt-dlp-ejs. **Проверено:** без него JS-рантайма на машине нет вообще (node/bun/quickjs отсутствуют), и при этом yt-dlp и так находит deno рядом со своим exe — флаг держим как страховку, а не как необходимое условие. deno опционален: в `requiredComponents` его нет, страховка, а не условие работы.
- `fetch.go` — разрешение компонентов: portable → встроенная копия → кэш/сервер, **один раз за процесс** (`ensureBins`/`refreshComponents` под `componentsMu`); повторные вызовы из каждого скачивания больше не пересчитывают хеши и не ходят за манифестом. `componentPath`/`ytdlpPath` — реестр фактических путей; `loadManifest`, `downloadComponent` (только https), `fetchAndVerify` (атомарная установка по SHA-256 и размеру), `componentsReady`/`missingComponents`. **Недостающие компоненты не фатальны** — иначе окно с ошибкой просит зайти в настройки, в которые попасть нельзя.
- Режимы загрузки (`jobs.go`): `video`, `video_noaudio`, `m4a`, `mp3_128`, `mp3_192`; `validMode`/`isVideoMode` — единая проверка для API и восстановления очереди. Для выбранного качества fallback обязан сохранять видео: `<id>+bestaudio[ext=m4a]/<id>+bestaudio/<id>` (см. грабли 25). У `video_noaudio` имя файла получает суффикс ` (no audio)`.

## Обновление вшитых бинарников

**Обычно обновлять не нужно:** сборка `clipnipremote` берёт движок с сервера
(`components_url` в конфиге), и смена версии — это прогон
`/usr/local/bin/update-clipnip-bins.sh` на `sat` (исходник — `scripts/`).
Хеши пересчитываются там же, вручную ничего трогать не надо.

Текущие вшитые версии: yt-dlp 2026.10.29.232701, deno 2.9.8, ffmpeg 8.1.2.

Порядок ниже нужен только для **автономной** сборки (~112 МБ), где всё лежит
внутри exe.

1. Скачать свежие `yt-dlp.exe` (nightly, ADR 006), `ffmpeg.exe` (GitHub / gyan.dev) и `deno.exe` (релиз denoland/deno, ассет `deno-x86_64-pc-windows-msvc.zip`).
2. Запаковать в `embedded/` (имена: `yt-dlp.exe.gz`, `ffmpeg.exe.gz`, `deno.exe.gz`). `gzip` в PowerShell нет — пакуй любым gzip-инструментом (git bash, 7-Zip, python `gzip`). deno.exe уже упакован: gzip даёт 42.6 MB из 97.5 MB, экономить не на чем.
3. **Поднять константы `ytdlpVersion` и `denoVersion` в `ytdlp.go`** под новые версии.
4. **Обновить SHA-256 в `embeddedBins`** для yt-dlp и deno. Хеш берётся с уже распакованного файла в `%LOCALAPPDATA%\clipnip\bin\`. Забытый шаг — приложение не стартует: сверка целостности отвергнет расхождение. Тест `TestEmbeddedPayloadMatchesHash` ловит это до сборки.
5. Пересобрать exe. ffmpeg обновлять не обязательно (yt-dlp обновляется чаще).

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
9. **Компоненты могут приходить с сервера, а не из exe.** Прежний пункт требовал вшивать всё «потому что GitHub/gyan.dev заблокированы в РФ» — это следствие опиралось на источник, а не на подход, и было неверным: для загрузчика, тянущего из сети, «офлайн» почти ничего не значит. Своим сервером блокировка снимается, и exe теряет встроенные PE — вместе с сигнатурой дроппера, на которую реагируют эвристики антивирусов. Порядок разрешения: `clipnip-bin` рядом с exe → кэш → сервер. Подробности — ADR 011.
10. **Отставший yt-dlp ломает скачивание, а не только метаданные.** 2026.07.04 отдавал рабочий extractor и `403 Forbidden` на сам видеопоток (клиент `android_vr` отключён). Логи выглядят как «сеть виновата» — ретраи и cooldown такое не лечат. Проверять версию первым делом.
11. **403 ≠ перегрузка.** Проверено на живом X: `GET https://x.com/` из пустого профиля браузера даёт HTTP 403 при полностью рабочей сети — это отказ сайта небраузерному клиенту, а не блокировка. Раньше ClipNip классифицировал любой 403 как троттлинг, резал пул параллельности до 1 и так маскировал сломанный движок. Проверку сети (`netprobe.go`) никогда не строи на HTTP-статусе — только DNS и TCP.
12. **`unable to download video data` — НЕ доказательство устаревшего движка.** Строка идёт от HTTP-загрузчика yt-dlp независимо от того, качается видео или аудио, и означает лишь «форматы получены, передачу отказали». 403 здесь бывает и от устаревшего движка, и от репутации адреса (смена VPN, лимит на IP), и от истёкшей подписи на большом файле. **На этом уже врали:** ClipNip назвал устаревшим nightly 2026.09.27. Утверждать причину можно только сверив версию движка с `ytdlpKnownGoodSince` (ADR 007, поправка).
13. **Пользовательский `%APPDATA%\yt-dlp\config` может всё сломать** (свой `--output`, `--proxy`, `--cookies`) — поэтому `--ignore-config` во всех вызовах.
14. **Прямой доступ к YouTube и части сайтов закрыт по сети РФ**, нужен VPN. ClipNip наследует маршрут системы; если в окружении задан `HTTP(S)_PROXY`/`ALL_PROXY`, проба сети обязана молчать, а не гадать по прямому TCP.
15. **Сбой чтения cookies у yt-dlp роняет ВЕСЬ процесс.** На ошибке расшифровки DPAPI yt-dlp поднимает `DownloadError` с комментарием «force exit» — не просто «не прочитал cookies», а полный отказ. Включённые cookies способны сломать скачивание вообще всего, включая обычный YouTube. Отсюда обязательная кнопка Test и человеческий перевод сбоев (занятая база — issue 7271, DPAPI — issue 10927).
16. **Любой Chromium-форк читается через `chromium:<каталог-профиля>`** — спецификация yt-dlp принимает путь вместо имени. Не нужно перечислять Yandex.Browser и прочие: точечно указываем путь к профилю (не к `User Data` — `Local State` ищется уровнем выше).
17. **Тесты не должны писать в рантайм-каталоги.** `configDirOverride` + `TestMain` уводят конфиг в temp; без этого юнит-тест на cookies затирает `download_dir` пользователя (уже случалось).
18. **`--no-playlist` больше не передаётся в `/api/info`** — yt-dlp сам различает одно видео и страницу с несколькими. Возврат флага тихо урежет не-YouTube страницу с несколькими роликами до одного. `--flat-playlist` — только для ссылок на плейлист YouTube.
19. **Форматы: при равной высоте https-DASH предпочтительнее m3u8.** Наивный max tbr отдавал HLS (битрейт всегда выше), а у HLS `total_bytes` иногда NA — прогресс-бар терял точность.
20. **deno не сжимается.** gzip даёт 42.6 MB из 97.5 MB, поэтому вшивание стоит ~42 MB и не имеет альтернативы: node/bun/quickjs на машине отсутствуют.
21. **Канал yt-dlp — nightly, а не stable.** YouTube ломает player-клипы каждые несколько недель, фикс приезжает в master за дни, а сам README yt-dlp называет stable «склонным к внешним поломкам». Обоснование и риски — ADR 006 (там же поправка моего прежнего неверного довода).
22. **При склейке счётчики считаются по компонентам.** Видео и аудио качаются отдельными файлами, каждый с нуля: начало нового компонента определяется откатом `downloaded_bytes`. В UI — `DoneTotal + Cur*`. Наивное «взять последний тик» показывало размер аудиофайла вместо готового ролика.
23. **`msgBox` в headless висель вечно.** Модальное окно блокируется на клике, а интерактивного пользователя нет — в headless только лог (обнаружено на прогоне single-instance).
24. **Очередь на диске — состав, а не состояние.** Формат файла при обновлении надо валидировать при восстановлении (мусорные URL и режимы отбрасываются).
25. **X отдаёт аудио как HLS/mp4, а не m4a.** Старый fallback `<id>+bestaudio[ext=m4a]/bestaudio` выбирал **только звук**: первая ветка не находила m4a, вторая скачивала bestaudio. Любой fallback для выбранного качества обязан сохранять видео: `<id>+bestaudio[ext=m4a]/<id>+bestaudio/<id>`. Проверено живым постом X: 720p + AAC.
26. **`ensureBins` нельзя вызывать из каждого скачивания.** Резолв — один раз на процесс (`sync`/`componentsMu`), иначе каждый джоб хеширует ~215 МБ и ходит за манифестом; параллельные воркеры гоняются за общий реестр путей. Явный refresh — только из настроек.
27. **Манифест компонентов не грузить, если всё есть локально.** Portable-папка или встроенные копии — сеть не нужна; 10-секундный таймаут на старте из-за недоступного сервера недопустим.
28. **Локальный API должен проверять Host и Origin.** Сторонняя страница могла text/plain POST без preflight переключить сервер компонентов (RCE) или поставить загрузку; DNS rebinding даёт совпадающие Origin/Host. См. `sameOrigin` в `api.go`.
29. **Режим без звука нельзя делать только выбором формата.** У части сайтов единственный формат мультиплексный (X: `http-*` содержит аудио). Проверять ffmpeg и пересобирать `-map 0 -map -0:a -c copy`; имя — с суффиксом ` (no audio)`, иначе skip считает обычное видео уже скачанным.
30. **Тесты пишут только в temp.** `CLIPNIP_DATA`/`configDirOverride` уводят конфиг, очередь, лог и кэш компонентов; без этого прогон затирал реальную очередь (`ftp://evil/x` находился в `%LOCALAPPDATA%`).
31. **Имя новой папки — trust boundary.** Проверять разделители, `.`/`..`, хвостовые точки/пробелы, зарезервированные Windows-имена (`CON`, `NUL`, `COM1`…) и длину; создавать только `os.Mkdir`, без вложенных путей. UI-ошибку показывать в inline-заметке, не алертом.
32. **`CLIPNIP_INSTANCE` — только для тестовых прогонов.** Он снимает запрет второго экземпляра; обязательны отдельные `CLIPNIP_DATA` и папка загрузки, иначе две копии будут делить `.part` и статусы.

## Места хранения

- Бинарники (распакованные): `%LOCALAPPDATA%\clipnip\bin\` (yt-dlp.exe, ffmpeg.exe, deno.exe) — сюда же ставятся компоненты, скачанные с сервера
- Portable-режим: `<каталог exe>\clipnip-bin\` — приоритетнее кэша, сеть не нужна
- Сервер компонентов: `https://clipnip.135.106.192.125.nip.io` (`manifest.json` + бинарники), адрес в конфиге `components_url`; сейчас yt-dlp 2026.10.29.232701, ffmpeg 8.1.2, deno 2.9.8
- Конфиг: `%LOCALAPPDATA%\clipnip\config.json`
- Очередь: `%LOCALAPPDATA%\clipnip\queue.json`
- Скачанное: выбранная пользователем папка (по умолчанию `%USERPROFILE%\Downloads\ClipNip\`)
- Docker/venv отсутствуют намеренно — проект десктопный, один exe.
