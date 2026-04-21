module github.com/corescope/ingestor

go 1.22

require (
	github.com/eclipse/paho.mqtt.golang v1.5.0
	github.com/meshcore-analyzer/geofilter v0.0.0
	github.com/meshcore-analyzer/sigvalidate v0.0.0
	modernc.org/sqlite v1.34.5
)

replace github.com/meshcore-analyzer/geofilter => ../../internal/geofilter

replace github.com/meshcore-analyzer/sigvalidate => ../../internal/sigvalidate

require github.com/meshcore-analyzer/packetpath v0.0.0

replace github.com/meshcore-analyzer/packetpath => ../../internal/packetpath

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.27.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
)
