module github.com/corescope/migrate

go 1.22

require (
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/meshcore-analyzer/dbschema v0.0.0
)

replace github.com/meshcore-analyzer/dbschema => ../../internal/dbschema
