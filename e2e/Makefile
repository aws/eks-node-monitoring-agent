.PHONY: build
build:
	go test -c -tags=e2e -o ../bin/e2e.test .

.PHONY: test
test:
	go test -v -tags=e2e ./... --install=true --image=$(IMAGE)
