.PHONY: test vet check

test:
	go test ./...

vet:
	go vet ./...

check: vet test
