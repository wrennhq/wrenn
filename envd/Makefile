LDFLAGS := -s -w

.PHONY: build clean fmt vet

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o envd .
	@file envd | grep -q "statically linked" || \
		(echo "ERROR: envd is not statically linked!" && exit 1)

clean:
	rm -f envd

fmt:
	gofmt -w .

vet:
	go vet ./...
