package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	user32     = syscall.NewLazyDLL("user32.dll")
	procMsgBox = user32.NewProc("MessageBoxW")
	kernel32   = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	procLoadIcon = user32.NewProc("LoadIconW")
	procSendMessage = user32.NewProc("SendMessageW")
)

const (
	wmSetIcon   = 0x0080
	iconSmall   = 0
	iconBig     = 1
)

// setWindowIcon ставит иконку приложения (ресурс #1 из exe) в заголовок окна
// и панель задач — WebView2 сам её не наследует.
func setWindowIcon(hwnd unsafe.Pointer) {
	hInst, _, _ := procGetModuleHandle.Call(0)
	icon, _, _ := procLoadIcon.Call(hInst, 1)
	if icon == 0 {
		log.Printf("window icon: resource #1 not found")
		return
	}
	procSendMessage.Call(uintptr(hwnd), wmSetIcon, iconBig, icon)
	procSendMessage.Call(uintptr(hwnd), wmSetIcon, iconSmall, icon)
	log.Printf("window icon: set")
}

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
