# tierbound worker

Go pgmq consumer that can only propose actions. See `docs/IMPLEMENTATION_PLAN.md` step 1.4.

```bash
go test ./...
go run ./cmd/worker
go tool golangci-lint run ./...
```
