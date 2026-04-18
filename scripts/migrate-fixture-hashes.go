// migrate-fixture-hashes recomputes content hashes in a fixture DB using the
// current ComputeContentHash formula.  Run once; idempotent.
package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

func computeContentHash(rawHex string) string {
	buf, err := hex.DecodeString(rawHex)
	if err != nil || len(buf) < 2 {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	headerByte := buf[0]
	offset := 1
	routeType := int(headerByte & 0x03)
	if routeType == 2 || routeType == 3 { // transport
		offset += 4
	}
	if offset >= len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}
	pathByte := buf[offset]
	offset++
	hashSize := int((pathByte>>6)&0x3) + 1
	hashCount := int(pathByte & 0x3F)
	pathBytes := hashSize * hashCount

	payloadStart := offset + pathBytes
	if payloadStart > len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	payload := buf[payloadStart:]
	payloadType := (headerByte >> 2) & 0x0F
	toHash := []byte{payloadType}

	// TRACE = payload type 7
	if int(payloadType) == 7 {
		toHash = append(toHash, pathByte, 0x00)
	}
	toHash = append(toHash, payload...)

	h := sha256.Sum256(toHash)
	return hex.EncodeToString(h[:])[:16]
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <db-path>\n", os.Args[0])
		os.Exit(1)
	}
	dbPath := os.Args[1]

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, raw_hex, hash FROM transmissions")
	if err != nil {
		log.Fatal(err)
	}

	type update struct {
		id      int
		newHash string
	}
	var updates []update

	for rows.Next() {
		var id int
		var rawHex, oldHash string
		if err := rows.Scan(&id, &rawHex, &oldHash); err != nil {
			log.Printf("scan: %v", err)
			continue
		}
		newHash := computeContentHash(rawHex)
		if newHash != oldHash {
			updates = append(updates, update{id, newHash})
		}
	}
	rows.Close()

	if len(updates) == 0 {
		fmt.Println("All hashes already match current formula.")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	stmt, err := tx.Prepare("UPDATE transmissions SET hash = ? WHERE id = ?")
	if err != nil {
		log.Fatal(err)
	}
	merged := 0
	for _, u := range updates {
		if _, err := stmt.Exec(u.newHash, u.id); err != nil {
			// UNIQUE constraint = duplicate (same content, different old hash).
			// Move observations to the surviving tx, then delete the dup.
			log.Printf("update id %d: %v — merging duplicate", u.id, err)
			// Find surviving tx id
			var survID int
			if err2 := tx.QueryRow("SELECT id FROM transmissions WHERE hash = ?", u.newHash).Scan(&survID); err2 == nil {
				tx.Exec("UPDATE observations SET transmission_id = ? WHERE transmission_id = ?", survID, u.id)
				tx.Exec("DELETE FROM transmissions WHERE id = ?", u.id)
				merged++
			}
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Migrated %d hashes, merged %d duplicates.\n", len(updates)-merged, merged)
}
