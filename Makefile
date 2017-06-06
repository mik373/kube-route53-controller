default: build

deps:
	go get github.com/ericchiang/k8s
	go get github.com/aws/aws-sdk-go
	go get github.com/ghodss/yaml

build: deps
	GODEBUG=netdns=cgo CGO_ENABLED=0 GOOS=linux go build -a -tags netgo -ldflags '-w' -o healthcheck .
