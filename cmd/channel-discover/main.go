package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

// grpTxtPayload is the decoded_json shape for GRP_TXT packets.
type grpTxtPayload struct {
	Type             string `json:"type"`
	ChannelHash      int    `json:"channelHash"`
	ChannelHashHex   string `json:"channelHashHex"`
	DecryptionStatus string `json:"decryptionStatus"`
	MAC              string `json:"mac"`
	EncryptedData    string `json:"encryptedData"`
}

// undecryptedPacket holds a GRP_TXT packet that failed decryption.
type undecryptedPacket struct {
	ID            int
	Hash          string
	ChannelHash   byte
	MAC           string
	EncryptedData string
}

// discoveredChannel is a confirmed channel discovery result.
type discoveredChannel struct {
	Name           string `json:"name"`
	Key            string `json:"key"`
	ChannelHash    string `json:"channelHash"`
	PacketsMatched int    `json:"packetsMatched"`
	SampleMessages []sampleMessage `json:"sampleMessages"`
}

type sampleMessage struct {
	Sender    string `json:"sender,omitempty"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
}

// deriveChannelKey derives an AES-128 key from a hashtag channel name.
// key = SHA256(name)[:16]
func deriveChannelKey(name string) []byte {
	h := sha256.Sum256([]byte(name))
	return h[:16]
}

// channelHashFromKey computes the 1-byte channel hash from a 16-byte key.
// channelHash = SHA256(key)[0]
func channelHashFromKey(key []byte) byte {
	h := sha256.Sum256(key)
	return h[0]
}

// tryDecrypt attempts to decrypt ciphertext with given key and MAC.
// Returns (sender, message, timestamp, ok).
func tryDecrypt(ciphertextHex, macHex string, key []byte) (string, string, uint32, bool) {
	macBytes, err := hex.DecodeString(macHex)
	if err != nil || len(macBytes) != 2 {
		return "", "", 0, false
	}
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", "", 0, false
	}

	// HMAC-SHA256 verification: secret = key + 16 zero bytes
	secret := make([]byte, 32)
	copy(secret, key)
	h := hmac.New(sha256.New, secret)
	h.Write(ciphertext)
	mac := h.Sum(nil)
	if mac[0] != macBytes[0] || mac[1] != macBytes[1] {
		return "", "", 0, false
	}

	// AES-128-ECB decrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", 0, false
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}

	if len(plaintext) < 5 {
		return "", "", 0, false
	}
	timestamp := binary.LittleEndian.Uint32(plaintext[0:4])
	// flags := plaintext[4]
	msg := string(plaintext[5:])
	if idx := strings.IndexByte(msg, 0); idx >= 0 {
		msg = msg[:idx]
	}

	// Validate: must be printable UTF-8
	if !utf8.ValidString(msg) {
		return "", "", 0, false
	}
	nonPrintable := 0
	for _, r := range msg {
		if r < 0x20 && r != '\n' && r != '\t' {
			nonPrintable++
		} else if r == utf8.RuneError {
			nonPrintable++
		}
	}
	if nonPrintable > 2 {
		return "", "", 0, false
	}

	// Parse "sender: message"
	sender := ""
	text := msg
	if idx := strings.Index(msg, ": "); idx > 0 && idx < 50 {
		potential := msg[:idx]
		if !strings.ContainsAny(potential, ":[]") {
			sender = potential
			text = msg[idx+2:]
		}
	}

	return sender, text, timestamp, true
}

// loadPackets extracts undecrypted GRP_TXT packets from the DB.
func loadPackets(db *sql.DB) ([]undecryptedPacket, error) {
	rows, err := db.Query(`
		SELECT id, hash, decoded_json
		FROM transmissions
		WHERE payload_type = 5 AND decoded_json IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var packets []undecryptedPacket
	for rows.Next() {
		var id int
		var hash, djson string
		if err := rows.Scan(&id, &hash, &djson); err != nil {
			continue
		}
		var p grpTxtPayload
		if err := json.Unmarshal([]byte(djson), &p); err != nil {
			continue
		}
		// Include both decryption_failed and no_key packets
		if p.DecryptionStatus != "decrypted" && p.EncryptedData != "" && p.MAC != "" {
			packets = append(packets, undecryptedPacket{
				ID:            id,
				Hash:          hash,
				ChannelHash:   byte(p.ChannelHash),
				MAC:           p.MAC,
				EncryptedData: p.EncryptedData,
			})
		}
	}
	return packets, rows.Err()
}

