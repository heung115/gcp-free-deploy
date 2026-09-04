# Deploy Docker to Google Cloud Free Tier

[![CI](https://github.com/heung115/gcp-free-deploy/actions/workflows/ci.yml/badge.svg)](https://github.com/heung115/gcp-free-deploy/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/heung115/gcp-free-deploy)](https://github.com/heung115/gcp-free-deploy/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/heung115/gcp-free-deploy)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**gcp-free-deploy** is a safety-first Go CLI for deploying one Docker image or one
public GitHub repository to a Google Cloud Platform (GCP) Compute Engine VM with
Terraform. It creates an isolated network, verifies the container from inside
and outside the VM, and removes the resources through a reviewed destroy plan.
It is intended for disposable demos and learning environments, not production.

[한국어 문서](README.ko.md)

> [!WARNING]
> **Free Tier-compatible does not mean a guaranteed $0 bill.** The default VM,
> region, and disk profile target the Google Cloud Free Tier, but this tool also
> assigns an external IPv4 address. Google's current Billing Pricing API shows a
> monthly zero-price address-hour tier before the **$0.005/hour** paid tier. It
> often covers most or all of one continuously used address, but the allowance
> is pooled across the billing account. Excess address-hours, outbound data,
> taxes, or other resources can still produce a bill. Read
> [Cost and Free Tier limits](docs/costs.md) before applying a plan. Pricing last
> checked: **2026-09-03**.

## Why use it?

- Deploy an explicitly tagged or digest-pinned public Docker image, or build a
  root-level `Dockerfile` from a public GitHub repository.
- Create a dedicated VPC, `/28` subnet, restricted HTTP firewall, IAP-only SSH
  firewall, and one Compute Engine VM.
- Fall back across zones in the same region when `e2-micro` capacity is
  unavailable.
- Verify the startup script, running container, and internal and external HTTP
  health before reporting success.
- Review saved Terraform plans before both creation and deletion.
- Refuse to operate on unknown, legacy, mixed, or ambiguous Terraform state.
- Clean up partially created resources even when VM creation fails.

## Architecture

```text
JSON config
    │
    ▼
Go CLI ───── Terraform ───── Google Cloud
    │                          ├─ dedicated VPC and subnet
    │                          ├─ HTTP and IAP SSH firewalls
    │                          └─ one Compute Engine VM
    │
    └──── IAP SSH and HTTP health verification
```

| Component | Responsibility |
| --- | --- |
| Go CLI | Input validation, execution order, approval, health checks, and safe errors |
| Terraform | Network, firewall, VM, disk, and lifecycle state |
| Docker | Run a public image or build a public repository |
| IAP SSH | Inspect startup without exposing TCP/22 to the internet |

## Requirements

- Go 1.26.8 or newer when installing from source
- Terraform 1.9 or newer and earlier than 2.0
- [Google Cloud CLI](https://cloud.google.com/sdk/docs/install)
- `curl` available on the operator's machine for the external health check
- A Google Cloud project with an active billing account
- Compute Engine API enabled
- Application Default Credentials and an authenticated `gcloud` session

The deploying identity needs permissions to manage Compute Engine instances and
networks, connect through IAP, and use OS Login. Enabling the API also requires
the relevant Service Usage permission. See the
[permissions checklist](docs/troubleshooting.md#permissions-and-apis) instead of
granting broad `Owner` or `Editor` access.

```bash
gcloud auth login
gcloud auth application-default login
gcloud config set project YOUR_PROJECT_ID
gcloud services enable compute.googleapis.com
```

## Install

### With Go

```bash
go install github.com/heung115/gcp-free-deploy@latest
```

Make sure your Go binary directory—usually `$(go env GOPATH)/bin`—is on `PATH`.

### From a release

Download the archive for Linux, macOS, or Windows and `SHA256SUMS` from the
[latest release](https://github.com/heung115/gcp-free-deploy/releases/latest).
Verify the downloaded archive before extracting it. For example, on Linux or
macOS, run the following from the download directory (replace the filename):

```bash
grep 'gcp-free-deploy-v0.2.0-linux-amd64.tar.gz' SHA256SUMS | shasum -a 256 -c -
```

On Windows PowerShell, print the archive hash and compare it with the matching
`SHA256SUMS` entry:

```powershell
(Get-FileHash .\gcp-free-deploy-v0.2.0-windows-amd64.zip -Algorithm SHA256).Hash
```

GitHub build-provenance attestations are published for these release files.
Supported targets are Linux amd64/arm64, macOS amd64/arm64, and Windows amd64.

## Quick start

Use a new empty working directory for each deployment so its local Terraform
state cannot mix with another deployment.

```bash
mkdir my-gcp-deployment
cd my-gcp-deployment

gcp-free-deploy init
cp gcp-free-deploy.example.json gcp-free-deploy.json
```

On Windows PowerShell, use `New-Item -ItemType Directory my-gcp-deployment`,
`Set-Location my-gcp-deployment`, and `Copy-Item` in place of `mkdir`, `cd`, and
`cp`.

Edit `gcp-free-deploy.json`:

```json
{
  "project_id": "your-project-id",
  "zone": "us-central1-a",
  "fallback_zones": ["us-central1-b", "us-central1-c"],
  "source": "docker",
  "docker_image": "nginx:1.30.4",
  "container_port": 80,
  "allowed_source_ranges": ["203.0.113.10/32"],
  "machine_type": "e2-micro",
  "disk_size_gb": 10
}
```

`203.0.113.10/32` is a documentation address and will not give you access.
Replace it with your own public IPv4 address followed by `/32`.

The startup image is Ubuntu 24.04 for x86-64. If you override `machine_type`, it
must support x86-64; Arm-only machine families such as T2A and C4A are not
supported.

Validate locally, review the cloud plan, and deploy:

```bash
gcp-free-deploy validate
gcp-free-deploy up --plan-only
gcp-free-deploy up
```

Type `yes` only after reviewing the Terraform plan. When you are finished,
review and remove every resource managed by this working directory:

```bash
gcp-free-deploy down
```

## Deploy a public GitHub repository

Set `source` to `github`, remove `docker_image`, and provide a public repository
whose default branch has a `Dockerfile` at its root. This is a complete valid
example; replace the placeholder values before using it:

```json
{
  "project_id": "your-project-id",
  "zone": "us-central1-a",
  "fallback_zones": ["us-central1-b", "us-central1-c"],
  "source": "github",
  "github_repo": "https://github.com/example/demo-app.git",
  "container_port": 8080,
  "allowed_source_ranges": ["203.0.113.10/32"],
  "machine_type": "e2-micro",
  "disk_size_gb": 10
}
```

The container must listen on `0.0.0.0`, not only `localhost`, and the
`container_port` setting must match the application's listening port. Private
repositories, non-default branches, subdirectory Dockerfiles, build arguments,
and runtime secrets are not supported. The VM is Linux amd64, so the image and
build output must support that platform. GitHub mode clones once during VM
creation; it is not continuous deployment. To build newer commits from the same
URL, destroy and recreate the deployment.

## Commands

| Command | Effect |
| --- | --- |
| `gcp-free-deploy init` | Write missing embedded Terraform and example files without overwriting existing files |
| `gcp-free-deploy validate` | Validate config and Terraform locally without querying or changing GCP resources |
| `gcp-free-deploy up --plan-only` | Query GCP and create a Terraform plan without applying it |
| `gcp-free-deploy up` | Review, create, and verify the deployment |
| `gcp-free-deploy down` | Review and destroy resources tracked by the local state |
| `gcp-free-deploy version` | Print the CLI version |

Useful options:

```bash
gcp-free-deploy up --config other.json
gcp-free-deploy up --startup-timeout 20m
gcp-free-deploy up --auto-approve
gcp-free-deploy down --auto-approve
```

Startup verification returns as soon as the container and internal and external
HTTP checks succeed. Its default 15-minute limit can be set from 1 minute to 1
hour. At the timeout boundary, the CLI makes one final status and health check;
startup failures may also collect bounded startup, container, and HTTP
diagnostics. Those final steps can take up to 90 additional seconds. The
resources remain running after a timeout until you diagnose them or run
`gcp-free-deploy down`.

To expose plain HTTP to the entire IPv4 internet, use `0.0.0.0/0` in the config
and acknowledge the risk separately. The same acknowledgement is required if
multiple ranges collectively cover all IPv4 addresses:

```bash
gcp-free-deploy up --allow-public-http
```

## Free Tier resource profile

The example intentionally uses the current Compute Engine Free Tier VM and disk
shape:

| Resource | Example | Current Free Tier consideration |
| --- | --- | --- |
| VM | Non-preemptible `e2-micro` | Eligible usage is pooled by billing account and month |
| Region | `us-west1`, `us-central1`, or `us-east1` | Other regions are outside the Compute Engine Free Tier |
| Boot disk | 10 GB `pd-standard` | Up to 30 GB-month across the billing account |
| OS | Ubuntu 24.04 LTS | The selected standard image has no premium OS license charge |
| External IPv4 | Ephemeral, Standard Tier | A monthly account-wide zero-price address-hour tier applies before excess usage is billed |
| Outbound traffic | Application-dependent | Quotas and destination exclusions apply |

The CLI cannot see your billing-account-wide usage, discounts, taxes, existing
VMs, or future pricing changes. It therefore cannot certify a deployment as
free. Review [the full cost checklist](docs/costs.md) and the live Google Cloud
pricing pages before every long-running deployment.

An explicit Docker tag, including `latest`, can still be moved in its registry.
Running `up` again with the same reference does not pull the newer image onto an
existing VM. Use an immutable digest when possible; otherwise run `down` and
recreate the deployment to refresh it.

## Security model

- Uses a dedicated VPC instead of the default VPC.
- Allows SSH only from Google's IAP TCP forwarding range.
- Enables OS Login and blocks project-wide SSH keys.
- Attaches no VM service account or OAuth scopes.
- Requires an explicit flag before HTTP ranges may cover the entire IPv4 internet.
- Redacts common secret patterns and bounds diagnostic output.
- Does not overwrite existing runtime assets or silently migrate state.
- Rejects modified managed Terraform assets and additional top-level Terraform files.

This tool serves **plain HTTP**. Root-owned Docker builds and starts the selected
source; the container process uses the image's configured user, which is root
when the image does not specify one. Use only code and images you trust. Do not
use it for sensitive data or a production service.

## Limitations

- One VM and one container only
- No HTTPS, domain, authentication, high availability, autoscaling, or backup
- No image signature or vulnerability verification
- Health path fixed to `/`
- Local Terraform state with a per-directory process lock, but no remote state or team locking
- No automatic migration of legacy state
- No private registries or GitHub repositories
- Fixed resource names; use only one active deployment per GCP project
- No automatic expiry, stop, or cleanup; resources remain until `down` succeeds
- Container builds can exceed the memory or disk available on an `e2-micro`
- The ephemeral external IP can change after VM replacement or some lifecycle operations

Keep the working directory and its local state until `down` completes. Moving or
deleting the state first can leave resources—and charges—without a safe cleanup
path.

Keep the CLI binary used for an active deployment until cleanup is complete.
Managed Terraform assets are version-bound; if a newer CLI reports an asset
mismatch, use the original binary and files to run `down`. Do not copy only the
state into a freshly initialized directory.

Deployments created by the tagged `v0.1.3` release use a legacy state layout.
Run `down` with the `v0.1.3` binary and its original working directory before
upgrading to `v0.2.0`; the new CLI intentionally will not guess how to migrate
or destroy that legacy state.

## Documentation and support

- [Cost and Free Tier limits](docs/costs.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Architecture and operations decisions](docs/architecture-and-operations.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)

If something fails, check [Troubleshooting](docs/troubleshooting.md) first, then
[open an issue](https://github.com/heung115/gcp-free-deploy/issues/new/choose)
with the CLI version, operating system, failing step, and **redacted** output.

## License

Released under the [MIT License](LICENSE).
