.PHONY: build test docs

build:
	go build -o terraform-provider-cursor

test:
	go test ./...

docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 generate --provider-name cursor
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 validate --provider-name cursor
