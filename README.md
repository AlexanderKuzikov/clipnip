<p align="center">
  <a href="https://go.dev"><img alt="Go" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/License-MIT-blue.svg"></a>
</p>

<h1 align="center">ClipNip</h1>
<p align="center">Скачивает видео и аудио с 1000+ сайтов через встроенный WebView</p>

---

Десктоп-загрузчик медиа на Go: автономная сборка ~112 MB (всё внутри) или тонкая ~8 MB с компонентами рядом/на сервере. UI — встроенный WebView2 (Edge Chromium), движок — yt-dlp. Вставь ссылки из YouTube, TikTok, Instagram, Twitter/X и любых других поддерживаемых сайтов — получи MP4, M4A или MP3; видео можно сохранить без звуковой дорожки.

Наследник [ReClip by averygan](https://github.com/averygan/reclip) — идея и дизайн UI взяты оттуда, бэкенд переписан с нуля.

- **Три поставки** — автономная (~112 MB, всё внутри), remote (~8 MB, компоненты с сервера) и portable (~8 MB, только `clipnip-bin` рядом с exe)
- **5 режимов** — MP4 (с выбором качества), MP4 без звука, M4A, MP3 128/192
- **Видео без звука в обычном MP4** — DASH-контейнер нормализуется через ffmpeg; старые файлы чинит `scripts/repair-noaudio.ps1`
- **Cookies из браузера** — Chrome, Edge, Firefox, Chromium-форки и Yandex Browser; профили с человеческими именами, снимок cookies для работы при открытом браузере
- **Постоянная панель папок** — диски и подпапки раскрываются лениво, папка загрузки выбирается кликом, новые папки создаются прямо в панели; без модальных диалогов
- **Плейлисты** — до 500 позиций, разворачиваются в список карточек
- **Адаптивная параллельность** — сама подбирает темп: растёт при успехах, сбавляет при отказах (429/timeout), без ручных настроек
- **Авторетраи** — сбой сети/зависание → повтор с докачкой; фатальные ошибки (private/удалено) не ретраятся
- **Пакетная загрузка** — сколько угодно ссылок, очередь с дедупом и восстановлением после перезапуска
- **Имя файла из клипа** — файлы на диске называются как оригинальный ролик
- **Возобновление** — докачался частично — продолжит с того же места
- **Отмена** — убивает yt-dlp и ffmpeg целиком (taskkill /T), без осиротевших процессов
- **Офлайн-UI** — шрифты встроены, никаких внешних CDN
- **«Открыть папку»** — файл уже на диске, диалог сохранения не нужен

## Быстрый старт

```bash
git clone https://github.com/AlexanderKuzikov/clipnip.git
cd clipnip

# автономная сборка: всё внутри
go build -ldflags="-s -w -H windowsgui" -o clipnip.exe .

# тонкая сборка: компоненты из clipnip-bin рядом с exe, кэша или сервера
go build -tags clipnipremote -ldflags="-s -w -H windowsgui" -o clipnip-remote.exe .

# portable: только clipnip-bin рядом с exe, сетевой загрузки нет вообще
go build -tags clipnipportable -ldflags="-s -w -H windowsgui" -o clipnip-portable.exe .
```

Автономная сборка при первом запуске распаковывает yt-dlp и ffmpeg в `%LOCALAPPDATA%\clipnip\bin\` (1-2 секунды). Файлы попадают в выбранную папку (по умолчанию `%USERPROFILE%\Downloads\ClipNip\`).

## Документация

- [`docs/CONTEXT.md`](docs/CONTEXT.md) — состояние проекта
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — архитектурные решения

## Статус

**v1.4.2** — Yandex Browser и снимок cookies, нормализация DASH для видео без звука и скрипт ремонта, создание папок в панели, выбор формата для X/Twitter, три поставки (автономная/remote/portable), плейлисты до 500, адаптивная параллельность, авторетраи, паузы/отмены, watchdog. Сервер компонентов обновлён до yt-dlp 2026.10.29 и deno 2.9.8.

## Лицензия

[MIT](LICENSE) — Copyright (c) 2026 averygan, Alexander Kuzikov
