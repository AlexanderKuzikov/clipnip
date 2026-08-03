<p align="center">
  <a href="https://go.dev"><img alt="Go" src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/License-MIT-blue.svg"></a>
</p>

<h1 align="center">ClipNip</h1>
<p align="center">Один exe: скачивает видео и аудио с 1000+ сайтов через встроенный WebView</p>

---

Десктоп-загрузчик медиа на Go: один бинарник (~8 MB), UI — встроенный WebView2 (Edge Chromium), движок — yt-dlp. Вставь ссылки из YouTube, TikTok, Instagram, Twitter/X и любых других поддерживаемых сайтов — получи MP4, M4A или MP3.

Наследник [ReClip by averygan](https://github.com/averygan/reclip) — идея и дизайн UI взяты оттуда, бэкенд переписан с нуля.

- **Один файл** — без Python, venv и зависимостей; yt-dlp и ffmpeg скачиваются автоматически при первом запуске
- **4 режима** — MP4 (с выбором качества), M4A, MP3 128/192
- **Пакетная загрузка** — сколько угодно ссылок, очередь из 3 воркеров
- **Возобновление** — докачался частично — продолжит с того же места
- **Отмена** — убивает yt-dlp и ffmpeg целиком (taskkill /T), без осиротевших процессов
- **Офлайн-UI** — шрифты встроены, приложение не зависит от внешних CDN
- **«Открыть папку»** — файл уже на диске, диалог сохранения не нужен

## Быстрый старт

```bash
git clone https://github.com/AlexanderKuzikov/clipnip.git
cd clipnip

go build -ldflags="-s -w -H windowsgui" -o clipnip.exe .
.\clipnip.exe
```

Файлы попадают в `%USERPROFILE%\Downloads\ClipNip\`. Первый запуск скачает yt-dlp (~18 MB) и ffmpeg (~100 MB) в `%LOCALAPPDATA%\clipnip\bin\`.

## Документация

- [`docs/CONTEXT.md`](docs/CONTEXT.md) — состояние проекта
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — архитектурные решения

## Статус

**v0.1.0** — рабочая версия: скачивание, прогресс, отмена, возобновление, дедуп, очередь.

## Лицензия

[MIT](LICENSE) — Copyright (c) 2026 averygan, Alexander Kuzikov
