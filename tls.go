package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
)

var (
	userAgentsDesktop = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	}

	userAgentsMobile = []string{
		"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36",
		"Mozilla/5.0 (Linux; Android 14; SM-S908B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/124.0.0.0 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36",
	}

	secChUaDesktop = []string{
		`"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		`"Chromium";v="123", "Google Chrome";v="123", "Not-A.Brand";v="99"`,
	}

	secChUaMobile = []string{
		`"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		`"Chromium";v="123", "Google Chrome";v="123", "Not-A.Brand";v="99"`,
	}

	secChUaPlatformsDesktop = []string{`"Windows"`, `"macOS"`, `"Linux"`}
	secChUaPlatformsMobile  = []string{`"Android"`}
)

type statsType struct {
	total   int64
	success int64
	failed  int64
	http3   int64
	http2   int64
}

var stats statsType

func buildHTTP3Client() *http.Client {
	return &http.Client{
		Transport: &http3.RoundTripper{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify:       true,
				CurvePreferences:         []tls.CurveID{tls.X25519, tls.CurveP256},
				PreferServerCipherSuites: true,
				MinVersion:               tls.VersionTLS12,
				MaxVersion:               tls.VersionTLS13,
			},
			QuicConfig: nil,
		},
		Timeout: 10 * time.Second,
	}
}

func buildHTTP2Client() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify:       true,
				CurvePreferences:         []tls.CurveID{tls.X25519, tls.CurveP256},
				PreferServerCipherSuites: true,
				MinVersion:               tls.VersionTLS12,
				MaxVersion:               tls.VersionTLS13,
				CipherSuites: []uint16{
					tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
					tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
					tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
					tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				},
			},
		},
		Timeout: 10 * time.Second,
	}
}

func setHeaders(req *http.Request, isMobile bool) {
	rng := rand.Intn

	var ua string
	var secChUa string
	var secChUaPlatform string

	if isMobile {
		ua = userAgentsMobile[rng(len(userAgentsMobile))]
		secChUa = secChUaMobile[rng(len(secChUaMobile))]
		secChUaPlatform = secChUaPlatformsMobile[rng(len(secChUaPlatformsMobile))]
	} else {
		ua = userAgentsDesktop[rng(len(userAgentsDesktop))]
		secChUa = secChUaDesktop[rng(len(secChUaDesktop))]
		secChUaPlatform = secChUaPlatformsDesktop[rng(len(secChUaPlatformsDesktop))]
	}

	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,id;q=0.8")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Sec-Ch-Ua", secChUa)
	req.Header.Set("Sec-Ch-Ua-Mobile", func() string {
		if isMobile {
			return "?1"
		}
		return "?0"
	}())
	req.Header.Set("Sec-Ch-Ua-Platform", secChUaPlatform)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Dnt", "1")
}

func makeRequest(client *http.Client, target string, isMobile bool) bool {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return false
	}

	setHeaders(req, isMobile)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return true
	}
	return false
}

func main() {
	if len(os.Args) < 5 {
		fmt.Println("Usage: go run tls.go <url> <duration_seconds> <rate_per_second> <threads>")
		fmt.Println("Example: go run tls.go https://example.com 10 100 50")
		os.Exit(1)
	}

	target := os.Args[1]
	duration, err := strconv.Atoi(os.Args[2])
	if err != nil || duration <= 0 {
		fmt.Println("Error: duration must be a positive integer (seconds)")
		os.Exit(1)
	}
	rate, err := strconv.Atoi(os.Args[3])
	if err != nil || rate <= 0 {
		fmt.Println("Error: rate must be a positive integer (req/s)")
		os.Exit(1)
	}
	threads, err := strconv.Atoi(os.Args[4])
	if err != nil || threads <= 0 {
		fmt.Println("Error: threads must be a positive integer")
		os.Exit(1)
	}

	http3Client := buildHTTP3Client()
	http2Client := buildHTTP2Client()

	sem := make(chan struct{}, threads)

	interval := time.Second / time.Duration(rate)
	ticker := time.NewTicker(interval)
	deadline := time.After(time.Duration(duration) * time.Second)
	var wg sync.WaitGroup

	fmt.Printf("Starting HTTP/3 + HTTP/2 requests to %s\n", target)
	fmt.Printf("Duration: %ds | Rate: %d req/s | Threads: %d\n\n", duration, rate, threads)

	start := time.Now()

loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-ticker.C:
			sem <- struct{}{}
			wg.Add(1)
			atomic.AddInt64(&stats.total, 1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				isMobile := rand.Intn(100) < 40
				useHTTP3 := rand.Intn(100) < 90

				var client *http.Client
				if useHTTP3 {
					client = http3Client
				} else {
					client = http2Client
				}

				if makeRequest(client, target, isMobile) {
					atomic.AddInt64(&stats.success, 1)
					if useHTTP3 {
						atomic.AddInt64(&stats.http3, 1)
					} else {
						atomic.AddInt64(&stats.http2, 1)
					}
				} else {
					atomic.AddInt64(&stats.failed, 1)
				}
			}()
		}
	}

	ticker.Stop()
	wg.Wait()
	elapsed := time.Since(start).Seconds()

	fmt.Println("========== SUMMARY ==========")
	fmt.Printf("Target      : %s\n", target)
	fmt.Printf("Duration    : %.2fs\n", elapsed)
	fmt.Printf("Total Req   : %d\n", stats.total)
	fmt.Printf("Success     : %d\n", stats.success)
	fmt.Printf("  HTTP/3    : %d\n", stats.http3)
	fmt.Printf("  HTTP/2    : %d\n", stats.http2)
	fmt.Printf("Failed      : %d\n", stats.failed)
	fmt.Printf("Avg Rate    : %.2f req/s\n", float64(stats.total)/elapsed)
	fmt.Println("=============================")
}
