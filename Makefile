# zkscan — คำสั่งสำหรับ build / ทดสอบ / แพ็กไฟล์
# ใช้: make build | make test | make vet | make dist | make clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# timetzdata ฝังฐานข้อมูลเขตเวลาไว้ในไฟล์โปรแกรม ใช้ได้แม้เครื่อง/คอนเทนเนอร์ไม่มี tzdata
GOFLAGS_BUILD := -trimpath -tags timetzdata -ldflags "$(LDFLAGS)"

.PHONY: build test vet dist clean

build:
	@mkdir -p dist
	go build $(GOFLAGS_BUILD) -o dist/zkscan ./cmd/zkscan

test:
	go test -race ./...

vet:
	go vet ./...

dist:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/zkscan-linux-amd64 ./cmd/zkscan
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS_BUILD) -o dist/zkscan-linux-arm64 ./cmd/zkscan
	@echo "สร้างเสร็จ (เวอร์ชัน $(VERSION)):"
	@ls -l dist/zkscan-linux-amd64 dist/zkscan-linux-arm64

clean:
	rm -rf dist
