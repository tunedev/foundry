BIN := $(HOME)/.local/bin/foundry
SKILL := $(HOME)/.claude/skills/foundry-ship
UNITS := $(HOME)/.config/systemd/user

.PHONY: build test install install-skill install-timer
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
install-timer:
	mkdir -p $(UNITS)
	cp deploy/systemd/foundry-work.service deploy/systemd/foundry-work.timer $(UNITS)/
	systemctl --user daemon-reload
	@echo "enable with: systemctl --user enable --now foundry-work.timer"
