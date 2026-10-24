# Reltitle

Anime and film release-title parser command-line tool written in Go.
The CLI prints the complete parse result as JSON.

## Usage

Requires Go 1.22 or newer. The default configuration is embedded in the
binary, so no external files are needed:

```bash
go run ./cmd/reltitle -title "[LoliHouse] Sample Title - 03 [1080p]"
```

The title can also be supplied as a positional argument or through standard input.

```bash
echo "[LoliHouse] Sample Title - 03 [1080p]" | go run ./cmd/reltitle
```

To run against a modified copy of the configuration instead of the embedded
one, point `-config` at its directory:

```bash
./reltitle -config ./configs -title "[LoliHouse] Sample Title - 03 [1080p]"
```

## Build

```bash
go build -o reltitle ./cmd/reltitle
```

For Linux x86-64:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o reltitle ./cmd/reltitle
```

## Test

```bash
go test ./...
```
