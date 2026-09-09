module github.com/corescope/server

go 1.22

require (
	github.com/gorilla/mux v1.8.1
	github.com/gorilla/websocket v1.5.3
	github.com/meshcore-analyzer/geofilter v0.0.0
	github.com/meshcore-analyzer/sigvalidate v0.0.0
)

replace github.com/meshcore-analyzer/geofilter => ../../internal/geofilter

replace github.com/meshcore-analyzer/sigvalidate => ../../internal/sigvalidate

require github.com/meshcore-analyzer/packetpath v0.0.0

replace github.com/meshcore-analyzer/packetpath => ../../internal/packetpath

require github.com/meshcore-analyzer/dbconfig v0.0.0

replace github.com/meshcore-analyzer/dbconfig => ../../internal/dbconfig

require github.com/meshcore-analyzer/perfio v0.0.0

replace github.com/meshcore-analyzer/perfio => ../../internal/perfio

require github.com/meshcore-analyzer/dbschema v0.0.0

replace github.com/meshcore-analyzer/dbschema => ../../internal/dbschema

require github.com/meshcore-analyzer/lora v0.0.0

replace github.com/meshcore-analyzer/lora => ../../internal/lora

require github.com/meshcore-analyzer/prunequeue v0.0.0

replace github.com/meshcore-analyzer/prunequeue => ../../internal/prunequeue

require (
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/meshcore-analyzer/mbcapqueue v0.0.0
	golang.org/x/sync v0.10.0
)

replace github.com/meshcore-analyzer/mbcapqueue => ../../internal/mbcapqueue
