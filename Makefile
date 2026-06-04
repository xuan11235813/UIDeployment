# Makefile for lidarControl

# Binary name
BINARY_NAME=lidarControl

# Output directory
BIN_DIR=bin

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Main source file
MAIN_FILE=main.go

.PHONY: all build clean run

all: build

build:
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -o $(BIN_DIR)/$(BINARY_NAME) $(MAIN_FILE)

clean:
	$(GOCLEAN)
	rm -rf $(BIN_DIR)

run: build
	./$(BIN_DIR)/$(BINARY_NAME)

test:
	$(GOTEST) -v ./...

deps:
	$(GOMOD) download
	$(GOMOD) verify