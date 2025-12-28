APP_NAME := can-web
GO       := go

.PHONY: run build clean tidy

## Run the web server (uses vcan0 by default)
run:
	@echo "▶ Running $(APP_NAME)"
	CAN_IFACE?=vcan0 \
	HTTP_ADDR?=127.0.0.1:8080 \
	CAN_MAP?=can_map.csv \
	$(GO) run .

## Build a local binary
build:
	@echo "▶ Building $(APP_NAME)"
	$(GO) build -o $(APP_NAME)

## Tidy go modules
tidy:
	@echo "▶ Tidying go modules"
	$(GO) mod tidy

## Remove built artifacts
clean:
	@echo "▶ Cleaning"
	rm -f $(APP_NAME)
