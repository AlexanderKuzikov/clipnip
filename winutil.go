package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procMsgBox          = user32.NewProc("MessageBoxW")
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	procLoadIcon        = user32.NewProc("LoadIconW")
	procSendMessage     = user32.NewProc("SendMessageW")
	procCreateMutex     = kernel32.NewProc("CreateMutexW")
)

const errorAlreadyExists = 183

// singleInstance держит именованный mutex на всё время жизни процесса.
// Без него два запуска поднимают два пула воркеров на одну папку загрузки,
// что даёт коллизии в `.part` и перепутанные статусы джобов.
// instanceMutexName — имя mutex. CLIPNIP_INSTANCE добавляет суффикс для
// параллельного headless-прогона рядом с работающим GUI; использовать только
// с отдельными CLIPNIP_DATA и папкой загрузки, иначе две копии подерутся.
func instanceMutexName() string {
	name := `Local\ClipNip.SingleInstance`
	raw := strings.TrimSpace(os.Getenv("CLIPNIP_INSTANCE"))
	if raw == "" {
		return name
	}
	var suffix strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			suffix.WriteRune(r)
		}
	}
	if suffix.Len() == 0 {
		return name
	}
	return name + "." + suffix.String()
}

func singleInstance() (syscall.Handle, error) {
	name, err := syscall.UTF16PtrFromString(instanceMutexName())
	if err != nil {
		return 0, err
	}
	handle, _, err := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return 0, err
	}
	if errno, ok := err.(syscall.Errno); ok && errno == errorAlreadyExists {
		syscall.CloseHandle(syscall.Handle(handle))
		return 0, errAlreadyRunning
	}
	return syscall.Handle(handle), nil
}

var errAlreadyRunning = errors.New("another ClipNip instance is already running")

func openLogFile() error {
	_, logName := filepath.Split(logFilePath())
	if logName == "" {
		logName = "clipnip.log"
	}
	return exec.Command("explorer", "/select,"+filepath.Join(localAppDataDir(), logName)).Start()
}

const (
	wmSetIcon = 0x0080
	iconSmall = 0
	iconBig   = 1
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

// maxLogSize — порог ротации. Лог раньше рос бесконечно.
const maxLogSize = 10 << 20

func setupLog() (*os.File, error) {
	// На первом запуске каталога ещё нет: без MkdirAll лог молча не создаётся.
	if err := os.MkdirAll(localAppDataDir(), 0o755); err != nil {
		return nil, err
	}
	path := logFilePath()
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// readLogTail читает только хвост файла: GET /api/log раньше тянул весь лог
// в память, а он ничем не ограничен.
func readLogTail(max int64) (tail []byte, truncated bool, err error) {
	f, err := os.Open(logFilePath())
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	start := int64(0)
	if max > 0 && fi.Size() > max {
		start = fi.Size() - max
		truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false, err
	}
	return data, truncated, nil
}

func logFilePath() string {
	return filepath.Join(localAppDataDir(), "clipnip.log")
}

func fatalBox(err error) {
	log.Printf("fatal: %v", err)
	if headless() {
		// В headless нет пользователя: модальное окно нависнет навсегда.
		return
	}
	msgBox("ClipNip — startup error", fmt.Sprintf(
		"ClipNip could not start:\n\n%v\n\nSee %s\\clipnip.log for details.", err, localAppDataDir()))
}
