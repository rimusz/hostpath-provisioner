#
IMAGE?=quay.io/rimusz/hostpath-provisioner

TAG_GIT=$(IMAGE):v0.2.5
TAG_LATEST=$(IMAGE):latest

PHONY: test-image
test-image:
	docker build -t hostpath-provisioner -f Dockerfile .

PHONY: all
all: image push

PHONY: image
image:
	docker build -t $(TAG_GIT) -f Dockerfile .
	docker tag $(TAG_GIT) $(TAG_LATEST)

PHONY: push
push:
	docker push $(TAG_GIT)
	docker push $(TAG_LATEST)

PHONY: hostpath-provisioner
hostpath-provisioner: export CGO_ENABLED=0
hostpath-provisioner: export GO111MODULE=on
hostpath-provisioner: $(shell find . -name "*.go")
	go build -a -ldflags '-extldflags "-static"' -o hostpath-provisioner .
