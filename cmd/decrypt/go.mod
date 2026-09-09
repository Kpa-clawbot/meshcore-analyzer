module github.com/corescope/decrypt

go 1.22

require (
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/meshcore-analyzer/channel v0.0.0
)

replace github.com/meshcore-analyzer/channel => ../../internal/channel
