# sentinel-go learning-project instructions

## Project purpose

This repository is both a working Go/SRE system and evidence of independent learning. Correct software matters, but the user must also be able to reproduce, test, debug, and explain the core path. The current milestone and exclusions are defined in `..\实习准备-2026\00-总览与当前状态.md` and `..\实习准备-2026\05-sentinel-go项目定义.md`.

## Assistance boundary

When the user asks for explanation, review, hints, or diagnosis, do not edit the core implementation. Inspect the attempt, identify the smallest blocking misconception, and give graduated help. When the user explicitly asks to implement or fix code, make the requested change; clearly distinguish agent-implemented work from user-independent evidence.

Do not introduce a framework, database, cache, message queue, container platform, or abstraction only for resume keywords. Add a dependency when the current milestone requires it and the user can explain the problem it solves. Keep the first HTTP and CRUD milestones standard-library-first unless the user deliberately changes that decision.

## Engineering rules

- Keep changes scoped to the active milestone; preserve user-authored work and commit history.
- Prefer explicit, readable Go over premature abstraction.
- Handle normal and failure paths. Do not claim reliability or performance without a reproducible test.
- Keep README behavior, commands, limitations, and implementation status aligned with code.
- Never include secrets, personal data, or machine-specific credentials.

Before reporting completion, run `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...` when tests exist. If a check does not apply or fails, say why. Do not commit or push unless the user asks.

## Learning evidence

After a core milestone, be ready to ask the user to explain the request/data flow, important API contracts, failure behavior, tradeoffs, and one debugging path. A passing implementation written materially by the agent is valid project progress, but it becomes independent skill evidence only after the user can reproduce or explain the key path and verify it without the original answer.
