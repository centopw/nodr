# Task 2 report

## Changed files
- `internal/bootstrap/service.go`: added the transactional first-run coordinator, request validation, attempt-local auth and secrets stores, rollback, and completion-state write sequencing.
- `internal/bootstrap/service_test.go`: added success, post-auth state-write compensation/retry, invalid-name, and Git-failure coverage.
- `internal/authn/setup.go`: evolved setup JSON with `workspaceName`, introduced the narrow setup service interface, and emit RFC 9457 `invalid-params` field errors from coordinator validation.
- `internal/authn/store.go`: adapted legacy `Store` to the setup service interface without changing its established setup primitive.
- `internal/authn/setup_test.go`: covered workspace-name forwarding through the handler while preserving legacy handler tests.

## RED proof
`GOTOOLCHAIN=go1.24.13 go test ./internal/bootstrap -run TestServiceSetup` initially failed because `Service` and `Request` did not exist.  
`GOTOOLCHAIN=go1.24.13 go test ./internal/authn -run TestSetupHandlerPassesWorkspaceNameToSetupService` initially failed because `SetupHandler` accepted only `*Store`.

## Final test output
`GOTOOLCHAIN=go1.24.13 go test ./internal/authn ./internal/bootstrap`

```
ok   github.com/centopw/nodr/internal/authn    1.242s
ok   github.com/centopw/nodr/internal/bootstrap    2.366s
```

## Commit
`feat: bootstrap workspace through setup transaction`

## Concerns
The coordinator opens the attempt-local secrets store as required but no initial secret is part of this task's contract. The generated KEK deliberately persists across compensated attempts.
