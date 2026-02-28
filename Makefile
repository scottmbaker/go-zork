PUBLISH_REPOSITORY             ?= smbaker
PUBLISH_REGISTRY               ?= docker.io
CHART_NAME                     ?= go-zork
DOCKER_VERSION                 ?= 1.0.0
DOCKER_TAG                     ?= $(PUBLISH_REGISTRY)/$(PUBLISH_REPOSITORY)/$(CHART_NAME):$(DOCKER_VERSION)

ifeq ($(OS),Windows_NT)
	EXE := .exe
else
	EXE :=
endif

.PHONY: all clean play walkthrough test docker

all: _build/zork$(EXE) _build/web$(EXE) _build/mcp$(EXE)

_build:
	mkdir -p _build

_build/zork$(EXE): cmd/zork/main.go | _build
	go build -o _build/zork$(EXE) ./cmd/zork

_build/web$(EXE): cmd/web/main.go cmd/web/terminal.html cmd/web/chat.html | _build
	go build -o _build/web$(EXE) ./cmd/web

_build/mcp$(EXE): cmd/mcp/main.go | _build
	go build -o _build/mcp$(EXE) ./cmd/mcp

test: _build/zork$(EXE)
	./_build/zork$(EXE) -seed 372 zork1.dat < walkthrough.txt > walthrough_output.txt
	grep "Your score is 350" walthrough_output.txt

walkthrough: _build/zork$(EXE)
	./_build/zork$(EXE) -seed 372 zork1.dat < walkthrough.txt

play: _build/zork$(EXE)
	./_build/zork$(EXE) zork1.dat

docker-build: ## Build docker image
	docker build -t $(PUBLISH_REPOSITORY)/$(CHART_NAME) .

docker-push: ## push helm chart to dockerhub
	docker tag $(PUBLISH_REPOSITORY)/$(CHART_NAME) $(DOCKER_TAG)
	docker push $(DOCKER_TAG)

clean:
	rm -rf _build *.o
