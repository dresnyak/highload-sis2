.PHONY: gen build test vet fmt up down run-task run-group

gen:
	protoc -I proto \
		--go_out=gen --go_opt=paths=source_relative \
		--go-grpc_out=gen --go-grpc_opt=paths=source_relative \
		proto/tasktracker/v1/*.proto

build:
	go build ./...

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal

up:
	docker compose up -d --build

down:
	docker compose down

run-task:
	go run ./cmd/task-service

run-group:
	go run ./cmd/group-service
