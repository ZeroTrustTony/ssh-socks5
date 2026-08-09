.PHONY: build build-udp-relay clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o ssh-socks5 ./cmd/ssh-socks5

build-udp-relay:
	$(MAKE) -C distrib/udp-relay build

build-udp-relay-docker:
	chmod +x distrib/udp-relay/build.sh
	./distrib/udp-relay/build.sh

clean:
	rm -f ssh-socks5
	$(MAKE) -C distrib/udp-relay clean
