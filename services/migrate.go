package services

import (
	"context"
	"embed"
	"log"

	"github.com/shivanand-burli/go-starter-kit/postgress"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations applies all pending SQL migration files from the embedded FS.
// Files are executed in lexicographic order. Already-applied migrations are skipped.
// Panics on failure because the app cannot function without the schema.
func RunMigrations() {
	ctx := context.Background()
	if err := postgress.MigrateFS(ctx, migrationsFS, "migrations"); err != nil {
		log.Fatalf("[migration] MigrateFS failed: %v", err)
	}
}
