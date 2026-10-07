package main

import (
	"io/fs"
)

// webRoot — общий доступ к вшитым файлам интерфейса. Сам assetsDir
// объявляется в embed_full.go / embed_slim.go: набор встроенных бинарников
// зависит от тега сборки.
var webRoot fs.FS

func webFiles() fs.FS {
	if webRoot == nil {
		sub, err := fs.Sub(assetsDir, "web")
		if err != nil {
			panic(err)
		}
		webRoot = sub
	}
	return webRoot
}
