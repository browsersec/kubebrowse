ENCORE ?= encore

all: run

# Install dependencies
deps:
	go mod tidy

# Install git hooks using lefthook
hooks:
	@echo "Installing git hooks using lefthook..."
	@if ! command -v lefthook &> /dev/null; then \
		echo "lefthook command not found. Please install lefthook: https://github.com/evilmartians/lefthook"; \
		exit 1; \
	fi
	@lefthook install
	@echo "Lefthook Git hooks installed successfully"

# Run pre-commit checks on all files using lefthook
lint-all:
	@echo "Running pre-commit checks on all files using lefthook..."
	@lefthook run pre-commit --all-files
	@echo "Pre-commit checks completed"

# Run pre-commit checks on staged files only using lefthook
lint:
	@echo "Running pre-commit checks on staged files using lefthook..."
	@lefthook run pre-commit --files $(git diff --name-only --cached)

# Run the Encore application. Encore owns the API server lifecycle.
run:
	$(ENCORE) run

run_frontend:
	@echo "Running frontend..."
	@echo "Starting frontend..."
	bun --cwd frontend run dev

generate:
	bash ./certs/generate.sh

# generate lets encrypt
generate_prod:
	sudo certbot certonly --standalone -d $(DOMAIN) --email $(EMAIL) --agree-tos --non-interactive
	@echo "Certs generated in /etc/letsencrypt/live/$(DOMAIN)/"
	@echo "Copying certs to certs directory..."
	sudo cp /etc/letsencrypt/live/$(DOMAIN)/fullchain.pem certs/prod/certificate.crt
	sudo cp /etc/letsencrypt/live/$(DOMAIN)/privkey.pem certs/prod/private.key

test:
	$(ENCORE) test ./...

test_coverage:
	$(ENCORE) test ./...

build:
	@echo "Encore applications are built and deployed with 'encore deploy'."

# MkDocs documentation targets
docs-setup:
	pip install mkdocs mkdocs-material

docs-serve:
	mkdocs serve

docs-build:
	mkdocs build

# Setup development environment
setup: deps hooks
	@echo "Development environment setup complete"

KIND_CLUSTER ?= kubebrowse-cluster
ARCH := $(shell uname -m)
ifeq ($(ARCH),x86_64)
  PLATFORM := linux/amd64
  TAG_SUFFIX := amd64
else ifneq ($(filter $(ARCH),arm64 aarch64),)
  PLATFORM := linux/arm64
  TAG_SUFFIX := arm64
else
  PLATFORM := linux/$(ARCH)
  TAG_SUFFIX := $(ARCH)
endif
CHROMIUM_IMAGE := ghcr.io/browsersec/rdp-chromium:latest-$(TAG_SUFFIX)
OFFICE_IMAGE := ghcr.io/browsersec/rdp-onlyoffice-lxde:latest-$(TAG_SUFFIX)

# Load chromium image into KIND
load-chromium:
	@echo "Pulling $(CHROMIUM_IMAGE)..."
	docker pull --platform $(PLATFORM) $(CHROMIUM_IMAGE)
	@echo "Loading $(CHROMIUM_IMAGE) into KIND cluster $(KIND_CLUSTER)..."
	docker save $(CHROMIUM_IMAGE) -o /tmp/chromium.tar
	kind load image-archive /tmp/chromium.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/chromium.tar
	@echo "Chromium image loaded."

# Load onlyoffice image into KIND
load-office:
	@echo "Pulling $(OFFICE_IMAGE)..."
	docker pull --platform $(PLATFORM) $(OFFICE_IMAGE)
	@echo "Loading $(OFFICE_IMAGE) into KIND cluster $(KIND_CLUSTER)..."
	docker save $(OFFICE_IMAGE) -o /tmp/onlyoffice.tar
	kind load image-archive /tmp/onlyoffice.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/onlyoffice.tar
	@echo "Onlyoffice image loaded."

# Load all sandbox images into KIND
load-images: load-chromium load-office

help:
	@echo ""
	@echo "Additional Make targets:"
	@echo "  deps         - Refresh Go dependencies"
	@echo "  hooks        - Install lefthook git hooks"
	@echo "  setup        - Set up development environment (deps + hooks)"
	@echo "  lint         - Run lefthook pre-commit checks on staged files"
	@echo "  lint-all     - Run lefthook pre-commit checks on all files"
	@echo "  test         - Run tests"
	@echo "  build        - Show the Encore deployment command"
	@echo "  docs-setup   - Install MkDocs and Material theme via pip"
	@echo "  docs-serve   - Serve documentation locally"
	@echo "  docs-build   - Build documentation into static files"
	@echo "  load-images  - Pull all sandbox images from GHCR and load into KIND"
	@echo "  load-chromium - Pull and load chromium image into KIND"
	@echo "  load-office  - Pull and load onlyoffice image into KIND"
	@echo "  generate     - Generate self-signed certificates"
	@echo "  generate_prod - Generate Let's Encrypt certificates"
