package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	// pprof profiling — off by default, enable with ENABLE_PPROF=true
	if os.Getenv("ENABLE_PPROF") == "true" {
		pprofPort := os.Getenv("PPROF_PORT")
		if pprofPort == "" {
			pprofPort = "6061"
		}
		go func() {
			log.Printf("[pprof] ingestor profiling at http://localhost:%s/debug/pprof/", pprofPort)
			if err := http.ListenAndServe(":"+pprofPort, nil); err != nil {
				log.Printf("[pprof] failed to start: %v (non-fatal)", err)
			}
		}()
	}

	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[ingestor] ")

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	sources := cfg.ResolvedSources()
	if len(sources) == 0 {
		log.Fatal("no MQTT sources configured — set mqttSources in config or MQTT_BROKER env var")
	}

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()
	log.Printf("SQLite opened: %s", cfg.DBPath)

	// Node retention: move stale nodes to inactive_nodes on startup
	nodeDays := cfg.NodeDaysOrDefault()
	store.MoveStaleNodes(nodeDays)

	// Daily ticker for node retention
	retentionTicker := time.NewTicker(1 * time.Hour)
	go func() {
		for range retentionTicker.C {
			store.MoveStaleNodes(nodeDays)
		}
	}()

	// Periodic stats logging (every 5 minutes)
	statsTicker := time.NewTicker(5 * time.Minute)
	go func() {
		for range statsTicker.C {
			store.LogStats()
		}
	}()

	channelKeys := loadChannelKeys(cfg, *configPath)
	if len(channelKeys) > 0 {
		log.Printf("Loaded %d channel keys for GRP_TXT decryption", len(channelKeys))
	} else {
		log.Printf("No channel keys loaded — GRP_TXT packets will not be decrypted")
	}

	// Connect to each MQTT source
	var clients []mqtt.Client
	for _, source := range sources {
		tag := source.Name
		if tag == "" {
			tag = source.Broker
		}

		opts := mqtt.NewClientOptions().
			AddBroker(source.Broker).
			SetAutoReconnect(true).
			SetConnectRetry(true).
			SetOrderMatters(true)

		if source.Username != "" {
			opts.SetUsername(source.Username)
		}
		if source.Password != "" {
			opts.SetPassword(source.Password)
		}
		if source.RejectUnauthorized != nil && !*source.RejectUnauthorized {
			opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: true})
		} else if strings.HasPrefix(source.Broker, "ssl://") {
			opts.SetTLSConfig(&tls.Config{})
		}

		opts.SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("MQTT [%s] connected to %s", tag, source.Broker)
			topics := source.Topics
			if len(topics) == 0 {
				topics = []string{"meshcore/#"}
			}
			for _, t := range topics {
				token := c.Subscribe(t, 0, nil)
				token.Wait()
				if token.Error() != nil {
					log.Printf("MQTT [%s] subscribe error for %s: %v", tag, t, token.Error())
				} else {
					log.Printf("MQTT [%s] subscribed to %s", tag, t)
				}
			}
		})

		opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
			log.Printf("MQTT [%s] disconnected: %v", tag, err)
		})

		// Capture source for closure
		src := source
		opts.SetDefaultPublishHandler(func(c mqtt.Client, m mqtt.Message) {
			handleMessage(store, tag, src, m, channelKeys, cfg.GeoFilter)
		})

		client := mqtt.NewClient(opts)
		token := client.Connect()
		token.Wait()
		if token.Error() != nil {
			log.Printf("MQTT [%s] connection failed (non-fatal): %v", tag, token.Error())
			continue
		}
		clients = append(clients, client)
	}

	if len(clients) == 0 {
		log.Fatal("no MQTT connections established")
	}

	log.Printf("Running — %d MQTT source(s) connected", len(clients))

	// Wait for shutdown signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down...")
	retentionTicker.Stop()
	statsTicker.Stop()
	store.LogStats() // final stats on shutdown
	for _, c := range clients {
		c.Disconnect(1000)
	}
	log.Println("Done.")
}

