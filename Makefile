.PHONY: build run test vet

build:
	go build -trimpath -ldflags='-s -w' -o email-mcp .

run: build
	./email-mcp

test:
	go vet ./... && go test ./...

vet:
	go vet ./...
