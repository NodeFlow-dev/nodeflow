package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nodeflow/nodeflow/internal/migrate"
	"github.com/nodeflow/nodeflow/migrations"
)

// runMigrate applies the migrations embedded in this binary to DATABASE_URL.
// Used by the release compose file (`panel-api migrate`), so installs need
// neither psql nor a source checkout.
func runMigrate() int {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	list, err := migrate.Load(migrations.Files)
	if err != nil {
		slog.Error("load migrations", "error", err)
		return 1
	}
	var conn *pgx.Conn
	for attempt := 1; ; attempt++ {
		conn, err = pgx.Connect(ctx, databaseURL)
		if err == nil {
			break
		}
		if attempt >= 30 {
			slog.Error("database unavailable", "error", err)
			return 1
		}
		time.Sleep(time.Second)
	}
	defer conn.Close(context.Background())

	applied, err := migrate.Apply(ctx, conn, list, func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})
	if err != nil {
		slog.Error("migration failed", "error", err)
		return 1
	}
	fmt.Printf("migrations up to date: %d applied, %d total, panel %s\n", len(applied), len(list), panelVersion)
	return 0
}
