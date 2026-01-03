VERSION ?= $(shell cat ./VERSION)
MAJOR_VERSION ?= $(shell echo $(VERSION) | cut -d . -f1)
GO_IMAGE := golang:1.25-alpine
GO_RUN := docker run --rm -e CGO_ENABLED=0 -e HOME=$$HOME -v $$HOME:$$HOME -u $(shell id -u):$(shell id -g) -v $(shell pwd):/build -v /tmp:/tmp -v /var/run/docker.sock:/var/run/docker.sock -w /build $(GO_IMAGE) go
GO_RUN_TEST := docker run --rm --network host -e CGO_ENABLED=0 -v $(shell pwd):/build -v /tmp:/tmp -v /var/run/docker.sock:/var/run/docker.sock -w /build $(GO_IMAGE) go test
GO_FILES := $(shell find . -type f -path **/*.go -not -path "./vendor/*")
PACKAGES := $(shell go list ./...)
DOCKER_GID := $(shell getent group docker | cut -d: -f3)
IMAGE_NAME := frozengoats/evo

.PHONY: test
test:
	$(GO_RUN_TEST) -p 1 --timeout 10m $(PACKAGES)

.PHONY: lint-check
lint-check:
	docker run -t --rm -v $(shell pwd):/app -w /app golangci/golangci-lint:v2.1.1 golangci-lint run

.PHONY: build
build: build-docker

.PHONY: build-docker
build-docker: bin/evo
	docker build -t $(IMAGE_NAME):$(VERSION) .

.PHONY: publish
publish: build
	docker push $(IMAGE_NAME):$(VERSION)
	docker tag $(IMAGE_NAME):$(VERSION) $(IMAGE_NAME):$(MAJOR_VERSION)
	docker push $(IMAGE_NAME):$(MAJOR_VERSION)

bin/evo: $(GO_FILES)
	$(GO_RUN) build -trimpath -ldflags="-s -w -X 'main.Version=$(VERSION)'" -mod=vendor -o ./bin/evo main.go

.PHONY: install
install: bin/evo
	sudo cp ./bin/evo /usr/local/bin/evo

.PHONY: clean
clean:
	rm -rf bin
