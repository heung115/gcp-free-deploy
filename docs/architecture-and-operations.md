# Architecture and operations decisions

[한국어](architecture-and-operations.ko.md)

## Scope and non-goals

This project creates a disposable Google Cloud VM for one demo application and
checks deployment and failure evidence. The user explicitly removes the
resources afterward with `gcp-free-deploy down` through a reviewed Terraform
destroy plan. It is designed for repeatable, short-lived demonstrations—not
long-term VM management, scheduled patching, high availability, large-scale
operations, or FinOps reporting.

## Why recreate instead of patch

A demo VM should not hold durable user data or long-lived service state. Patching
an existing VM accumulates manual changes and configuration drift; recreating it
from declared Terraform and a startup script gives each run a known starting
point and a defined deletion boundary.

A service that needs persistent data requires separate storage, backups,
migrations, and a patch policy. Those concerns are intentionally outside this
tool's scope.

## Responsibility boundaries

- Terraform defines the dedicated network, subnet, firewall rules, VM, boot
  disk, and outputs.
- The startup script installs Docker, runs exactly one selected application, and
  writes machine-readable status markers during first boot.
- The Go CLI validates JSON input, sequences Terraform commands, manages saved
  plans and approvals, classifies failures, and guards resource deletion.
- The monitor reads structured Terraform outputs, gathers startup, container,
  and internal HTTP evidence through IAP SSH, then performs an external HTTP
  check.
- Documentation states the IAM, network, secret, state, cost, and verification
  boundaries.

These boundaries keep interactive workflow out of Terraform, prevent the CLI
from deleting arbitrary Google Cloud resources directly, and stop the startup
script from becoming an infrastructure manager.

## Network and access decisions

The tool uses a dedicated VPC and subnet. Reusing the default VPC would couple a
deployment to pre-existing firewall rules, making its exposure difficult to
understand from this repository alone.

The demo requires TCP/80 for browser access. Every configuration must declare
the allowed IPv4 CIDRs. `0.0.0.0/0`, or multiple ranges that collectively cover
all IPv4 addresses, requires the separate `--allow-public-http`
acknowledgement. Full coverage exposes unencrypted HTTP to the entire IPv4
internet; a single-address `/32` is the safer default for a personal test.

The tool does not expose SSH directly to the internet. OS Login is enabled,
project-wide SSH keys are blocked, and TCP/22 is allowed only from Google's IAP
TCP forwarding range to a VM-specific network tag. Administrative connections
use `--tunnel-through-iap`. The VM still receives an external IPv4 address
because external HTTP access is part of this demo's design.

First boot needs outbound access to package mirrors and either an image registry
or a public GitHub repository. The deployment relies on the VPC's default egress
behavior and does not implement an egress allowlist. It should therefore not be
treated as a control against data exfiltration or command-and-control traffic
after an application compromise.

## IAM and VM identity

The Terraform identity needs permissions to manage the selected project's
Compute Engine networks, firewalls, and VM. The monitoring identity needs IAP
tunnel and OS Admin Login permissions. Broad project-level `Owner` or `Editor`
roles are not a requirement.

