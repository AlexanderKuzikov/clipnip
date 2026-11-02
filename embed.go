package main

import (
	"io/fs"
	"sync"
)

// webRoot — общий доступ к вшитым файлам интерфейса. Сам assetsDir
// объявляется в embed_embedded.go / embed_thin.go: набор встроенных
// бинарников зависит от тега сборки.
var (
	webRoot     fs.FS
	webRootOnce sync.Once
)

func webFiles() fs.FS {
	webRootOnce.Do(func() {
		sub, err := fs.Sub(assetsDir, "web")
		if err != nil {
			panic(err)
		}
		webRoot = sub
	})
	return webRoot
}
