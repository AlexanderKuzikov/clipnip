package main

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
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

// Имена, которые Windows не даёт создать даже с расширением: CON, NUL,
// COM1..COM9 и т.д. Проверяется база до расширения.
var reservedFolderNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func validFolderName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		// скрытые папки дерево всё равно не показывает
		return false
	}
	if unsafeNameRe.MatchString(name) {
		return false
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	if utf8.RuneCountInString(name) > 120 {
		return false
	}
	base := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name))))
	return !reservedFolderNames[base]
}

// handleFSMkdir создаёт одну папку внутри выбранной. Никаких вложенных
// путей через имя: разделители запрещены, так что filepath.Join безопасен.
func handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var req struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}
	parent := strings.TrimSpace(req.Parent)
	name := strings.TrimSpace(req.Name)
	if !filepath.IsAbs(parent) || !validFolderName(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid folder name"})
		return
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Parent folder not found"})
		return
	}
	dest := filepath.Join(parent, name)
	if _, err := os.Stat(dest); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "A folder with this name already exists"})
		return
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		log.Printf("fs mkdir %s: %v", dest, err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Could not create folder: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": dest})
}
