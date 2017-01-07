SOURCEDIR=.
SOURCES := $(shell find $(SOURCEDIR) -name '*.go')

BINARY=nagome-webapp_template

.DEFAULT_GOAL: $(BINARY)

$(BINARY): $(SOURCES)
	go build -o ${BINARY}

.PHONY: install
install:
	go install ./...


.PHONY: cross
cross:
	gox -osarch "linux/amd64 darwin/amd64 windows/amd64" -output "release/{{.Dir}}_{{.OS}}_{{.Arch}}"

.PHONY: clean
clean:
	if [ -f ${BINARY} ] ; then rm ${BINARY} ; fi

