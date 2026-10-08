.PHONY: test build-connect build-connect-dev run-connect run-connect-dev release

test:
	go test ./...

build-connect:
	go build -o connect ./cmd/connect

build-connect-dev:
	go build -tags dev -o connect-dev ./cmd/connect

run-connect:
	go run ./cmd/connect --foreground $(TOKEN)

run-connect-dev:
	go run -tags dev ./cmd/connect --foreground $(TOKEN)

release:
	bash scripts/build-release "$(VERSION)"
