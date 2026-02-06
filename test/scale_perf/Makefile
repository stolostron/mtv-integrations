SHELL := /usr/bin/env bash

# Build only the upstream simulator binary into ./bin/simulator
#
# Usage:
#   make build
#   make rebuild
#   make clean
#
# Customize:
#   make build SIM_REF=<branch|tag|sha>
#   make build SIM_REPO=https://github.com/<org>/<repo>.git

SIM_REPO   ?= https://github.com/jnpacker/cluster-curator-rollout-controller.git
SIM_REF    ?= main
SIM_SUBDIR ?= test/simulator

VENDOR_DIR ?= $(CURDIR)/_vendor
SIM_SRC    ?= $(VENDOR_DIR)/cluster-curator-rollout-controller

BIN_DIR ?= $(CURDIR)/bin
SIM_BIN ?= $(BIN_DIR)/simulator

.PHONY: build fetch clean rebuild print-vars

build: $(SIM_BIN)

$(SIM_BIN): fetch
	@mkdir -p "$(BIN_DIR)"
	# Upstream test/simulator is a nested Go module (has its own go.mod),
	# so we must build from inside that directory.
	cd "$(SIM_SRC)/$(SIM_SUBDIR)" && go mod tidy
	cd "$(SIM_SRC)/$(SIM_SUBDIR)" && go build -o "$(SIM_BIN)" .
	@echo "Built: $(SIM_BIN)"

fetch:
	@mkdir -p "$(VENDOR_DIR)"
	@if [[ ! -d "$(SIM_SRC)/.git" ]]; then \
		echo "Fetching simulator sources (sparse checkout) into: $(SIM_SRC)"; \
		rm -rf "$(SIM_SRC)"; \
		git clone --depth 1 --filter=blob:none --sparse --branch "$(SIM_REF)" "$(SIM_REPO)" "$(SIM_SRC)"; \
		( cd "$(SIM_SRC)" && git sparse-checkout set "$(SIM_SUBDIR)" ); \
	else \
		echo "Updating simulator sources in: $(SIM_SRC)"; \
		( cd "$(SIM_SRC)" && git fetch --depth 1 origin "$(SIM_REF)" && git checkout -q "$(SIM_REF)" ); \
		( cd "$(SIM_SRC)" && git sparse-checkout set "$(SIM_SUBDIR)" ); \
	fi

clean:
	rm -rf "$(BIN_DIR)" "$(VENDOR_DIR)"

rebuild: clean build

print-vars:
	@echo "SIM_REPO=$(SIM_REPO)"
	@echo "SIM_REF=$(SIM_REF)"
	@echo "SIM_SUBDIR=$(SIM_SUBDIR)"
	@echo "SIM_SRC=$(SIM_SRC)"
	@echo "SIM_BIN=$(SIM_BIN)"
