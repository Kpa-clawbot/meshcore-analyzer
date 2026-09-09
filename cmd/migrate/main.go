// Command migrate runs all dbschema migrations against a SQLite
// CoreScope database and exits. Used by CI / one-shot tooling to bring
// an unmigrated fixture (or a fresh DB) up to the schema shape the
// read-only server (cmd/server) requires via dbschema.AssertReady.
//
// In production the ingestor (cmd/ingestor) runs dbschema.Apply at
// startup before subscribing to MQTT — this binary exists so CI's E2E
// job can migrate the e2e-fixture.db without booting the full ingestor
// (which needs MQTT brokers).
//
// Usage:
//
//	migrate -db path/to/file.db
package main

import (
	"database/sql"
	"flag"
	"log"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
)

func main() {
	dbPath := flag.String("db", "", "path to SQLite database to migrate (required)")
	flag.Parse()

	if *dbPath == "" {
		log.Fatalf("[migrate] -db is required")
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[migrate] ")

	// Same DSN as the ingestor. A bare path here would mean synchronous=NORMAL
	// under mattn, quietly weakening durability for a process that writes — and
	// this one runs dbschema.Apply, which now also deletes duplicate rows.
	db, err := sql.Open("sqlite3", dbschema.WriterDSN(*dbPath))
	if err != nil {
		log.Fatalf("open %s: %v", *dbPath, err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("ping %s: %v", *dbPath, err)
	}

	if err := dbschema.Apply(db, log.Printf); err != nil {
		log.Fatalf("dbschema.Apply: %v", err)
	}

	if err := dbschema.AssertReady(db); err != nil {
		log.Fatalf("dbschema.AssertReady after Apply: %v (this is a bug — Apply did not produce a ready schema)", err)
	}

	log.Printf("OK: %s is migrated and ready", *dbPath)
}
