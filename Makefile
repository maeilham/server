GOOS   = linux
GOARCH = amd64
OUTDIR = bin

.PHONY: all server cron clean

all: server cron

server:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(OUTDIR)/server ./cmd/server/

cron:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(OUTDIR)/cron ./cmd/cron/

clean:
	rm -rf $(OUTDIR)
