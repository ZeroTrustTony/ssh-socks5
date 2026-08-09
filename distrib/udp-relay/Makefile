.PHONY: build clean build-docker

BINARY := ssh-socks5-udp-relay

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) .

build-docker:
	chmod +x build.sh
	./build.sh

clean:
	rm -f $(BINARY)
