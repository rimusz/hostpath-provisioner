#
IMAGE?=public.ecr.aws/arkcase/hostpath-provisioner

TAG_GIT=$(IMAGE):0.4.1
TAG_LATEST=$(IMAGE):latest

PHONY: test-image
test-image: image
	docker build -t hostpath-provisioner -f Dockerfile .

PHONY: all
all: image

PHONY: hostpath-provisioner
hostpath-provisioner: export CGO_ENABLED=0
hostpath-provisioner: export GO111MODULE=on
hostpath-provisioner: $(shell find . -name "*.go")
	go build -a -ldflags '-extldflags "-static"' -o hostpath-provisioner .

PHONY: image
image: hostpath-provisioner
	docker build -t $(TAG_GIT) -f Dockerfile .
	docker tag $(TAG_GIT) $(TAG_LATEST)