The deployed application does not call Google Cloud APIs. With the Google
provider pinned to 7.12.0 in both the version constraint and lock file,
`service_account { scopes = [] }` is the provider's explicit no-service-account,
no-OAuth-scope configuration (see the pinned
[provider implementation](https://github.com/hashicorp/terraform-provider-google/blob/v7.12.0/google/services/compute/resource_compute_instance.go)).
If Cloud API access is added later, create a dedicated service account and grant
only the necessary roles instead of adding broad scopes or project roles to the
current VM.

## Supply chain and startup behavior

Prefer an immutable Docker digest or, at minimum, an explicit non-`latest` tag.
The CLI permits `latest` but warns about it. GitHub sources must match the public
`https://github.com/<owner>/<repo>` form and are shallow-cloned from their
default branch. The first successful boot writes a completion marker only after
the container passes its internal HTTP check. Later boots restart that existing
container and repeat the health check instead of pulling, cloning, or rebuilding
the source. If initial provisioning fails, no marker is written, so the next
boot removes partial application state and retries provisioning from scratch.

Neither restriction makes the source trusted. The root-owned Docker daemon
builds and starts the source; the container process uses the image's configured
user, which is root when the image omits `USER`. Review the source before
deployment. A missing
`Dockerfile`, failed build, failed pull, or failed container start is reported as
a deployment failure; the tool does not substitute a fallback container that
could hide the original error.

## Success and failure criteria

Startup process completion alone does not prove that the application is usable.
The monitor requires all of the following evidence:

1. `STARTUP_DONE` in the tagged startup log
2. A running application container
3. Successful HTTP checks from inside and outside the VM

When the VM exists but the application fails, the CLI gathers the startup
service status, tagged startup log, container list, bounded container log, and
last health result. Input and Terraform failures use typed failure categories;
runtime failures combine explicit startup markers with observed state.
Diagnostics are size-limited and common secret patterns are masked, but this is
not a guarantee that every possible application log format is fully sanitized.

Startup time depends on VM performance, package mirrors, image registries, and
zone conditions. The monitor polls until the service is ready, then returns
immediately. A configurable wall-clock timeout—15 minutes by default—prevents
the CLI from waiting indefinitely. At the boundary it makes one final check
using a separate context, then collects bounded diagnostics if the deployment
still is not ready. The timeout does not delete resources: the VM and network
can keep accruing charges until the user runs `down`.

## Apply, destroy, and state safety

A plan is the final review boundary before resources change. By default, apply
uses the saved plan and requires the user to type `yes`; unattended approval is
available only with `--auto-approve`. Public HTTP acknowledgement remains a
separate requirement.

Destroy is deliberately stricter. The CLI cross-checks an allowlist of local
state addresses with the actual resource type, name, project, region, and zone
from `terraform show -json` and the protected deployment variables file. It does
not depend on VM outputs, so a partial deployment containing only a network,
subnet, or firewall can still be reviewed and removed. Missing or ambiguous
state fails closed. After destroy, any remaining resource address is reported as
a failure with a cost warning instead of a successful cleanup.

`up` applies the same ownership boundary before planning when local state already
exists. Legacy or unrelated addresses, a non-default workspace, or a mismatch
between the previous deployment and the requested project, region, or zone
stops the run. Deleting state to bypass this check can orphan billable resources.

Local state also creates two important operating limits:

- Run `init`, `validate`, `up`, and `down` from the same working directory. The
  CLI serializes its own commands there, but does not provide remote or team
  state locking.
- Use a separate empty directory for each deployment, but keep only one active
  deployment per Google Cloud project because the managed resource names are
  fixed.

Before any workspace or state command, the CLI runs `terraform init
-reconfigure` against the managed configuration. Because that configuration has
no remote backend block, stale cached backend metadata cannot redirect state
operations away from the working directory.

If state is lost or moved, inspect the real cloud resources and recover or
explicitly migrate state before proceeding. Starting again blindly in a new
directory can cause name collisions while leaving resources unmanaged.

## Release execution model

Release binaries embed the Terraform configuration, provider lock file, and
example configuration. `init`, `validate`, `up`, and `down` materialize only
missing assets in the working directory and never overwrite existing files.
Before planning, applying, or destroying, the CLI rejects a modified managed
asset or an additional top-level Terraform configuration file. This keeps the
documented ownership and cleanup boundary intact. Existing configuration
versions are not migrated automatically.

New deployments require the exact assets embedded in the running release. The
destroy path alone may accept a narrowly reviewed checksum from a compatible
untagged predecessor so an existing deployment can still be removed; arbitrary
or legacy assets remain rejected.

Keep the binary and working directory used for an active deployment until
cleanup completes. If a newer binary rejects a version-bound asset, use the
original binary and files for `down`; copying only the state into a newly
initialized directory is unsafe.

The tagged `v0.1.3` release uses a legacy state layout and is not in that
compatibility set. Destroy `v0.1.3` deployments with the original binary and
working directory before upgrading to `v0.2.0`.

Build targets are limited to platforms supported by the pinned Google provider
7.12.0: Linux amd64/arm64, macOS amd64/arm64, and Windows amd64.

## Cost boundary and production gaps

A small machine type, standard persistent disk, and ephemeral address do not
guarantee a zero-dollar deployment. The Compute Engine Free Tier allowance is
shared across the billing account and applies only to eligible usage.

The external IPv4 address has its own account-wide pricing tier. As last checked
on **2026-09-03**, Google listed the paid tier for an attached static or
ephemeral external IPv4 at **$0.005/hour**, while the Billing Pricing API exposed
a monthly zero-price tier first. Its 720- or 744-address-hour threshold often
covers most or all of one continuously attached address; a 31-day month with a
720-hour threshold leaves only the final 24 hours in the paid tier. Overlapping
addresses or other account-wide usage can push more hours into that tier. See
[Cost and Google Cloud Free Tier limits](costs.md) and verify both the account's
Billing Report and the live
[Google Cloud VPC pricing](https://cloud.google.com/vpc/network-pricing) before a
long-running deployment.

Plan review and prompt destroy are safety practices, not proof of savings. A
production version would also need, at minimum, TLS and a domain, authentication
and secret management, remote state locking and encryption, CI identity
federation, image provenance and vulnerability scanning, a patch/base-image
policy, centralized logs and metrics, backup and restore, configurable health
endpoints, SLOs and alerting, and a rolling-update or high-availability design.

## Verification evidence and limits

Automated coverage includes unit and mock tests plus Go and Terraform static
checks. The following live checks were point-in-time maintainer tests using
isolated local state, not continuous end-to-end monitoring:

- On **2026-08-12**, an `e2-micro` capacity failure in `us-central1` exercised
  same-region zone fallback and cleanup of partial state created before a VM
  existed. A separate `us-west1-a` run created the dedicated VPC and VM, used
  IAP SSH, deployed a Docker image, passed container and internal/external HTTP
  checks, then completed destroy with no managed resources remaining.
- On **2026-08-13**, after startup monitoring was changed to a configurable
  wall-clock limit, the default 15-minute path detected readiness after about
  7 minutes 9 seconds and returned success immediately. An unrelated,
  pre-existing workload in the same project remained running throughout the
  test.

These observations support the disposable demo lifecycle at those dates. They
do not establish continuous availability, production reliability, guaranteed
cost savings, zero downtime, or high availability, and they do not predict
future regional capacity or Google Cloud behavior.
