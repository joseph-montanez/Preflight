.PHONY: tidy fmt test build

tidy:
	go mod tidy

fmt:
	gofmt -w $$(find . -name '*.go')

test:
	go test ./...

build:
	go build -o godeploy ./cmd/godeploy
