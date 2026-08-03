package main

import (
	"embed"
	"io/fs"
)

//go:embed web embedded
var assetsDir embed.FS

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
