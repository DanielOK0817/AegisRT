package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (resultErr error) {
	if ctx == nil {
		return errors.New("context is required")
	}
	options, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	if options.showVersion {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}
	app, err := buildDaemon(options)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, app.close()) }()
	listener, err := net.Listen("tcp", options.listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", options.listenAddress, err)
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           loopbackHostGuard(app.handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	stats := app.kernel.Stats()
	logger.Info("AegisRT started",
		"address", listener.Addr().String(), "version", version, "agents", app.agentCount,
		"durable", stats.Durable, "generation", stats.Generation,
		"state_loaded", app.recovery.Loaded,
		"tasks_requeued", len(app.recovery.RequeuedTaskIDs),
		"tasks_failed", len(app.recovery.FailedTaskIDs),
		"tasks_canceled", len(app.recovery.CanceledTaskIDs),
	)

	serviceCtx, stopService := context.WithCancel(ctx)
	defer stopService()
	httpErrors := make(chan error, 1)
	serviceErrors := make(chan error, 1)
	go func() {
		httpErrors <- server.Serve(listener)
	}()
	go func() {
		serviceErrors <- app.service.Run(serviceCtx)
	}()
	app.ready.Store(true)
	defer app.ready.Store(false)

	httpReceived := false
	serviceReceived := false
	// The listener is bound and readiness is raised before the one-shot
	// receipt. A supervisor may still need a short retry while Serve is first
	// scheduled, so the receipt is an endpoint-discovery handshake, not an HTTP
	// response substitute.
	triggerErr := emitStartupReceipt(options.startupReceiptFD,
		newStartupReceipt(listener.Addr().String(), app.runtimeNamespace, app.recovery))
	if triggerErr != nil {
		triggerErr = fmt.Errorf("emit startup receipt: %w", triggerErr)
	} else {
		select {
		case <-ctx.Done():
		case serveErr := <-httpErrors:
			httpReceived = true
			if !errors.Is(serveErr, http.ErrServerClosed) {
				triggerErr = fmt.Errorf("serve HTTP: %w", serveErr)
			} else if ctx.Err() == nil {
				triggerErr = errors.New("HTTP server stopped unexpectedly")
			}
		case serviceErr := <-serviceErrors:
			serviceReceived = true
			if serviceErr != nil {
				triggerErr = fmt.Errorf("run Runtime service: %w", serviceErr)
			} else if ctx.Err() == nil {
				triggerErr = errors.New("Runtime service stopped unexpectedly")
			}
		}
	}

	app.ready.Store(false)
	stopService()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), options.shutdownTimeout)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("shutdown HTTP server: %w", shutdownErr)
	}
	var httpErr error
	if !httpReceived {
		httpErr = waitComponent(shutdownCtx, "HTTP server", httpErrors, true)
	}
	var serviceErr error
	if !serviceReceived {
		serviceErr = waitComponent(shutdownCtx, "Runtime service", serviceErrors, false)
	}
	return errors.Join(triggerErr, shutdownErr, httpErr, serviceErr)
}

func waitComponent(ctx context.Context, name string, results <-chan error, allowServerClosed bool) error {
	select {
	case err := <-results:
		if err == nil || allowServerClosed && errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("%s stopped: %w", name, err)
	case <-ctx.Done():
		return fmt.Errorf("wait for %s: %w", name, ctx.Err())
	}
}
