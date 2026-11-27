.PHONY: all run serve test fmt vet clean

all: fmt vet test

run:
	go run ./cmd/simctl

serve:
	go run ./cmd/simctl -serve :8080

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

clean:
	rm -f simctl
