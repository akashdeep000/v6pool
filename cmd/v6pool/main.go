// Command v6pool is a rotating IPv6 proxy that cycles source addresses per
// request and supports sticky sessions and optional address claiming.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akashdeep000/v6pool/internal/config"
	"github.com/akashdeep000/v6pool/internal/proxy"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath string
		showVer    bool
	)
	flag.StringVar(&configPath, "config", "", "path to config file (default /etc/v6pool/config.yaml)")
	flag.BoolVar(&showVer, "version", false, "print version and exit")
	flag.Parse()
	if showVer {
		fmt.Printf("v6pool %s\n", version)
		return nil
	}
	if configPath == "" {
		configPath = "/etc/v6pool/config.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("starting", "version", version, "config", configPath)

	px, err := proxy.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 3)

	if cfg.EnableHTTP && cfg.HTTPListen != "" {
		ln, err := net.Listen("tcp", cfg.HTTPListen)
		if err != nil {
			return fmt.Errorf("http listen: %w", err)
		}
		go func() {
			slog.Info("http proxy listening", "addr", ln.Addr())
			errCh <- http.Serve(ln, px.Handler())
		}()
	} else {
		slog.Info("http proxy disabled")
	}

	if cfg.EnableSOCKS5 && cfg.SOCKS5Listen != "" {
		ln, err := net.Listen("tcp", cfg.SOCKS5Listen)
		if err != nil {
			return fmt.Errorf("socks5 listen: %w", err)
		}
		go func() {
			slog.Info("socks5 listening", "addr", ln.Addr())
			errCh <- serveSOCKS5(ln, px)
		}()
	} else {
		slog.Info("socks5 proxy disabled")
	}

	if cfg.EnableStats && cfg.StatsListen != "" {
		ln, err := net.Listen("tcp", cfg.StatsListen)
		if err != nil {
			return fmt.Errorf("stats listen: %w", err)
		}
		go func() {
			slog.Info("stats listening", "addr", ln.Addr())
			errCh <- http.Serve(ln, px.StatsHandler(cfg.StatsToken))
		}()
	} else {
		slog.Info("stats server disabled")
	}

	if !cfg.EnableHTTP && !cfg.EnableSOCKS5 && !cfg.EnableStats {
		slog.Warn("all listeners disabled, proxy will not accept connections")
	}

	// Sweep expired sticky sessions and idle claimed addresses on a fixed
	// schedule while the proxy runs.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				px.SweepSessions()
				px.SweepClaims()
			}
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		return nil
	case err := <-errCh:
		return err
	}
}

// serveSOCKS5 accepts TCP connections and hands each to the proxy's SOCKS5
// handler.
func serveSOCKS5(ln net.Listener, px *proxy.Proxy) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go px.HandleSOCKS5(conn)
	}
}
