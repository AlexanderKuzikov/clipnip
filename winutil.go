package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	user32     = syscall.NewLazyDLL("user32.dll")
	procMsgBox = user32.NewProc("MessageBoxW")
)

// msgBox показывает нативное окно с сообщением (важно: GUI-процесс без консоли).
func msgBox(title, text string) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	procMsgBox.Call(0, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(titlePtr)), 0x30) // MB_ICONWARNING|MB_OK
}

// webView2Installed проверяет наличие WebView2 Runtime через реестр.
func webView2Installed() bool {
	paths := []string{
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
	}
	for _, p := range paths {
		for _, root := range []syscall.Handle{syscall.HKEY_LOCAL_MACHINE, syscall.HKEY_CURRENT_USER} {
			var k syscall.Handle
			err := syscall.RegOpenKeyEx(root, syscall.StringToUTF16Ptr(p), 0, syscall.KEY_READ, &k)
			if err != nil {
				continue
			}
			var buf [128]uint16
			var size uint32 = uint32(len(buf))
			verr := syscall.RegQueryValueEx(k, syscall.StringToUTF16Ptr("pv"), nil, nil, (*byte)(unsafe.Pointer(&buf[0])), &size)
			syscall.RegCloseKey(k)
			if verr == nil && size > 0 {
				return true
			}
		}
	}
	return false
}

func setupLog() (*os.File, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "clipnip")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "clipnip.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func fatalBox(err error) {
	msgBox("ClipNip — startup error", fmt.Sprintf(
		"ClipNip could not start:\n\n%v\n\nSee %s\\clipnip.log for details.", err, localAppDataDir()))
}
