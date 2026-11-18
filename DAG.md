---
name: bashsharp
description: Build/test/lint targets for Bash# as a bashy dag pipeline
---

# Bash# — DAG task file

This DAG file provides the agent-first front door for Bash#, runnable with `bashy dag`:

```bash
bashy dag --list            # available targets
bashy dag test              # run the suite
bashy dag --json test       # machine-readable envelope for an agent
```

## Tasks

### build
Build the library and cmd binaries.
Sources: cmd/, front/, transpile/, godelta/, engine/, go.mod, go.sum
Effects: read

```bash
go build ./...
```

### test
Run the test suite.
Effects: read

```bash
go test ./...
```

### vet
Static checks: gofmt + go vet.
Effects: read

```bash
set -e
gofmt -s -d .
go vet ./...
```

### tidy
go mod tidy + gofmt -s -w . + go vet.
Effects: write

```bash
set -e
go mod tidy
gofmt -s -w .
go vet ./...
```

### clean
go clean.
Effects: destroy

```bash
go clean ./...
```
