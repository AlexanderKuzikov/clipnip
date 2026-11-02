//go:build !clipnipportable

package main

// allowRemoteFetch — сетевая загрузка компонентов собрана. В portable-сборке
// константа false, из-за чего ветки загрузки становятся недостижимыми и линкер
// выкидывает loadManifest/downloadComponent целиком.
const allowRemoteFetch = true
