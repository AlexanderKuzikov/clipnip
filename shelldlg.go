package main

import (
	"syscall"
	"unsafe"
)

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")
	procBrowseFolder = shell32.NewProc("SHBrowseForFolderW")
	procGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	ole32 = syscall.NewLazyDLL("ole32.dll")
	procCoTaskMemFree = ole32.NewProc("CoTaskMemFree")
)

const (
	bifReturnOnlyFSDirs  = 0x0001
	bifNewDialogStyle    = 0x0040
)

type browseInfo struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

// browseFolder показывает нативный диалог выбора папки.
// Возвращает пустую строку, если пользователь отменил.
func browseFolder(title string) (string, error) {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", err
	}
	var displayBuf [260]uint16
	bi := browseInfo{
		pszDisplayName: &displayBuf[0],
		lpszTitle:      titlePtr,
		ulFlags:        bifReturnOnlyFSDirs | bifNewDialogStyle,
	}
	pidl, _, _ := procBrowseFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", nil // cancelled
	}
	defer procCoTaskMemFree.Call(pidl)

	var pathBuf [260]uint16
	r, _, _ := procGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&pathBuf)))
	if r == 0 {
		return "", nil
	}
	return syscall.UTF16ToString(pathBuf[:]), nil
}
