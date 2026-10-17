.PHONY: build test worker orchestrator

build:
	go build -o bin/worker ./cmd/worker
	go build -o bin/orchestrator ./cmd/orchestrator

test:
	go vet ./...
	go test ./...

# Run each in its own terminal (after `make build`).
worker:
	nono run --profile ./policies/worker.json --allow-cwd --credential gemini -- ./bin/worker

orchestrator:
	nono run --profile ./policies/orchestrator.json --allow-cwd --credential gemini -- ./bin/orchestrator