// loadWordlist reads a file with one word per line.
func loadWordlist(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var words []string
	for _, line := range strings.Split(string(data), "\n") {
		w := strings.TrimSpace(line)
		if w != "" && !strings.HasPrefix(w, "#") {
			words = append(words, w)
		}
	}
	return words, nil
}

// defaultWordlist returns a built-in list of common channel name candidates.
func defaultWordlist() []string {
	return []string{
		// Common mesh/radio terms
		"test", "testing", "general", "chat", "local", "help", "emergency",
		"net", "repeater", "mesh", "meshcore", "lora", "radio", "ham",
		"hf", "vhf", "uhf", "simplex", "duplex", "packet", "digital",
		"analog", "beacon", "relay", "node", "base", "mobile", "portable",
		"antenna", "tower", "signal", "frequency", "channel", "band",
		"monitor", "scanner", "wx", "weather", "alert", "warning",
		"ares", "races", "emcomm", "skywarn", "cert", "fema",
		"sos", "mayday", "rescue", "search", "fire", "medical",
		"police", "sheriff", "ems", "dispatch",

		// Common words
		"hello", "world", "admin", "default", "public", "private",
		"open", "closed", "secure", "secret", "password", "key",
		"group", "team", "family", "friends", "club", "community",
		"network", "system", "server", "client", "device",
		"home", "office", "work", "school", "park", "trail",
		"mountain", "valley", "river", "lake", "ocean", "beach",
		"forest", "desert", "island", "bridge", "road", "highway",
		"north", "south", "east", "west", "central", "downtown",
		"urban", "rural", "suburban", "metro",

		// Tech/hacker terms
		"hack", "hacker", "cyber", "crypto", "bitcoin", "blockchain",
		"linux", "unix", "windows", "mac", "android", "ios",
		"wifi", "bluetooth", "zigbee", "zwave", "mqtt", "iot",
		"sensor", "gps", "tracker", "ping", "pong", "echo",
		"debug", "dev", "prod", "staging", "beta", "alpha",
		"demo", "sample", "example", "foo", "bar", "baz",

		// US cities
		"seattle", "portland", "sanfrancisco", "losangeles", "sandiego",
		"denver", "phoenix", "dallas", "houston", "austin", "chicago",
		"newyork", "boston", "miami", "atlanta", "nashville",
		"detroit", "minneapolis", "stlouis", "kansascity", "omaha",
		"saltlakecity", "lasvegas", "albuquerque", "tucson", "reno",
		"boise", "spokane", "tacoma", "eugene", "bend", "olympia",
		"sacramento", "oakland", "sanjose", "fresno", "bakersfield",
		"anchorage", "honolulu", "fairbanks", "juneau",

		// PNW / Cascadia specific
		"cascadia", "pnw", "pacific", "northwest", "puget", "sound",
		"rainier", "hood", "helens", "baker", "olympic", "cascade",
		"columbia", "willamette", "snake", "fraser", "skagit",
		"bellingham", "everett", "redmond", "bellevue", "kirkland",
		"issaquah", "sammamish", "mercer", "whidbey", "orcas",
		"sanjuan", "lopez", "vashon", "bainbridge", "camano",
		"corvallis", "salem", "medford", "astoria", "cannon",
		"victoria", "vancouver", "whistler", "nanaimo", "kelowna",

		// US states
		"alabama", "alaska", "arizona", "arkansas", "california",
		"colorado", "connecticut", "delaware", "florida", "georgia",
		"hawaii", "idaho", "illinois", "indiana", "iowa",
		"kansas", "kentucky", "louisiana", "maine", "maryland",
		"massachusetts", "michigan", "minnesota", "mississippi", "missouri",
		"montana", "nebraska", "nevada", "newhampshire", "newjersey",
		"newmexico", "newyork", "northcarolina", "northdakota", "ohio",
		"oklahoma", "oregon", "pennsylvania", "rhodeisland", "southcarolina",
		"southdakota", "tennessee", "texas", "utah", "vermont",
		"virginia", "washington", "westvirginia", "wisconsin", "wyoming",

		// Numbers and simple patterns
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "10",
		"42", "69", "100", "123", "420", "666", "911", "1234",
		"chan1", "chan2", "chan3", "ch1", "ch2", "ch3",
		"group1", "group2", "group3", "grp1", "grp2", "grp3",
		"net1", "net2", "net3", "mesh1", "mesh2", "mesh3",

		// Call sign prefixes
		"w", "k", "n", "wa", "wb", "wc", "wd", "ka", "kb", "kc", "kd",
		"ke", "kf", "kg", "ki", "kj", "kk", "kl", "km", "kn", "ko",
		"kp", "kq", "kr", "ks", "kt", "ku", "kv", "kw", "kx", "ky", "kz",

		// Outdoor/prepper
		"prepper", "survival", "offgrid", "bugout", "shtf", "shtshtf",
		"camping", "hiking", "hunting", "fishing", "climbing",
		"backpacking", "overlanding", "jeep", "offroad", "4x4",
		"bushcraft", "homestead", "farm", "ranch", "garden",

		// Events/organizations
		"defcon", "hamfest", "fieldday", "arrl", "amsat", "aprs",
		"winlink", "vara", "js8", "ft8", "psk31", "sstv",
		"dmr", "dstar", "fusion", "p25", "nxdn", "tetra",
		"meshtastic", "gotenna", "baofeng", "yaesu", "icom", "kenwood",
		"elecraft", "flexradio",

		// Misc common
		"love", "peace", "freedom", "liberty", "justice", "truth",
		"power", "energy", "solar", "wind", "water", "earth",
		"space", "moon", "mars", "stars", "galaxy", "universe",
		"cats", "dogs", "birds", "fish", "wolves", "bears", "eagles",
		"coffee", "beer", "wine", "pizza", "taco", "burrito",
		"music", "rock", "jazz", "blues", "country", "metal",
		"game", "play", "fun", "cool", "awesome", "epic",
		"nostr", "fedi", "mastodon", "matrix", "signal", "telegram",

		// Short common words that might be channels
		"go", "run", "fly", "sky", "sun", "fog", "ice", "hot", "cold",
		"new", "old", "big", "top", "low", "all", "one", "two", "ten",
		"red", "blue", "green", "black", "white", "gold", "grey", "gray",
		"oak", "elm", "pine", "fir", "ash", "bay", "cove", "cape",
		"port", "dock", "pier", "reef", "wave", "surf", "tide", "sand",
	}
}

