//go:build clipnipportable

package main

// allowRemoteFetch — в portable-сборке логики загрузки нет вообще: ни строки
// «manifest.json», ни проверки https, ни скачивания исполняемых файлов. Из exe
// выходит программа, которая запускает yt-dlp рядом с собой и больше ни к чему
// не лезет.
const allowRemoteFetch = false
