BINARY := ferry
BINDIR := bin
TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64

.PHONY: build test vet clean

# Cross-compile every supported target to bin/ferry-<goos>-<arch>.
build:
	@mkdir -p $(BINDIR)
	@for target in $(TARGETS); do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		out=$(BINDIR)/$(BINARY)-$$goos-$$goarch; \
		echo "building $$out"; \
		GOOS=$$goos GOARCH=$$goarch go build -o $$out . || exit 1; \
	done

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf $(BINDIR)
