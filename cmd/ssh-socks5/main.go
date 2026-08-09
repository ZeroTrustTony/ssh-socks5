package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ssh-socks5/internal/config"
	"ssh-socks5/internal/logger"
	"ssh-socks5/internal/socks5"
	"ssh-socks5/internal/startuptest"
	"ssh-socks5/internal/tunnel"
)

func main() {
	configPath := flag.String("config", "/etc/ssh-socks5/config.yaml", "path to configuration file")
	healthCheck := flag.Bool("health-check", false, "check that SOCKS5 listener is accepting connections and exit")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if *healthCheck {
		if err := runHealthCheck(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	log := logger.New(cfg.LogLevel)

	if cfg.UDP.Enabled {
		log.Debugf("UDP support enabled (remote: %s:%d)", cfg.UDP.RemotePath, cfg.UDP.Port)
	}

	tm := tunnel.NewManager(cfg, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := socks5.NewServer(cfg, tm, log)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe(ctx)
	}()

	select {
	case <-srv.Ready():
	case err := <-serverErr:
		log.Errorf("server error: %v", err)
		os.Exit(1)
	case <-time.After(10 * time.Second):
		log.Errorf("server failed to start within timeout")
		os.Exit(1)
	}

	if cfg.StartupTest.Enabled {
		startuptest.Run(ctx, cfg, tm, log)
	}

	go func() {
		sig := <-sigCh
		log.Debugf("received signal %s, shutting down", sig)
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		_ = tm.Shutdown(shutdownCtx)
	}()

	if err := <-serverErr; err != nil && ctx.Err() == nil {
		log.Errorf("server error: %v", err)
		os.Exit(1)
	}

	srv.Wait()
	log.Debugf("service stopped")
}

func runHealthCheck(cfg *config.Config) error {
	addr, err := cfg.HealthCheckAddr()
	if err != nil {
		return err
	}

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	_ = conn.Close()
	return nil
}
