.PHONY: check-source test build smoke dist release clean

VERSION ?= 0.6.1
LDFLAGS := -s -w

check-source:
	test -s cmd/cos/main.go

test: check-source
	go test ./...
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o cos ./cmd/cos

smoke: build
	python3 scripts/e2e.py ./cos
	python3 scripts/control_plane_e2e.py ./cos
	python3 scripts/multiclient_e2e.py ./cos

dist:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='$(LDFLAGS)' -o dist/cos-linux-amd64 ./cmd/cos
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='$(LDFLAGS)' -o dist/cos-linux-arm64 ./cmd/cos

release: check-source test smoke dist
	cd dist && sha256sum cos-linux-amd64 cos-linux-arm64 > SHA256SUMS

clean:
	rm -f cos dist/cos-linux-amd64 dist/cos-linux-arm64
