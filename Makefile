.PHONY: build build-udp-relay clean docker-build docker-buildx

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o ssh-socks5 ./cmd/ssh-socks5

build-udp-relay:
	$(MAKE) -C distrib/udp-relay build

build-udp-relay-docker:
	chmod +x distrib/udp-relay/build.sh
	./distrib/udp-relay/build.sh

# Build a single-arch image for the local machine.
IMAGE ?= ssh-socks5
docker-build:
	docker build -t $(IMAGE) .

# Build (and optionally push) a multi-arch image for amd64 + arm64.
# Requires Docker Buildx. Set PUSH=1 and IMAGE=<registry>/<name>:<tag> to push.
# Without PUSH the image is built for both arches but not loaded (buildx cannot
# load multi-arch images into the local docker store).
PLATFORMS ?= linux/amd64,linux/arm64
docker-buildx:
	docker buildx build --platform $(PLATFORMS) -t $(IMAGE) $(if $(filter 1,$(PUSH)),--push,) .

clean:
	rm -f ssh-socks5
	$(MAKE) -C distrib/udp-relay clean
