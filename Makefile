.PHONY: build test

build:
	go build -o terraform-provider-cursor

test:
	go test ./...
