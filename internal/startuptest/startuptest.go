package startuptest

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ssh-socks5/internal/config"
	"ssh-socks5/internal/logger"
	"ssh-socks5/internal/tunnel"
)

const curlWriteOut = "%{http_code}|%{size_download}|%{speed_download}|%{time_total}"

func Run(ctx context.Context, cfg *config.Config, tm *tunnel.Manager, log *logger.Logger) {
	testCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	log.Infof("startup test: connecting tunnel...")

	if err := tm.Acquire(testCtx); err != nil {
		log.Infof("startup test: FAILED (tunnel: %v)", err)
		return
	}
	defer tm.Release()

	proxyURL, err := cfg.SOCKS5ProxyURL()
	if err != nil {
		log.Infof("startup test: FAILED (proxy url: %v)", err)
		return
	}

	if _, err := exec.LookPath("curl"); err != nil {
		log.Infof("startup test: FAILED (curl not found in PATH)")
		return
	}

	cmd := exec.CommandContext(testCtx, "curl",
		"-s",
		"-o", "/dev/null",
		"-w", curlWriteOut,
		"--connect-timeout", "30",
		"--proxy", proxyURL,
		"--",
		cfg.StartupTest.URL,
	)

	start := time.Now()
	output, err := cmd.Output()
	elapsed := time.Since(start)

	metrics, parseErr := parseCurlMetrics(strings.TrimSpace(string(output)))
	if parseErr != nil {
		if err != nil {
			log.Infof("startup test: FAILED (%v, elapsed %.2f s)", err, elapsed.Seconds())
		} else {
			log.Infof("startup test: FAILED (parse metrics: %v, raw: %q)", parseErr, string(output))
		}
		return
	}

	result := formatResult(metrics, cfg.StartupTest.URL)
	if err != nil {
		log.Infof("startup test: FAILED (%v, %s)", err, result)
		return
	}

	if metrics.httpCode < 200 || metrics.httpCode >= 300 {
		log.Infof("startup test: FAILED (%s)", result)
		return
	}

	log.Infof("startup test: OK (%s)", result)
}

type curlMetrics struct {
	httpCode      int
	sizeDownload  float64
	speedDownload float64
	timeTotal     float64
}

func parseCurlMetrics(raw string) (curlMetrics, error) {
	parts := strings.Split(raw, "|")
	if len(parts) != 4 {
		return curlMetrics{}, fmt.Errorf("expected 4 fields, got %d", len(parts))
	}

	httpCode, err := strconv.Atoi(parts[0])
	if err != nil {
		return curlMetrics{}, fmt.Errorf("http_code: %w", err)
	}

	sizeDownload, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return curlMetrics{}, fmt.Errorf("size_download: %w", err)
	}

	speedDownload, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return curlMetrics{}, fmt.Errorf("speed_download: %w", err)
	}

	timeTotal, err := strconv.ParseFloat(parts[3], 64)
	if err != nil {
		return curlMetrics{}, fmt.Errorf("time_total: %w", err)
	}

	return curlMetrics{
		httpCode:      httpCode,
		sizeDownload:  sizeDownload,
		speedDownload: speedDownload,
		timeTotal:     timeTotal,
	}, nil
}

func formatResult(m curlMetrics, url string) string {
	return fmt.Sprintf(
		"HTTP %d, %s transferred, %s/s, %.2f s, %s",
		m.httpCode,
		formatBytes(m.sizeDownload),
		formatBytes(m.speedDownload),
		m.timeTotal,
		url,
	)
}

func formatBytes(n float64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%.0f B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", n/1024)
	default:
		return fmt.Sprintf("%.1f MB", n/1024/1024)
	}
}
