package main

import (
	"embed"
	"io/fs"
)

//go:embed web
var webDir embed.FS

var webRoot fs.FS

func webFiles() fs.FS {
	if webRoot == nil {
		sub, err := fs.Sub(webDir, "web")
		if err != nil {
			panic(err)
		}
		webRoot = sub
	}
	return webRoot
}
