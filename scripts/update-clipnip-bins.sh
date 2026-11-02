#!/bin/bash
# Обновление компонентов ClipNip на сервере sat и пересчёт манифеста.
# Раньше смена версии yt-dlp требовала пересборки ClipNip (~107 МБ exe) и
# перераздачи его всем пользователям. Теперь exe ~8 МБ без PE внутри, а движок
# обновляется здесь: скрипт кладёт свежие бинарники и переписывает manifest.json.
# Клиент сверяет SHA-256 и отказывается ставить файл при расхождении.
#
# Запуск (с Windows-машины):
#   ssh -i C:\Users\alexa\.ssh\id_ed25519 deploy@135.106.192.125 \
#     'bash /usr/local/bin/update-clipnip-bins.sh'
#
# Опционально передать конкретную версию:
#   ... 'bash /usr/local/bin/update-clipnip-bins.sh 2026.10.05.000000'
set -uo pipefail

DEST=/var/www/clipnip
NIGHTLY_REPO=yt-dlp/yt-dlp-nightly-builds
# deno необязателен: ClipNip работает и без JS-рантайма (ADR 009).
# Поставь WITH_DENO=1, если хочешь держать его на сервере.
WITH_DENO=${WITH_DENO:-1}
KEEP_VERSIONS=2   # сколько прошлых сборок yt-dlp оставлять рядом

log() { echo "$*" | systemd-cat -t clipnip-bins -p info; echo "$*"; }
mkdir -p "$DEST"

# --- 1. версия yt-dlp: nightly по умолчанию, либо заданная пользователем ---
if [ $# -ge 1 ]; then
    YT_TAG="$1"
else
    # json целиком в переменную: curl | grep -m1 рвётся по SIGPIPE и печатает
    # «curl: (23) Failure writing output» — шум на каждый прогон
    RELEASE_JSON=$(curl -fsSL --max-time 60 \
        "https://api.github.com/repos/$NIGHTLY_REPO/releases/latest") \
        || { log "FAIL: не удалось получить список релизов"; exit 1; }
    YT_TAG=$(printf '%s' "$RELEASE_JSON" | grep -m1 '"tag_name"' | cut -d'"' -f4)
fi
[ -z "${YT_TAG:-}" ] && { log "FAIL: не удалось определить тег yt-dlp"; exit 1; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

log "yt-dlp: качаю $YT_TAG"
curl -fL --max-time 900 -o "$TMP/yt-dlp.exe" \
    "https://github.com/$NIGHTLY_REPO/releases/download/$YT_TAG/yt-dlp.exe" \
    || { log "FAIL: скачивание yt-dlp"; exit 1; }

# yt-dlp.exe не переносит версию в имя файла, поэтому она живёт в манифесте.
# Копия под версионным именем нужна ротации: иначе старые сборки нечего чистить.
install -m 664 "$TMP/yt-dlp.exe" "$DEST/yt-dlp.exe"
cp -p "$DEST/yt-dlp.exe" "$DEST/yt-dlp.exe.$YT_TAG"

# --- 2. ffmpeg: обновляем только если его нет ---
# Качается BtbN master-latest, поэтому версия в манифесте берётся из маркера,
# а не выдумывается: подпись "8.1.2" под master-сборкой вводила в заблуждение.
# Версия informational, проверяется SHA-256.
FFMPEG_VER=master
FFMPEG_VER_FILE="$DEST/ffmpeg.exe.ver"
if [ ! -f "$DEST/ffmpeg.exe" ]; then
    log "ffmpeg: файла нет, качаю essentials $FFMPEG_VER"
    curl -fL --max-time 1800 -o "$TMP/ffmpeg.zip" \
        "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip" \
        || { log "FAIL: скачивание ffmpeg"; exit 1; }
    unzip -o -j "$TMP/ffmpeg.zip" '*/bin/ffmpeg.exe' -d "$TMP" >/dev/null \
        || { log "FAIL: распаковка ffmpeg"; exit 1; }
    install -m 664 "$TMP/ffmpeg.exe" "$DEST/ffmpeg.exe"
    printf '%s' "$FFMPEG_VER" > "$FFMPEG_VER_FILE"
else
    log "ffmpeg: уже на месте, версия $(cat "$FFMPEG_VER_FILE" 2>/dev/null || echo unknown), не трогаю"
fi
FFMPEG_MANIFEST_VER=$(cat "$FFMPEG_VER_FILE" 2>/dev/null || echo "unknown")

# --- 3. deno (необязательно) ---
DENO_VER=2.9.8
DENO_VER_FILE="$DEST/deno.exe.ver"
if [ "$WITH_DENO" = "1" ]; then
    # Маркер версии: без него обновление DENO_VER никогда не доезжало —
    # проверялось только наличие файла.
    if [ ! -f "$DEST/deno.exe" ] || [ "$(cat "$DENO_VER_FILE" 2>/dev/null)" != "$DENO_VER" ]; then
        log "deno: качаю $DENO_VER"
        if curl -fL --max-time 1800 -o "$TMP/deno.zip" \
            "https://github.com/denoland/deno/releases/download/v$DENO_VER/deno-x86_64-pc-windows-msvc.zip"; then
            if unzip -o -j "$TMP/deno.zip" 'deno.exe' -d "$TMP" >/dev/null; then
                install -m 664 "$TMP/deno.exe" "$DEST/deno.exe"
                printf '%s' "$DENO_VER" > "$DENO_VER_FILE"
            else
                log "WARN: deno не распакован — ClipNip справится и без него"
            fi
        else
            log "WARN: deno не скачан — ClipNip справится и без него"
        fi
    fi
fi
# В манифест идёт фактическая версия, а не желаемая: если загрузка упала,
# старый файл не должен выдаваться за новый.
DENO_MANIFEST_VER=$(cat "$DENO_VER_FILE" 2>/dev/null || echo "unknown")

# --- 4. манифест: хеши считаются по тому, что реально лежит в каталоге ---
cd "$DEST" || exit 1
{
    printf '{\n  "version": 1,\n  "components": [\n'
    first=1
    for spec in "yt-dlp.exe:$YT_TAG" "ffmpeg.exe:$FFMPEG_MANIFEST_VER" "deno.exe:$DENO_MANIFEST_VER"; do
        name=${spec%%:*}
        ver=${spec#*:}
        [ -f "$name" ] || continue
        sum=$(sha256sum "$name" | cut -d' ' -f1)
        size=$(stat -c %s "$name")
        [ $first -eq 0 ] && printf ',\n'
        first=0
        printf '    {"name":"%s","version":"%s","file":"%s","sha256":"%s","size":%s}' \
            "$name" "$ver" "$name" "$sum" "$size"
    done
    printf '\n  ]\n}\n'
} > "$DEST/manifest.json.tmp" \
  && mv "$DEST/manifest.json.tmp" "$DEST/manifest.json" \
  || { log "FAIL: запись манифеста"; exit 1; }

chmod 664 "$DEST/manifest.json"
log "манифест перезаписан:"
cat "$DEST/manifest.json"

log "проверка раздачи:"
curl -fsS --max-time 60 "https://clipnip.135.106.192.125.nip.io/manifest.json" \
    | head -c 200 | systemd-cat -t clipnip-bins -p info \
    && log "OK: manifest.json отдаётся по https" || log "WARN: manifest.json не отдаётся — проверь Caddy"

# --- 5. ротация: старые версии не копим ---
OLD=$(ls -1t "$DEST"/yt-dlp.exe.* 2>/dev/null | tail -n +$((KEEP_VERSIONS + 1)))
for f in $OLD; do log "rotate out $(basename "$f")"; rm -f "$f"; done