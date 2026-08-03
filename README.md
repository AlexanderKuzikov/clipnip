<p align="center">
  <a href="https://go.dev"><img alt="Go" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/License-MIT-blue.svg"></a>
</p>

<h1 align="center">ClipNip</h1>
<p align="center">Один офлайн-файл: скачивает видео и аудио с 1000+ сайтов через встроенный WebView</p>

---

Десктоп-загрузчик медиа на Go: один самодостаточный exe (~60 MB), UI — встроенный WebView2 (Edge Chromium), движок — yt-dlp. Вставь ссылки из YouTube, TikTok, Instagram, Twitter/X и любых других поддерживаемых сайтов — получи MP4, M4A или MP3.

Наследник [ReClip by averygan](https://github.com/averygan/reclip) — идея и дизайн UI взяты оттуда, бэкенд переписан с нуля.

- **Один файл, полностью офлайн** — yt-dlp и ffmpeg вшиты в exe; при первом запуске распаковываются локально (несколько секунд), интернет не нужен
- **4 режима** — MP4 (с выбором качества), M4A, MP3 128/192
- **Пакетная загрузка** — сколько угодно ссылок, очередь из 3 воркеров
- **Имя файла из клипа** — файлы на диске называются как оригинальный ролик
- **Папка загрузки** — выбирается в приложении (нативный диалог) или вводом пути
- **Возобновление** — докачался частично — продолжит с того же места
- **Отмена** — убивает yt-dlp и ffmpeg целиком (taskkill /T), без осиротевших процессов
- **Офлайн-UI** — шрифты встроены, никаких внешних CDN
- **«Открыть папку»** — файл уже на диске, диалог сохранения не нужен

## Быстрый старт

```bash
git clone https://github.com/AlexanderKuzikov/clipnip.git
cd clipnip

go build -ldflags="-s -w -H windowsgui" -o clipnip.exe .
.\clipnip.exe
```

Первый запуск распаковывает yt-dlp и ffmpeg в `%LOCALAPPDATA%\clipnip\bin\` (1-2 секунды). Файлы попадают в выбранную папку (по умолчанию `%USERPROFILE%\Downloads\ClipNip\`).

## Документация

- [`docs/CONTEXT.md`](docs/CONTEXT.md) — состояние проекта
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — архитектурные решения

## Статус

**v0.1.0** — рабочая версия: скачивание, прогресс, отмена, возобновление, дедуп, очередь.

## Лицензия

[MIT](LICENSE) — Copyright (c) 2026 averygan, Alexander Kuzikov
