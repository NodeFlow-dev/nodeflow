package migrate

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nodeflow/nodeflow/migrations"
)

func TestLoadEmbeddedMigrations(t *testing.T) {
	list, err := Load(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Version != "000001" || !strings.Contains(list[0].SQL, "CREATE") {
		t.Fatalf("first migration = %+v", list[0].Name)
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Version >= list[i].Version {
			t.Fatalf("migrations out of order: %s then %s", list[i-1].Name, list[i].Name)
		}
	}
}

func TestLoadRejectsBadNames(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"no prefix": {"init.up.sql": {Data: []byte("SELECT 1")}},
		"duplicate": {"000001_a.up.sql": {Data: []byte("SELECT 1")}, "000001_b.up.sql": {Data: []byte("SELECT 1")}},
		"empty":     {},
	} {
		if _, err := Load(files); err == nil {
			t.Errorf("%s: Load succeeded", name)
		}
	}
}

// TestApplyIntegration applies the embedded migrations to a fresh schema of
// NODEFLOW_TEST_DATABASE_URL twice (the second run must be a no-op).
func TestApplyIntegration(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := "nf_migrate_test_" + time.Now().UTC().Format("150405")
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema+"; SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	list, err := Load(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(ctx, conn, list, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(list) {
		t.Fatalf("applied %d of %d migrations", len(applied), len(list))
	}
	again, err := Apply(ctx, conn, list, t.Logf)
	if err != nil || len(again) != 0 {
		t.Fatalf("second run applied %v, err %v", again, err)
	}
}
