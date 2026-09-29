// Nested module marker: keeps `go test ./...` and golangci-lint from descending into
// web/node_modules (some npm packages ship Go files). Nothing is built from here.
module github.com/rforced/ostiole/web

go 1.27
