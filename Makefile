.PHONY: build run test lint docker-build docker-up docker-logs

build:
	go build -o bin/orb ./cmd/bot

run: build
	./bin/orb

test:
	go test ./...

lint:
	go test ./...

docker-build:
	docker compose build

docker-up:
	docker compose up -d --build

docker-logs:
	docker compose logs -f
