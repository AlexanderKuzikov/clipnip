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
	loadConfig()
	cleanupStaleParts()
	startWorkers()

	if err := ensureBins(); err != nil {
		log.Fatalf("clipnip: bootstrap failed: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	if port := os.Getenv("CLIPNIP_PORT"); port != "" {
		p, _ := strconv.Atoi(port)
		if p > 0 {
			listener.Close()
			listener, err = net.Listen("tcp", "127.0.0.1:"+port)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	srv := &http.Server{Handler: newAPI()}
	go srv.Serve(listener)

	addr := listener.Addr().String()
	fmt.Println("clipnip: http://" + addr)

	if os.Getenv("CLIPNIP_HEADLESS") == "1" {
		select {}
	}

	runWebView(addr)
}

func runWebView(addr string) {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("ClipNip — Free Media Downloader")
	w.SetSize(1024, 780, webview.HintNone)
	w.Navigate("http://" + addr + "/")
	w.Run()
}
