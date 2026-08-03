package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/webview/webview_go"
)

func main() {
	logFile, err := setupLog()
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}
	log.Printf("=== clipnip start ===")

	loadConfig()
	cleanupStaleParts()
	startWorkers()

	if err := ensureBins(); err != nil {
		log.Printf("bootstrap failed: %v", err)
		fatalBox(err)
		return
	}

	if !headless() && !webView2Installed() {
		log.Printf("WebView2 Runtime not found")
		msgBox("ClipNip",
			"Microsoft Edge WebView2 Runtime is required but not installed.\n\n"+
				"Install it (free, one-time) and restart ClipNip:\n"+
				"https://developer.microsoft.com/microsoft-edge/webview2/\n\n"+
				"On Windows 10/11 the runtime is usually already present — update Windows if not.")
		return
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("listen failed: %v", err)
		fatalBox(err)
		return
	}
	if port := os.Getenv("CLIPNIP_PORT"); port != "" {
		p, _ := strconv.Atoi(port)
		if p > 0 {
			listener.Close()
			listener, err = net.Listen("tcp", "127.0.0.1:"+port)
			if err != nil {
				log.Printf("listen %s failed: %v", port, err)
				fatalBox(err)
				return
			}
		}
	}

	srv := &http.Server{Handler: newAPI()}
	go srv.Serve(listener)

	addr := listener.Addr().String()
	log.Printf("http://%s", addr)
	fmt.Println("clipnip: http://" + addr)

	if headless() {
		select {}
	}

	runWebView(addr)
}

func headless() bool {
	return os.Getenv("CLIPNIP_HEADLESS") == "1"
}

func runWebView(addr string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("webview panic: %v", r)
			fatalBox(fmt.Errorf("webview error: %v", r))
		}
	}()
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("ClipNip — Free Youtube Downloader")
	w.SetSize(1024, 780, webview.HintNone)
	setWindowIcon(w.Window())
	w.Navigate("http://" + addr + "/")
	w.Run()
}
