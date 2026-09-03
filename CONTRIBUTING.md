# Contributing

Thanks for helping improve gcp-free-deploy. Bug reports, documentation fixes, and
focused pull requests are welcome.

## Before opening a change

- Search existing issues first.
- For a behavior change, open an issue describing the user problem and expected
  safety boundary.
- Never include project credentials, access tokens, private Terraform state, or
  unredacted application logs.
- Keep the tool focused on one ephemeral demo VM and one container. Large
  production-platform features may be better suited to a separate project.

## Local development

Requirements:

- Go 1.26.8 or newer
- Terraform 1.9 or newer and earlier than 2.0

Run the fast checks:

```bash
gofmt -w *.go
go test ./...
go vet ./...
terraform fmt -check -diff main.tf
terraform init -reconfigure -lockfile=readonly -input=false
terraform validate
```

Tests should not require a real Google Cloud project. External commands are
isolated behind a runner so deployment behavior can be tested with recorded
results.

## Safety invariants

Changes must preserve these properties unless a proposal explicitly replaces
them with an equally clear boundary:

- Show and save a Terraform plan before applying it.
- Require explicit approval for apply, destroy, and public HTTP exposure.
- Never run shell-expanded user input on the local machine.
- Keep SSH restricted to IAP and keep the default VPC untouched.
- Refuse unknown or ambiguous Terraform state.
- Preserve existing runtime assets rather than overwriting them silently.
- Redact and bound diagnostics before displaying them.
- Leave enough local state after partial failure to support guarded cleanup.

## Pull requests

Keep each pull request scoped to one problem. Explain:

1. what user-facing problem it solves;
2. what changed;
3. how it was tested;
4. any cost, IAM, state, or network implications.

Update the English documentation first and keep the Korean summary accurate when
the change affects installation, commands, cost, or safety.
