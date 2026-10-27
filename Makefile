.PHONY: build clean deps fuzz install uninstall ui dev-ui diamtest

BINARY=hss
APP_VERSION=1.3.0
API_VERSION=1.4.2
VERSION_PKG=github.com/svinson1121/vectorcore-hss/internal/version
GO_LDFLAGS=-X $(VERSION_PKG).AppVersion=$(APP_VERSION) -X $(VERSION_PKG).APIVersion=$(API_VERSION)
FUZZ_TIME?=30s
PREFIX=/opt/vectorcore
BINDIR=$(PREFIX)/bin
ETCDIR=$(PREFIX)/etc
LOGDIR=$(PREFIX)/log
SYSTEMD=/lib/systemd/system/

build: ui deps
	mkdir -p bin
	go build -ldflags "$(GO_LDFLAGS)" -o bin/$(BINARY) ./cmd/hss

ui: ## Build the React UI (requires Node.js / npm)
	cd web && npm install && npm run build

dev-ui: ## Start Vite dev server (proxies API to localhost:8080)
	cd web && npm install && npm run dev

diamtest: ## Build the Diameter test/load client (air/ulr/pur/suite/load/storm)
	mkdir -p bin
	go build -o bin/diamtest ./cmd/diamtest

deps:
	go mod tidy

fuzz:
	go test ./internal/gsup -run '^$$' -fuzz '^FuzzDecode$$' -fuzztime=$(FUZZ_TIME)
	go test ./internal/gsup -run '^$$' -fuzz '^FuzzParseIDResp$$' -fuzztime=$(FUZZ_TIME)
	go test ./internal/gsup -run '^$$' -fuzz '^FuzzIMSIRoundTrip$$' -fuzztime=$(FUZZ_TIME)
	go test ./internal/udm -run '^$$' -fuzz '^FuzzParseSUPI$$' -fuzztime=$(FUZZ_TIME)

install: build
	install -d $(BINDIR)
	install -d $(ETCDIR)
	install -d $(LOGDIR)

	install -m755 bin/$(BINARY) $(BINDIR)/$(BINARY)

	if [ ! -f $(ETCDIR)/hss.yaml ]; then \
		install -m644 config/hss.yaml $(ETCDIR)/hss.yaml; \
	fi

	touch $(LOGDIR)/hss.log
	chmod 644 $(LOGDIR)/hss.log

	install -m644 systemd/vectorcore-hss.service $(SYSTEMD)/vectorcore-hss.service

	systemctl daemon-reload
	systemctl enable vectorcore-hss
	systemctl start vectorcore-hss

clean:
	rm -rf bin/

uninstall:
	systemctl stop vectorcore-hss || true
	systemctl disable vectorcore-hss || true

	rm -f $(BINDIR)/$(BINARY)
	rm -f $(SYSTEMD)/vectorcore-hss.service

	systemctl daemon-reload
