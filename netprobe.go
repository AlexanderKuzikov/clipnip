package main

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// Проверка транспортной доступности домена. Смотрит ТОЛЬКО на DNS и TCP:
// HTTP-статус здесь неуместен — 403 от x.com это отказ сайта небраузерному
// клиенту, а не сетевой блок, и смешивать эти вещи нельзя (на этом уже
// строился бы неверный совет «включи VPN», когда VPN включён).

const (
	reachOK      = "ok"      // сеть доступна — значит дело не в сети
	reachDNS     = "dns"     // домен не резолвится
	reachTCP     = "tcp"     // соединение не устанавливается
	reachProxy   = "proxy"   // задан прокси — проба по прямой бессмысленна
	reachUnknown = "unknown" // не удалось определить
)

type reachResult struct {
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
}

// envProxySet сообщает, задан ли прокси в окружении. yt-dlp читает
// HTTP_PROXY/HTTPS_PROXY/ALL_PROXY, поэтому прямой TCP-проб в этом случае
// покажет ложное «домен недоступен» — лучше честно признать это.
func envProxySet() bool {
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY"} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

func reachDetail(v string) string {
	switch v {
	case reachOK:
		return "Сеть в порядке — домен доступен. Проблема не в сети."
	case reachDNS:
		return "Домен не резолвится. Проверь DNS или включи VPN."
	case reachTCP:
		return "Соединение не устанавливается. Домен недоступен из сети — включи VPN."
	case reachProxy:
		return "Задан прокси — сетевую проверку пропускаем."
	}
	return "Не удалось определить доступность домена."
}

// probeReach проверяет домен: сначала DNS, затем TCP-connect на 443.
func probeReach(rawURL string) reachResult {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return reachResult{reachUnknown, reachDetail(reachUnknown)}
	}
	host := u.Hostname()
	if host == "" {
		return reachResult{reachUnknown, reachDetail(reachUnknown)}
	}
	if envProxySet() {
		return reachResult{reachProxy, reachDetail(reachProxy)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
		return reachResult{reachDNS, reachDetail(reachDNS)}
	}

	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		return reachResult{reachTCP, reachDetail(reachTCP)}
	}
	conn.Close()
	return reachResult{reachOK, reachDetail(reachOK)}
}

// probeReachAsync запускает пробу в фоне. Таймаут yt-dlp на заблокированном
// домене — 45 с; проба занимает единицы секунд и успевает дать вердикт раньше,
// не блокируя вызов. Гонка специально сделана «проба вперёд, yt-dlp следом».
func probeReachAsync(rawURL string) <-chan reachResult {
	ch := make(chan reachResult, 1)
	go func() { ch <- probeReach(rawURL) }()
	return ch
}

// waitReach достаёт вердикт пробы, но не дольше wait: если сеть не
// дотянулась за 6 с, возвращаем неизвестность и не блокируем интерфейс.
func waitReach(ch <-chan reachResult, wait time.Duration) reachResult {
	select {
	case r := <-ch:
		return r
	case <-time.After(wait):
		return reachResult{reachUnknown, reachDetail(reachUnknown)}
	}
}