func handleMessage(store *Store, tag string, source MQTTSource, m mqtt.Message, channelKeys map[string]string, geoFilter *GeoFilterConfig) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("MQTT [%s] panic in handler: %v", tag, r)
		}
	}()

	topic := m.Topic()
	parts := strings.Split(topic, "/")

	// IATA filter
	if len(source.IATAFilter) > 0 && len(parts) > 1 {
		region := parts[1]
		matched := false
		for _, f := range source.IATAFilter {
			if f == region {
				matched = true
				break
			}
		}
		if !matched {
			return
		}
	}

	var msg map[string]interface{}
	if err := json.Unmarshal(m.Payload(), &msg); err != nil {
		return
	}

	// Skip status/connection topics
	if topic == "meshcore/status" || topic == "meshcore/events/connection" {
		return
	}

	// Status topic: meshcore/<region>/<observer_id>/status
	if len(parts) >= 4 && parts[3] == "status" {
		observerID := parts[2]
		name, _ := msg["origin"].(string)
		iata := parts[1]
		meta := extractObserverMeta(msg)
		if err := store.UpsertObserver(observerID, name, iata, meta); err != nil {
			log.Printf("MQTT [%s] observer status error: %v", tag, err)
		}
		log.Printf("MQTT [%s] status: %s (%s)", tag, firstNonEmpty(name, observerID), iata)
		return
	}

	// Format 1: Raw packet (meshcoretomqtt / Cisien format)
	rawHex, _ := msg["raw"].(string)
	if rawHex != "" {
		decoded, err := DecodePacket(rawHex, channelKeys)
		if err != nil {
			log.Printf("MQTT [%s] decode error: %v", tag, err)
			return
		}

		observerID := ""
		region := ""
		if len(parts) > 2 {
			observerID = parts[2]
		}
		if len(parts) > 1 {
			region = parts[1]
		}

		mqttMsg := &MQTTPacketMessage{Raw: rawHex}
		if v, ok := msg["SNR"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.SNR = &f
			}
		}
		if v, ok := msg["RSSI"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.RSSI = &f
			}
		}
		if v, ok := msg["origin"].(string); ok {
			mqttMsg.Origin = v
		}

		// For ADVERT packets with known coordinates, enforce geo_filter before
		// storing anything — drop the entire message if outside the area.
		if decoded.Header.PayloadTypeName == "ADVERT" && decoded.Payload.PubKey != "" {
			ok, reason := ValidateAdvert(&decoded.Payload)
			if !ok {
				log.Printf("MQTT [%s] skipping corrupted ADVERT: %s", tag, reason)
				return
			}
			if !NodePassesGeoFilter(decoded.Payload.Lat, decoded.Payload.Lon, geoFilter) {
				return
			}
			pktData := BuildPacketData(mqttMsg, decoded, observerID, region)
			isNew, err := store.InsertTransmission(pktData)
			if err != nil {
				log.Printf("MQTT [%s] db insert error: %v", tag, err)
			}
			role := advertRole(decoded.Payload.Flags)
			if err := store.UpsertNode(decoded.Payload.PubKey, decoded.Payload.Name, role, decoded.Payload.Lat, decoded.Payload.Lon, pktData.Timestamp); err != nil {
				log.Printf("MQTT [%s] node upsert error: %v", tag, err)
			}
			if isNew {
				if err := store.IncrementAdvertCount(decoded.Payload.PubKey); err != nil {
					log.Printf("MQTT [%s] advert count error: %v", tag, err)
				}
			}
			// Update telemetry if present in advert
			if decoded.Payload.BatteryMv != nil || decoded.Payload.TemperatureC != nil {
				if err := store.UpdateNodeTelemetry(decoded.Payload.PubKey, decoded.Payload.BatteryMv, decoded.Payload.TemperatureC); err != nil {
					log.Printf("MQTT [%s] node telemetry update error: %v", tag, err)
				}
			}
		} else {
			// Non-ADVERT packets: store normally (routing/channel messages from
			// in-area observers are relevant regardless of relay hop origin).
			pktData := BuildPacketData(mqttMsg, decoded, observerID, region)
			if _, err := store.InsertTransmission(pktData); err != nil {
				log.Printf("MQTT [%s] db insert error: %v", tag, err)
			}
		}

		// Upsert observer
		if observerID != "" {
			origin, _ := msg["origin"].(string)
			if err := store.UpsertObserver(observerID, origin, region, nil); err != nil {
				log.Printf("MQTT [%s] observer upsert error: %v", tag, err)
			}
		}

		return
	}

	// Format 2: Companion bridge channel message (meshcore/message/channel/<n>)
	if strings.HasPrefix(topic, "meshcore/message/channel/") {
		text, _ := msg["text"].(string)
		if text == "" {
			return
		}

		channelIdx := ""
		if len(parts) >= 4 {
			channelIdx = parts[3]
		}
		if ci, ok := msg["channel_idx"]; ok {
			channelIdx = fmt.Sprintf("%v", ci)
		}

		// Extract sender from "Name: message" format
		sender := ""
		if idx := strings.Index(text, ": "); idx > 0 && idx < 50 {
			sender = text[:idx]
		}

		channelName := fmt.Sprintf("ch%s", channelIdx)

		// Build decoded JSON matching Node.js CHAN format
		channelMsg := map[string]interface{}{
			"type":    "CHAN",
			"channel": channelName,
			"text":    text,
			"sender":  sender,
		}
		if st, ok := msg["sender_timestamp"]; ok {
			channelMsg["sender_timestamp"] = st
		}

		decodedJSON, _ := json.Marshal(channelMsg)

		now := time.Now().UTC().Format(time.RFC3339)
		hashInput := fmt.Sprintf("ch:%s:%s:%s", channelIdx, text, now)
		h := sha256.Sum256([]byte(hashInput))
		hash := hex.EncodeToString(h[:])[:16]

		var snr, rssi *float64
		if v, ok := msg["SNR"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		} else if v, ok := msg["snr"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		}
		if v, ok := msg["RSSI"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		} else if v, ok := msg["rssi"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		}

		pktData := &PacketData{
			Timestamp:    now,
			ObserverID:   "companion",
			ObserverName: "L1 Pro (BLE)",
			SNR:          snr,
			RSSI:         rssi,
			Hash:         hash,
			RouteType:    1, // FLOOD
			PayloadType:  5, // GRP_TXT
			PathJSON:     "[]",
			DecodedJSON:  string(decodedJSON),
		}

		if _, err := store.InsertTransmission(pktData); err != nil {
			log.Printf("MQTT [%s] channel insert error: %v", tag, err)
		}

		// Upsert sender as a companion node
		if sender != "" {
			senderKey := "sender-" + strings.ToLower(sender)
			if err := store.UpsertNode(senderKey, sender, "companion", nil, nil, now); err != nil {
				log.Printf("MQTT [%s] sender node upsert error: %v", tag, err)
			}
		}

		log.Printf("MQTT [%s] channel message: ch%s from %s", tag, channelIdx, firstNonEmpty(sender, "unknown"))
		return
	}

	// Format 2b: Companion bridge direct message (meshcore/message/direct/<id>)
	if strings.HasPrefix(topic, "meshcore/message/direct/") {
		text, _ := msg["text"].(string)
		if text == "" {
			return
		}

		sender := ""
		if idx := strings.Index(text, ": "); idx > 0 && idx < 50 {
			sender = text[:idx]
		}

		dm := map[string]interface{}{
			"type":   "DM",
			"text":   text,
			"sender": sender,
		}
		if st, ok := msg["sender_timestamp"]; ok {
			dm["sender_timestamp"] = st
		}

		decodedJSON, _ := json.Marshal(dm)

		now := time.Now().UTC().Format(time.RFC3339)
		hashInput := fmt.Sprintf("dm:%s:%s", text, now)
		h := sha256.Sum256([]byte(hashInput))
		hash := hex.EncodeToString(h[:])[:16]

		var snr, rssi *float64
		if v, ok := msg["SNR"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		} else if v, ok := msg["snr"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		}
		if v, ok := msg["RSSI"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		} else if v, ok := msg["rssi"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		}

		pktData := &PacketData{
			Timestamp:    now,
			ObserverID:   "companion",
			ObserverName: "L1 Pro (BLE)",
			SNR:          snr,
			RSSI:         rssi,
			Hash:         hash,
			RouteType:    1, // FLOOD
			PayloadType:  2, // TXT_MSG
			PathJSON:     "[]",
			DecodedJSON:  string(decodedJSON),
		}

		if _, err := store.InsertTransmission(pktData); err != nil {
			log.Printf("MQTT [%s] DM insert error: %v", tag, err)
		}

		log.Printf("MQTT [%s] direct message from %s", tag, firstNonEmpty(sender, "unknown"))
		return
	}
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// extractObserverMeta extracts hardware metadata from an MQTT status message.
// Casts battery_mv and uptime_secs to integers (they're always whole numbers).
func extractObserverMeta(msg map[string]interface{}) *ObserverMeta {
	meta := &ObserverMeta{}
	hasData := false

	if v, ok := msg["model"].(string); ok && v != "" {
		meta.Model = &v
		hasData = true
	}
	if v, ok := msg["firmware"].(string); ok && v != "" {
		meta.Firmware = &v
		hasData = true
	}
	if v, ok := msg["firmware_version"].(string); ok && v != "" {
		meta.Firmware = &v
		hasData = true
	}
	if v, ok := msg["client_version"].(string); ok && v != "" {
		meta.ClientVersion = &v
		hasData = true
	}
	if v, ok := msg["clientVersion"].(string); ok && v != "" {
		meta.ClientVersion = &v
		hasData = true
	}
	if v, ok := msg["radio"].(string); ok && v != "" {
		meta.Radio = &v
		hasData = true
	}

	if v, ok := msg["battery_mv"]; ok {
		if f, ok := toFloat64(v); ok {
			iv := int(math.Round(f))
			meta.BatteryMv = &iv
			hasData = true
		}
	}
	if v, ok := msg["uptime_secs"]; ok {
		if f, ok := toFloat64(v); ok {
			iv := int64(math.Round(f))
			meta.UptimeSecs = &iv
			hasData = true
		}
	}
	if v, ok := msg["noise_floor"]; ok {
		if f, ok := toFloat64(v); ok {
			meta.NoiseFloor = &f
			hasData = true
		}
	}

	if !hasData {
		return nil
	}
	return meta
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// deriveHashtagChannelKey derives an AES-128 key from a channel name.
// Same algorithm as Node.js: SHA-256(channelName) → first 32 hex chars (16 bytes).
func deriveHashtagChannelKey(channelName string) string {
	h := sha256.Sum256([]byte(channelName))
	return hex.EncodeToString(h[:16])
}

// loadChannelKeys loads channel decryption keys from config and/or a JSON file.
// Merge priority: rainbow (lowest) → derived from hashChannels → explicit config (highest).
func loadChannelKeys(cfg *Config, configPath string) map[string]string {
	keys := make(map[string]string)

	// 1. Rainbow table keys (lowest priority)
	keysPath := os.Getenv("CHANNEL_KEYS_PATH")
	if keysPath == "" {
		keysPath = cfg.ChannelKeysPath
	}
	if keysPath == "" {
		keysPath = filepath.Join(filepath.Dir(configPath), "channel-rainbow.json")
	}

	rainbowCount := 0
	if data, err := os.ReadFile(keysPath); err == nil {
		var fileKeys map[string]string
		if err := json.Unmarshal(data, &fileKeys); err == nil {
			for k, v := range fileKeys {
				keys[k] = v
			}
			rainbowCount = len(fileKeys)
			log.Printf("Loaded %d channel keys from %s", rainbowCount, keysPath)
		} else {
			log.Printf("Warning: failed to parse channel keys file %s: %v", keysPath, err)
		}
	}

	// 2. Derived keys from hashChannels (middle priority)
	derivedCount := 0
	for _, raw := range cfg.HashChannels {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		channelName := trimmed
		if !strings.HasPrefix(channelName, "#") {
			channelName = "#" + channelName
		}
		// Skip if explicit config already has this key
		if _, exists := cfg.ChannelKeys[channelName]; exists {
			continue
		}
		keys[channelName] = deriveHashtagChannelKey(channelName)
		derivedCount++
	}
	if derivedCount > 0 {
		log.Printf("[channels] %d derived from hashChannels", derivedCount)
	}

	// 3. Explicit config keys (highest priority — overrides rainbow + derived)
	for k, v := range cfg.ChannelKeys {
		keys[k] = v
	}

	return keys
}

// Version info (set via ldflags)
var version = "dev"

func init() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("corescope-ingestor", version)
		os.Exit(0)
	}
}
