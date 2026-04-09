package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xlyk/triptych/internal/db"
	"github.com/xlyk/triptych/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("TRIPTYCH_DATABASE_URL")
	if dsn == "" {
		log.Fatal("TRIPTYCH_DATABASE_URL is required")
	}
	addr := os.Getenv("TRIPTYCH_SERVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := server.NewHandler(db.NewStore(pool), logger)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("starting agentserver", "addr", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
