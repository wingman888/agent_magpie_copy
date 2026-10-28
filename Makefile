VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
TAGS     = production
TARGETS  = darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

# The GUI links the platform webview through cgo, so it is built natively.
# `nogui` builds the terminal-only dial, which cross-compiles anywhere.
ifeq ($(shell uname -s),Darwin)
  export CGO_CFLAGS  = -mmacosx-version-min=11.0
  export CGO_LDFLAGS = -mmacosx-version-min=11.0
endif

.PHONY: build cli install test app icons release release-cli clean

build:
	go build -tags $(TAGS) -trimpath -ldflags="$(LDFLAGS)" -o dial .

cli:
	CGO_ENABLED=0 go build -tags nogui -trimpath -ldflags="$(LDFLAGS)" -o dial .

install:
	go install -tags $(TAGS) -trimpath -ldflags="$(LDFLAGS)" .

test:
	go vet ./... && go test ./...

# macOS bundle: menu bar app with no Dock icon (LSUIElement).
app: build
	@rm -rf dial.app
	@mkdir -p dial.app/Contents/MacOS dial.app/Contents/Resources
	@cp dial dial.app/Contents/MacOS/dial
	@cp build/darwin/dial.icns dial.app/Contents/Resources/dial.icns
	@sed 's/@VERSION@/$(VERSION)/' build/darwin/Info.plist > dial.app/Contents/Info.plist
	@echo "  dial.app"

icons:
	@go run build/icon/gen.go tray internal/gui/tray.png
	@go run build/icon/gen.go app 64 internal/gui/icon.png
	@rm -rf build/darwin/dial.iconset && mkdir -p build/darwin/dial.iconset
	@for s in 16 32 128 256 512; do \
		go run build/icon/gen.go app $$s build/darwin/dial.iconset/icon_$${s}x$${s}.png; \
		go run build/icon/gen.go app $$((s*2)) build/darwin/dial.iconset/icon_$${s}x$${s}@2x.png; \
	done
	@iconutil -c icns build/darwin/dial.iconset -o build/darwin/dial.icns && rm -rf build/darwin/dial.iconset

release: clean build
	@mkdir -p dist
	@cp dial dist/dial-$(shell go env GOOS)-$(shell go env GOARCH)
	@$(MAKE) --no-print-directory release-cli

release-cli:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ $$os = windows ] && ext=.exe; \
		echo "  $$os/$$arch (cli)"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -tags nogui -trimpath -ldflags="$(LDFLAGS)" -o dist/dial-cli-$$os-$$arch$$ext . ; \
	done

clean:
	rm -rf dial dial.exe dial.app dist
