package main

import (
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// Постоянная навигация по папкам — контракт взят из MuteNip: диски и ленивый
// список подпапок, без модальных диалогов. Сетевые диски отсекаются: папка
// загрузки должна быть локальной и предсказуемо доступной.

var (
	procGetLogicalDrives = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW    = kernel32.NewProc("GetDriveTypeW")
)

type fsEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func handleFSDrives(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	mask, _, _ := procGetLogicalDrives.Call()
	drives := make([]fsEntry, 0, 8)
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + `:\`
		ptr, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		typ, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(ptr)))
		switch typ {
		case 2, 3, 5, 6: // removable, fixed, cdrom, ramdisk
		default: // сетевые (4), неизвестные и отсоединённые адреса не нужны
			continue
		}
		drives = append(drives, fsEntry{Name: letter + ":", Path: root})
	}
	writeJSON(w, http.StatusOK, map[string]any{"drives": drives})
}

func handleFSList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	raw := r.URL.Query().Get("path")
	if raw == "" || !filepath.IsAbs(raw) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нужен абсолютный путь"})
		return
	}
	path := filepath.Clean(raw)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "папка не найдена"})
		return
	}

	resp := struct {
		Path  string    `json:"path"`
		Dirs  []fsEntry `json:"dirs"`
		Error string    `json:"error"`
	}{Path: path, Dirs: []fsEntry{}}

	entries, err := os.ReadDir(path)
	if err != nil {
		log.Printf("fs list %s: %v", path, err)
		resp.Error = "нет доступа"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipSystemDir(name) {
			continue
		}
		full := filepath.Join(path, name)
		isDir := e.IsDir()
		if !isDir && e.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		resp.Dirs = append(resp.Dirs, fsEntry{Name: name, Path: full})
	}
	sort.Slice(resp.Dirs, func(i, j int) bool {
		return strings.ToLower(resp.Dirs[i].Name) < strings.ToLower(resp.Dirs[j].Name)
	})
	writeJSON(w, http.StatusOK, resp)
}

func skipSystemDir(name string) bool {
	lower := strings.ToLower(name)
	return lower == "$recycle.bin" || lower == "system volume information"
}
