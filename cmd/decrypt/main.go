// corescope-decrypt decrypts and exports hashtag channel messages from a CoreScope SQLite database.
//
// Usage:
//
//	corescope-decrypt --channel "#wardriving" --db meshcore.db [--format json|html] [--output file]
package main

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/channel"
	_ "modernc.org/sqlite"
)

// Version info (set via ldflags).
var version = "dev"

// ChannelMessage is a single decrypted channel message with metadata.
type ChannelMessage struct {
	Hash      string     `json:"hash"`
	Timestamp string     `json:"timestamp"`
	Sender    string     `json:"sender"`
	Message   string     `json:"message"`
	Channel   string     `json:"channel"`
	RawHex    string     `json:"raw_hex"`
	Path      []string   `json:"path"`
	Observers []Observer `json:"observers"`
}

// Observer is a single observation of the transmission.
type Observer struct {
	Name      string  `json:"name"`
	SNR       float64 `json:"snr"`
	RSSI      float64 `json:"rssi"`
	Timestamp string  `json:"timestamp"`
}

func main() {
	channelName := flag.String("channel", "", "Channel name (e.g. \"#wardriving\")")
	dbPath := flag.String("db", "", "Path to CoreScope SQLite database")
	format := flag.String("format", "json", "Output format: json, html, irc (or log)")
	output := flag.String("output", "", "Output file (default: stdout)")
	showVersion := flag.Bool("version", false, "Print version and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `corescope-decrypt — Decrypt and export MeshCore hashtag channel messages

USAGE
  corescope-decrypt --channel NAME --db PATH [--format FORMAT] [--output FILE]

FLAGS
  --channel NAME   Channel name to decrypt (e.g. "#wardriving", "wardriving")
                   The "#" prefix is added automatically if missing.
  --db PATH        Path to a CoreScope SQLite database file (read-only access).
  --format FORMAT  Output format (default: json):
                     json  — Machine-readable JSON array with full metadata
                     html  — Self-contained HTML viewer with search and sorting
                     irc   — Plain-text IRC-style log, one line per message
                     log   — Alias for irc
  --output FILE    Write output to FILE instead of stdout.
  --version        Print version and exit.

EXAMPLES
  # Export #wardriving messages as JSON
  corescope-decrypt --channel "#wardriving" --db /app/data/meshcore.db

  # Generate an interactive HTML viewer
  corescope-decrypt --channel wardriving --db meshcore.db --format html --output wardriving.html

  # Greppable IRC log
  corescope-decrypt --channel "#MeshCore" --db meshcore.db --format irc --output meshcore.log
  grep "KE6QR" meshcore.log

  # From the Docker container
  docker exec corescope-prod /app/corescope-decrypt --channel "#wardriving" --db /app/data/meshcore.db

RETROACTIVE DECRYPTION
  MeshCore hashtag channels use symmetric encryption — the key is derived from the
  channel name. The CoreScope ingestor stores ALL GRP_TXT packets in the database,
  even those it cannot decrypt at ingest time. This tool lets you retroactively
  decrypt messages for any channel whose name you know, even if the ingestor was
  never configured with that channel's key.

  This means you can recover historical messages by simply knowing the channel name.

LIMITATIONS
  - Only hashtag channels (shared-secret, name-derived key) are supported.
  - Direct messages (TXT_MSG) use per-peer encryption and cannot be decrypted.
  - Custom PSK channels (non-hashtag) require the raw key, not a channel name.
`)
	}

	flag.Parse()

	if *showVersion {
		fmt.Println("corescope-decrypt", version)
		os.Exit(0)
	}

	if *channelName == "" || *dbPath == "" {
		flag.Usage()
		os.Exit(1)
	}

	// Normalize channel name
	ch := *channelName
	if !strings.HasPrefix(ch, "#") {
		ch = "#" + ch
	}

	key := channel.DeriveKey(ch)
	chHash := channel.ChannelHash(key)

	db, err := sql.Open("sqlite", *dbPath+"?mode=ro")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Query all GRP_TXT packets
	rows, err := db.Query(`SELECT id, hash, raw_hex, first_seen FROM transmissions WHERE payload_type = 5`)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	var messages []ChannelMessage
	decrypted, total := 0, 0

	for rows.Next() {
		var id int
		var txHash, rawHex, firstSeen string
		if err := rows.Scan(&id, &txHash, &rawHex, &firstSeen); err != nil {
			log.Printf("Scan error: %v", err)
			continue
		}
		total++

		payload, err := extractGRPPayload(rawHex)
		if err != nil {
			continue
		}
		if len(payload) < 3 {
			continue
		}

		// Check channel hash byte
		if payload[0] != chHash {
			continue
		}

		mac := payload[1:3]
		ciphertext := payload[3:]
		if len(ciphertext) < 5 || len(ciphertext)%16 != 0 {
			// Pad ciphertext to block boundary for decryption attempt
			if len(ciphertext) < 16 {
				continue
			}
			// Truncate to block boundary
			ciphertext = ciphertext[:len(ciphertext)/16*16]
		}

		plaintext, ok := channel.Decrypt(key, mac, ciphertext)
		if !ok {
			continue
		}

		ts, sender, msg, err := channel.ParsePlaintext(plaintext)
		if err != nil {
			continue
		}

		decrypted++

		// Convert MeshCore timestamp
		timestamp := time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)

		// Get path from decoded_json
		path := getPathFromDB(db, id)

		// Get observers
		observers := getObservers(db, id)

		messages = append(messages, ChannelMessage{
			Hash:      txHash,
			Timestamp: timestamp,
			Sender:    sender,
			Message:   msg,
			Channel:   ch,
			RawHex:    rawHex,
			Path:      path,
			Observers: observers,
		})
	}

	// Sort by timestamp
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp < messages[j].Timestamp
	})

	log.Printf("Scanned %d GRP_TXT packets, decrypted %d for channel %s", total, decrypted, ch)

	// Generate output
	var out []byte
	switch *format {
	case "json":
		out, err = json.MarshalIndent(messages, "", "  ")
		if err != nil {
			log.Fatalf("JSON marshal: %v", err)
		}
		out = append(out, '\n')
	case "html":
		out = renderHTML(messages, ch)
	case "irc", "log":
		out = renderIRC(messages)
	default:
		log.Fatalf("Unknown format: %s (use json, html, irc, or log)", *format)
	}

	if *output != "" {
		if err := os.WriteFile(*output, out, 0644); err != nil {
			log.Fatalf("Write file: %v", err)
		}
		log.Printf("Written to %s", *output)
	} else {
		os.Stdout.Write(out)
	}
}

// extractGRPPayload parses a raw hex packet and returns the GRP_TXT payload bytes.
func extractGRPPayload(rawHex string) ([]byte, error) {
	buf, err := hex.DecodeString(strings.TrimSpace(rawHex))
	if err != nil || len(buf) < 2 {
		return nil, fmt.Errorf("invalid hex")
	}

	// Header byte
	header := buf[0]
	payloadType := int((header >> 2) & 0x0F)
	if payloadType != 5 { // GRP_TXT
		return nil, fmt.Errorf("not GRP_TXT")
	}

	routeType := int(header & 0x03)
	offset := 1

	// Transport codes (2 codes × 2 bytes) come BEFORE path for transport routes
	if routeType == 0 || routeType == 3 {
		offset += 4
	}

	// Path byte
	if offset >= len(buf) {
		return nil, fmt.Errorf("too short for path")
	}
	pathByte := buf[offset]
	offset++
	hashSize := int(pathByte>>6) + 1
	hashCount := int(pathByte & 0x3F)
	offset += hashSize * hashCount

	if offset >= len(buf) {
		return nil, fmt.Errorf("too short for payload")
	}

	return buf[offset:], nil
}

func getPathFromDB(db *sql.DB, txID int) []string {
	var decodedJSON sql.NullString
	err := db.QueryRow(`SELECT decoded_json FROM transmissions WHERE id = ?`, txID).Scan(&decodedJSON)
	if err != nil || !decodedJSON.Valid {
		return nil
	}

	var decoded struct {
		Path struct {
			Hops []string `json:"hops"`
		} `json:"path"`
	}
	if json.Unmarshal([]byte(decodedJSON.String), &decoded) == nil {
		return decoded.Path.Hops
	}
	return nil
}

func getObservers(db *sql.DB, txID int) []Observer {
	rows, err := db.Query(`
		SELECT o.name, obs.snr, obs.rssi, obs.timestamp
		FROM observations obs
		LEFT JOIN observers o ON o.id = CAST(obs.observer_idx AS TEXT)
		WHERE obs.transmission_id = ?
		ORDER BY obs.timestamp
	`, txID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var observers []Observer
	for rows.Next() {
		var name sql.NullString
		var snr, rssi sql.NullFloat64
		var ts int64
		if err := rows.Scan(&name, &snr, &rssi, &ts); err != nil {
			continue
		}
		obs := Observer{
			Timestamp: time.Unix(ts, 0).UTC().Format(time.RFC3339),
		}
		if name.Valid {
			obs.Name = name.String
		}
		if snr.Valid {
			obs.SNR = snr.Float64
		}
		if rssi.Valid {
			obs.RSSI = rssi.Float64
		}
		observers = append(observers, obs)
	}
	return observers
}

func renderIRC(messages []ChannelMessage) []byte {
	var b strings.Builder
	for _, m := range messages {
		sender := m.Sender
		if sender == "" {
			sender = "???"
		}
		// Parse RFC3339 timestamp into a compact format
		t, err := time.Parse(time.RFC3339, m.Timestamp)
		if err != nil {
			b.WriteString(fmt.Sprintf("[%s] <%s> %s\n", m.Timestamp, sender, m.Message))
			continue
		}
		b.WriteString(fmt.Sprintf("[%s] <%s> %s\n", t.Format("2006-01-02 15:04:05"), sender, m.Message))
	}
	return []byte(b.String())
}

func renderHTML(messages []ChannelMessage, channelName string) []byte {
	jsonData, _ := json.Marshal(messages)

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>CoreScope Channel Export — ` + html.EscapeString(channelName) + `</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:#0d1117;color:#c9d1d9;padding:20px}
h1{color:#58a6ff;margin-bottom:16px;font-size:1.5em}
.stats{color:#8b949e;margin-bottom:16px;font-size:0.9em}
input[type=text]{width:100%;max-width:500px;padding:8px 12px;background:#161b22;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:14px;margin-bottom:16px}
input[type=text]:focus{outline:none;border-color:#58a6ff}
table{width:100%;border-collapse:collapse;font-size:14px}
th{background:#161b22;color:#8b949e;text-align:left;padding:8px 12px;border-bottom:2px solid #30363d;cursor:pointer;user-select:none;white-space:nowrap}
th:hover{color:#58a6ff}
th.sorted-asc::after{content:" ▲"}
th.sorted-desc::after{content:" ▼"}
td{padding:8px 12px;border-bottom:1px solid #21262d;vertical-align:top}
tr:hover{background:#161b22}
tr.expanded{background:#161b22}
.detail-row td{padding:12px 24px;background:#0d1117;border-bottom:1px solid #21262d}
.detail-row pre{background:#161b22;padding:12px;border-radius:6px;overflow-x:auto;font-size:12px;color:#8b949e}
.detail-row .label{color:#58a6ff;font-weight:600;margin-top:8px;display:block}
.observer-tag{display:inline-block;background:#1f6feb22;color:#58a6ff;padding:2px 8px;border-radius:4px;margin:2px;font-size:12px}
.no-results{color:#8b949e;text-align:center;padding:40px;font-size:16px}
.sender{color:#d2a8ff;font-weight:600}
.timestamp{color:#8b949e;font-family:monospace;font-size:12px}
</style>
</head>
<body>
<h1>` + html.EscapeString(channelName) + ` — Channel Messages</h1>
<div class="stats" id="stats"></div>
<input type="text" id="search" placeholder="Search messages..." autocomplete="off">
<table>
<thead>
<tr>
<th data-col="timestamp">Timestamp</th>
<th data-col="sender">Sender</th>
<th data-col="message">Message</th>
<th data-col="observers">Observers</th>
</tr>
</thead>
<tbody id="tbody"></tbody>
</table>
<div class="no-results" id="no-results" style="display:none">No matching messages</div>
<script>
var DATA=` + string(jsonData) + `;
var sortCol="timestamp",sortAsc=true,expandedHash=null;
function init(){
document.getElementById("stats").textContent=DATA.length+" messages";
document.getElementById("search").addEventListener("input",render);
document.querySelectorAll("th[data-col]").forEach(function(th){
th.addEventListener("click",function(){
var col=th.dataset.col;
if(sortCol===col)sortAsc=!sortAsc;
else{sortCol=col;sortAsc=true}
render();
});
});
render();
}
function render(){
var q=document.getElementById("search").value.toLowerCase();
var filtered=DATA.filter(function(m){
if(!q)return true;
return(m.message||"").toLowerCase().indexOf(q)>=0||(m.sender||"").toLowerCase().indexOf(q)>=0;
});
filtered.sort(function(a,b){
var va=a[sortCol]||"",vb=b[sortCol]||"";
if(sortCol==="observers"){va=a.observers?a.observers.length:0;vb=b.observers?b.observers.length:0}
if(va<vb)return sortAsc?-1:1;
if(va>vb)return sortAsc?1:-1;
return 0;
});
document.querySelectorAll("th[data-col]").forEach(function(th){
th.className=th.dataset.col===sortCol?(sortAsc?"sorted-asc":"sorted-desc"):"";
});
var tb=document.getElementById("tbody");
tb.innerHTML="";
document.getElementById("no-results").style.display=filtered.length?"none":"block";
filtered.forEach(function(m){
var tr=document.createElement("tr");
tr.innerHTML='<td class="timestamp">'+esc(m.timestamp)+'</td><td class="sender">'+esc(m.sender||"—")+'</td><td>'+esc(m.message)+'</td><td>'+
(m.observers?m.observers.map(function(o){return'<span class="observer-tag">'+esc(o.name||"?")+" SNR:"+o.snr.toFixed(1)+'</span>'}).join(""):"—")+'</td>';
tr.style.cursor="pointer";
tr.addEventListener("click",function(){
expandedHash=expandedHash===m.hash?null:m.hash;
render();
});
tb.appendChild(tr);
if(expandedHash===m.hash){
tr.className="expanded";
var dr=document.createElement("tr");
dr.className="detail-row";
dr.innerHTML='<td colspan="4"><span class="label">Hash</span><pre>'+esc(m.hash)+'</pre>'+
'<span class="label">Raw Hex</span><pre>'+esc(m.raw_hex)+'</pre>'+
(m.path&&m.path.length?'<span class="label">Path</span><pre>'+esc(m.path.join(" → "))+'</pre>':'')+
'<span class="label">Observers</span><pre>'+esc(JSON.stringify(m.observers,null,2))+'</pre></td>';
tb.appendChild(dr);
}
});
}
function esc(s){var d=document.createElement("div");d.textContent=s;return d.innerHTML}
init();
</script>
</body>
</html>`)

	return []byte(b.String())
}
