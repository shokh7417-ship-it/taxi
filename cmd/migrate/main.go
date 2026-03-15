package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/tursodatabase/libsql-client-go/libsql"
	"github.com/pressly/goose/v3"
)

func main() {
	_ = godotenv.Load()

	up := flag.Bool("up", false, "run migrations up")
	down := flag.Bool("down", false, "run last migration down")
	flag.Parse()

	if *up == *down {
		log.Fatal("set exactly one of -up or -down")
	}

	_ = os.Chdir(findProjectRoot())
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		url := os.Getenv("TURSO_DATABASE_URL")
		token := os.Getenv("TURSO_AUTH_TOKEN")
		if url != "" && token != "" {
			sep := "?"
			if strings.Contains(url, "?") {
				sep = "&"
			}
			dbURL = url + sep + "authToken=" + token
		}
	}
	if dbURL == "" {
		log.Fatal("DATABASE_URL or TURSO_DATABASE_URL + TURSO_AUTH_TOKEN required")
	}

	db, err := sql.Open("libsql", dbURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping db: %v", err)
	}
	if err := goose.SetDialect("sqlite3"); err != nil {
		log.Fatalf("set dialect: %v", err)
	}

	dir := "db/migrations"
	ctx := context.Background()
	if *up {
		if err := goose.RunContext(ctx, "up", db, dir); err != nil {
			log.Fatalf("migrate up: %v", err)
		}
		log.Println("migrations up: ok")
	} else {
		if err := goose.RunContext(ctx, "down", db, dir); err != nil {
			log.Fatalf("migrate down: %v", err)
		}
		log.Println("migration down: ok")
	}
}

// findProjectRoot walks up to find dir containing db/migrations.
func findProjectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "db", "migrations")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}
