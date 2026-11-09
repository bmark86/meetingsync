VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
BINDIR ?= $(HOME)/bin

.PHONY: build install lint test vet vuln check

build:
	mkdir -p bin
	go build -ldflags '-X main.version=$(VERSION)' -o bin/meetingsync .

# Kopieren und Umbenennen statt Überschreiben: ein laufendes Binary wird atomar ersetzt (macOS beendet Prozesse mit geänderter signierter Datei).
install: build
	mkdir -p $(BINDIR)
	cp bin/meetingsync $(BINDIR)/meetingsync.new
	mv -f $(BINDIR)/meetingsync.new $(BINDIR)/meetingsync

lint:
	$(GOLANGCI_LINT) run ./...

test:
	go test -race ./...

vet:
	go vet ./...

vuln:
	$(GOVULNCHECK) ./...

check: vet lint test vuln
