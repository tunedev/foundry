BIN := $(HOME)/.local/bin/foundry
SKILL := $(HOME)/.claude/skills/foundry-ship

.PHONY: build test install install-skill
build:
	go build -o bin/foundry ./cmd/foundry
test:
	go test ./...
install:
	go build -o $(BIN) ./cmd/foundry
	@echo installed $(BIN)
install-skill:
	mkdir -p $(SKILL)
	cp skills/foundry-ship/SKILL.md $(SKILL)/SKILL.md
	@echo installed $(SKILL)
