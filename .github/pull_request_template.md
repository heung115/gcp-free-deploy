## Summary

<!-- What user-facing problem does this pull request solve? -->

## Changes

<!-- List the focused changes and any deliberate non-goals. -->

## Verification

<!-- Check the commands you ran. Add focused tests or manual checks below. -->

- [ ] `gofmt -w *.go`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `terraform fmt -check -diff -recursive`
- [ ] `terraform init -reconfigure -lockfile=readonly -input=false`
- [ ] `terraform validate`

Additional verification:

<!-- Include relevant results. Never paste secrets, project IDs, or Terraform state. -->

## Safety and operations

<!-- Describe cost, IAM, network exposure, state, migration, and cleanup effects. Write "None" where appropriate. -->

- Cost impact:
- IAM impact:
- Network exposure impact:
- Terraform state or migration impact:
- Failure and cleanup behavior:

## Documentation

- [ ] English documentation is updated when user-facing behavior changed.
- [ ] Korean documentation remains accurate when installation, commands, cost, or safety changed.
- [ ] No documentation change is needed.
