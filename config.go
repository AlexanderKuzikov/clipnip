package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	DownloadDir string `json:"download_dir"`
}

var config = struct {
	sync.RWMutex
	Config
}{}

func configPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "clipnip")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func loadConfig() {
	path, err := configPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var c Config
	if json.Unmarshal(data, &c) != nil {
		return
	}
	config.Lock()
	config.Config = c
	config.Unlock()
}

func saveConfig() {
	path, err := configPath()
	if err != nil {
		return
	}
	config.RLock()
	data, err := json.MarshalIndent(config.Config, "", "  ")
	config.RUnlock()
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0o600)
}

func getDownloadDir() string {
	config.RLock()
	dir := config.DownloadDir
	config.RUnlock()
	return dir
}

func setDownloadDir(dir string) {
	config.Lock()
	config.DownloadDir = dir
	config.Unlock()
	saveConfig()
}
