.PHONY: build test vet fmt run sign docker docker-vips test-vips

build:
	go build -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)" -o bin/ioimg ./cmd/ioimg
	go build -o bin/ioimgctl ./cmd/ioimgctl

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

run:
	IOIMG_ALLOW_ANY_HOST=true IOIMG_ALLOW_PRIVATE=true \
	IOIMG_CACHE_DIR=./cache IOIMG_SIGNING_KEYS=demo:secret go run ./cmd/ioimg

sign:
	go run ./cmd/ioimgctl sign -base http://localhost:8080 \
		-ops 'w_400,f_auto,q_80' -source 'http://127.0.0.1:8099/photo00.jpg' \
		-key demo:secret -exp 1h

docker:
	docker build -t ioimg:dev .

docker-vips:
	docker build -f Dockerfile.vips -t ioimg:vips .

# Runs the vips-tagged test suite inside the build image (needs libvips).
test-vips:
	docker build --target build -f Dockerfile.vips -t ioimg:vipsbuild .
	docker run --rm --entrypoint sh ioimg:vipsbuild -c "cd /src && CGO_ENABLED=1 go test -tags vips ./..."
