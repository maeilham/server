GOOS   = linux
GOARCH = amd64
OUTDIR = bin

.PHONY: all build clean test

all: build

build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(OUTDIR)/maeilham ./cmd/maeilham/

clean:
	rm -rf $(OUTDIR)

test:
	go test ./...