func main() {
	dbPath := flag.String("db", "", "Path to CoreScope SQLite database")
	wordlistPath := flag.String("wordlist", "", "Path to custom wordlist file (one word per line)")
	singleName := flag.String("name", "", "Test a single channel name (e.g. '#test')")
	verbose := flag.Bool("verbose", false, "Show progress and timing details")
	jsonOutput := flag.Bool("json", false, "Output results as JSON")
	maxSamples := flag.Int("samples", 3, "Max sample messages per discovered channel")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: channel-discover -db <path-to-db> [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	db, err := sql.Open("sqlite3", *dbPath+"?mode=ro")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Load undecrypted packets
	packets, err := loadPackets(db)
	if err != nil {
		log.Fatalf("Failed to load packets: %v", err)
	}
	if len(packets) == 0 {
		fmt.Println("No undecrypted GRP_TXT packets found in database.")
		return
	}

	// Group packets by channelHash
	byHash := make(map[byte][]undecryptedPacket)
	for _, p := range packets {
		byHash[p.ChannelHash] = append(byHash[p.ChannelHash], p)
	}

	if *verbose {
		fmt.Printf("Found %d undecrypted GRP_TXT packets across %d unique channel hashes\n",
			len(packets), len(byHash))
		for h, pkts := range byHash {
			fmt.Printf("  channelHash 0x%02X: %d packets\n", h, len(pkts))
		}
		fmt.Println()
	}

	// Build candidate list
	var candidates []string
	if *singleName != "" {
		name := *singleName
		if !strings.HasPrefix(name, "#") {
			name = "#" + name
		}
		candidates = []string{name}
	} else {
		// Start with default wordlist
		words := defaultWordlist()

		// Add custom wordlist if provided
		if *wordlistPath != "" {
			custom, err := loadWordlist(*wordlistPath)
			if err != nil {
				log.Fatalf("Failed to load wordlist: %v", err)
			}
			words = append(words, custom...)
			if *verbose {
				fmt.Printf("Loaded %d words from custom wordlist\n", len(custom))
			}
		}

		// Generate candidates: each word as "#word"
		seen := make(map[string]bool)
		for _, w := range words {
			w = strings.ToLower(strings.TrimSpace(w))
			if w == "" {
				continue
			}
			// Try with # prefix (standard hashtag channel)
			name := "#" + w
			if !seen[name] {
				candidates = append(candidates, name)
				seen[name] = true
			}
		}

		if *verbose {
			fmt.Printf("Generated %d candidate channel names\n\n", len(candidates))
		}
	}

	// Precompute candidate keys and hashes, filter by matching channelHash
	type candidate struct {
		Name        string
		Key         []byte
		ChannelHash byte
	}

	var matched []candidate
	start := time.Now()

	for _, name := range candidates {
		key := deriveChannelKey(name)
		ch := channelHashFromKey(key)
		if _, ok := byHash[ch]; ok {
			matched = append(matched, candidate{Name: name, Key: key, ChannelHash: ch})
		}
	}

	if *verbose {
		fmt.Printf("Hash precompute: %d candidates → %d hash matches (%.1f ms)\n",
			len(candidates), len(matched), float64(time.Since(start).Microseconds())/1000)
	}

	// Attempt decryption for each matched candidate
	var discovered []discoveredChannel
	decryptAttempts := 0

	for _, c := range matched {
		pkts := byHash[c.ChannelHash]
		var samples []sampleMessage
		decrypted := 0

		for _, pkt := range pkts {
			if len(pkt.EncryptedData) < 10 {
				continue
			}
			decryptAttempts++
			sender, text, ts, ok := tryDecrypt(pkt.EncryptedData, pkt.MAC, c.Key)
			if ok {
				decrypted++
				if len(samples) < *maxSamples {
					t := time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
					samples = append(samples, sampleMessage{
						Sender:    sender,
						Text:      text,
						Timestamp: t,
					})
				}
			}
		}

		if decrypted > 0 {
			discovered = append(discovered, discoveredChannel{
				Name:           c.Name,
				Key:            hex.EncodeToString(c.Key),
				ChannelHash:    fmt.Sprintf("0x%02X", c.ChannelHash),
				PacketsMatched: decrypted,
				SampleMessages: samples,
			})
		}
	}

	elapsed := time.Since(start)

	// Output results
	if *jsonOutput {
		out := struct {
			Candidates      int                 `json:"candidatesTested"`
			HashMatches     int                 `json:"hashMatches"`
			DecryptAttempts int                 `json:"decryptAttempts"`
			Discovered      []discoveredChannel `json:"discovered"`
			ElapsedMs       float64             `json:"elapsedMs"`
		}{
			Candidates:      len(candidates),
			HashMatches:     len(matched),
			DecryptAttempts: decryptAttempts,
			Discovered:      discovered,
			ElapsedMs:       float64(elapsed.Microseconds()) / 1000,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return
	}

	// Human-readable output
	fmt.Printf("Channel Discovery Results\n")
	fmt.Printf("========================\n\n")
	fmt.Printf("Database: %s\n", *dbPath)
	fmt.Printf("Undecrypted packets: %d (%d unique channel hashes)\n", len(packets), len(byHash))
	fmt.Printf("Candidates tested: %d\n", len(candidates))
	fmt.Printf("Hash matches: %d (filtered by 1-byte channelHash)\n", len(matched))
	fmt.Printf("Decryption attempts: %d\n", decryptAttempts)
	fmt.Printf("Time: %.1f ms (%.0f candidates/sec)\n\n", float64(elapsed.Microseconds())/1000,
		float64(len(candidates))/elapsed.Seconds())

	if len(discovered) == 0 {
		fmt.Println("No channels discovered.")
		fmt.Println("\nTips:")
		fmt.Println("  - Try a custom wordlist with domain-specific terms: -wordlist words.txt")
		fmt.Println("  - Test a specific guess: -name \"#yourchannel\"")
		fmt.Println("  - Channel names are case-sensitive and include the '#' prefix")
		return
	}

	fmt.Printf("✓ Discovered %d channel(s):\n\n", len(discovered))
	for _, ch := range discovered {
		fmt.Printf("  Channel: %s\n", ch.Name)
		fmt.Printf("  Key:     %s\n", ch.Key)
		fmt.Printf("  Hash:    %s\n", ch.ChannelHash)
		fmt.Printf("  Packets: %d decrypted\n", ch.PacketsMatched)
		if len(ch.SampleMessages) > 0 {
			fmt.Printf("  Sample messages:\n")
			for _, m := range ch.SampleMessages {
				if m.Sender != "" {
					fmt.Printf("    [%s] %s: %s\n", m.Timestamp, m.Sender, m.Text)
				} else {
					fmt.Printf("    [%s] %s\n", m.Timestamp, m.Text)
				}
			}
		}
		fmt.Println()
	}
}
