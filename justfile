# Build the forecast binary to bin/
build:
    go build -o bin/forecast ./cmd/forecast

# Run all Go tests
test:
    go test ./...
