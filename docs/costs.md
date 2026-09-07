# Cost and Google Cloud Free Tier limits

IPv4 and network-transfer sources rechecked: **2026-09-07**. This date does not
mean that every linked product price was reverified.

This project is Free Tier-aware, not a guarantee of a zero-dollar bill. Google
Cloud pricing, your billing-account-wide usage, taxes, credits, and discounts are
outside the CLI's control. Review the live pricing pages before each long-running
deployment.

## Compute Engine Free Tier profile

Google currently documents the following monthly Compute Engine Free Tier
allowance:

- Non-preemptible `e2-micro` usage equal to one instance-month, pooled across
  `us-west1`, `us-central1`, and `us-east1`
- 30 GB-month of `pd-standard` persistent disk
- 1 GB of outbound data transfer from North America to destinations other than
  China and Australia

The allowance is calculated across the billing account, not separately for each
project or VM. Running two eligible VMs for half a month can consume the same VM
allowance as one eligible VM for the full month. Unused allowance does not roll
over.

Sources:

- [Google Cloud Free Program](https://cloud.google.com/free/docs/free-cloud-features)
- [E2 machine specifications](https://cloud.google.com/compute/docs/general-purpose-machines)
- [Disk and image pricing](https://cloud.google.com/compute/disks-image-pricing)

## External IPv4: pricing API reports 720 free address-hours

This tool assigns one ephemeral external IPv4 address for public HTTP access.
On **2026-09-07**, the public pricing API for `External IP Charge on a Standard VM`
(`C054-7F72-A02E`) returned **720 address-hours per account per month at $0**, then
**$0.005 per address-hour**. An account-specific pricing response returned the
same list and contract prices with `default-price`. Units were hours, quantity
one, with account-level monthly aggregation. See the [observation record](evidence/ipv4-sku-2026-09-07.md).

For one continuously used address, if the same tier applies throughout the
period and no other usage consumes the shared allowance, 30 days (720 hours)
costs **$0** and 31 days (744 hours) costs **$0.12**, before credits and taxes.
The observed threshold is **720 hours**, not a variable 720/744-hour allowance.

The VPC pricing page still lists **one free hour per month per account**, whereas
the SKU catalog displays **0 month to 1 month**. The API resolves the numeric
threshold of the observed prices as 720 hours; the VPC page discrepancy remains.
An account-specific price response is not an actual bill or a record of consumed
allowance. Confirm billed usage and credits in Cloud Billing. The current Free
Program page does not state a separate `e2-micro` external IPv4 exemption.

Shared account usage, contract pricing, currency, credits, and delayed usage
attribution can change the invoice. Deleting the VM releases its ephemeral IP;
stopping the VM can leave billable storage. Standard Network Tier does not
change the IPv4 unit price or provide a separate IP allowance.

Sources:

- [Public pricing API: C054-7F72-A02E](https://cloudbilling.googleapis.com/v1beta/skus/C054-7F72-A02E/price?currencyCode=USD)
- [VPC external IP address pricing](https://cloud.google.com/vpc/network-pricing#ipaddress)
- [Public SKU catalog: C054-7F72-A02E](https://cloud.google.com/skus?currency=USD&filter=C054-7F72-A02E)
- [Google Cloud Free Program](https://docs.cloud.google.com/free/docs/free-cloud-features)

## Traffic: Standard Tier has a separate allowance

The Terraform template sets `network_tier = "STANDARD"`. Internet traffic entering
the VM is free under the network transfer pricing; data the VM sends to internet
clients is outbound traffic. Serving pages, API responses, and downloads consumes
outbound bytes. Downloading an image into the VM mainly consumes inbound bytes;
requests and protocol traffic sent by the VM still travel outbound.

For this project's US source regions, Standard Tier pricing lists **the first
200 GiB per month per account free**, then **$0.085/GiB** for the portion above
200 GiB through **10,240 GiB total monthly usage**. The VPC pricing page says the
free allowance is combined across all regions, so do not budget 200 GiB for each
VM, project, or region. For example, 250 GiB of qualifying monthly outbound usage
costs `(250 - 200) × $0.085 = $4.25`, before other charges and credits, assuming
no other usage consumes the shared allowance.

The Network Service Tiers pricing page explicitly says the Compute Engine
Always Free transfer allowance does not apply to Standard Tier. **Do not add
its 1 GB to the 200 GiB allowance.** Premium Tier has different rates and rules;
the actual VM must use Standard to claim its allowance. A template edit alone
does not change existing resources. GiB means 2^30 bytes.

There is also a documentation discrepancy: the Network Service Tiers overview
says 200 GB per region/per SKU, while the VPC pricing page says 200 GiB across
regions. Use the latter, more conservative pooling assumption for planning and
verify account-level usage before expanding across regions.

Sources:

- [Network Service Tiers pricing](https://cloud.google.com/network-tiers/pricing)
- [VPC network pricing](https://cloud.google.com/vpc/network-pricing)
- [Network Service Tiers overview](https://docs.cloud.google.com/network-tiers/docs/overview)

## How this repository maps to the profile

Run `gcp-free-deploy cost` (or `gcp-free-deploy cost --config other.json`) for an
offline, read-only configuration report. It requires no Terraform, `gcloud`, or
authentication, creates no files, and returns an error for a machine other than
`e2-micro` or a region outside `us-west1`, `us-central1`, and `us-east1`. The
existing config validation limits disks to 10–30 GB. The report does not inspect
Terraform state, live resources, bills, account eligibility, or usage; passing
the check does not guarantee a zero bill.

`up` blocks an out-of-profile configuration before runtime asset preparation,
external tool checks, or cloud access. Intentional deployments outside this
profile require `--allow-paid-resources`; `--auto-approve` does not bypass the
guard. `up --plan-only` is allowed without the override so you can review the
plan. The guard does not affect `down` for existing out-of-profile deployments;
the usual state and managed-asset safety checks still apply.

For existing resources, use authenticated `gcloud` through:

```bash
gcp-free-deploy audit --project YOUR_PROJECT_ID --vm YOUR_VM_NAME --zone us-central1-a
```

All three target flags are required. No Terraform state is needed, and no cloud
resources are changed. The audit reads the actual VM/network tier, project disks,
and VM count. Premium, missing required information, or resources outside the
checked profile cause an error. Multiple VMs produce a caution because account
VM-hour consumption is not known. It checks present allocation, not historical
usage, invoices, other projects, or other services.

`up` verifies actual Standard networking before health checks and success unless
`--allow-paid-resources` is specified. Verification failure leaves the resources
in place; inspect them with `audit` and resolve or clean up the deployment.

A legacy empty `access_config {}` defaults to Premium. Updating repository code
does not migrate an existing VM. Keep each deployment’s original CLI, `main.tf`,
and working directory for cleanup: this release changes the runtime template,
and existing managed files are not overwritten automatically. If an asset
mismatch blocks `down`, use the original binary. Do not apply a new template to
legacy state merely to change the network tier. See the
[free-first operations guide (한국어)](free-first-operations.ko.md).

| Setting or resource | Project behavior | Cost implication |
| --- | --- | --- |
| `machine_type` | Defaults to `e2-micro`; deploying other supported x86-64 types requires `--allow-paid-resources` | Other machine types are outside the Compute Engine Free Tier; Arm-only families are incompatible with the selected image |
| `zone` | Example uses `us-central1-a`; deploying outside the three profile regions requires `--allow-paid-resources` | Only the three documented US regions are eligible |
| Boot disk | Fixed to `pd-standard`; config allows 10–30 GB | Other disks in the billing account count toward the same allowance |
| `max_runtime_hours` | Optional `0` (unlimited) or `1`–`168`; examples recommend `24` | Stops the VM after each start interval; disk remains, and restarting resets the interval |
| VM image | Standard Ubuntu 24.04 LTS | Premium OS images are not used |
| External IP | One ephemeral IPv4 using Standard Network Tier | Pricing API observed 720 address-hours/month/account free, then $0.005/hour; VPC page still differs |
| Snapshots | None created | Snapshots would have separate storage and possible network charges |
| DNS and TLS | Not created | Cloud DNS and managed frontend services would be separate resources |

A non-zero bill can come from IPv4, outbound traffic beyond the applicable shared
allowance, overlapping resources, taxes, or rounding. In Billing Reports,
group by SKU and inspect `External IP Charge on a Standard VM`, persistent-disk,
instance-core/RAM, and network data transfer rows instead of inferring the cause
from the total alone.

## Before `up`

- Run `gcp-free-deploy cost` to check the configuration's VM, region, and disk profile.
- Confirm that the project is linked to the intended active billing account.
- Check existing `e2-micro`, disk, external IP, and outbound-transfer usage across
  that billing account.
- Keep `machine_type` set to `e2-micro`.
- Use a zone in `us-west1`, `us-central1`, or `us-east1` if you want the VM to be
  eligible for the current Compute Engine Free Tier.
- Keep total eligible `pd-standard` usage at or below the current account-wide
  allowance.
- Review the Terraform plan instead of using `--auto-approve` on a first run.
- Verify the external IPv4 SKU allowance actually applied to your account and
  include other addresses sharing it.
- Set `max_runtime_hours` to a short interval such as `24` for disposable demos.
  Automatic stop retains the disk and does not cap traffic or the account bill.
  Omission or `0` disables the runtime limit; restarting begins a new interval.
- Create a billing budget or alert if useful, while remembering that a standard
  budget is an alert and **does not automatically cap Compute Engine spending**.

Sources:

- [Create and manage budgets](https://cloud.google.com/billing/docs/how-to/budgets)
- [Cloud Billing account requirements](https://cloud.google.com/billing/docs/how-to/create-billing-account)

## After testing

Run `gcp-free-deploy down` from the same working directory that contains the
deployment's `terraform.tfstate` and `.gcp-free-deploy.tfvars.json`. Review the
destroy plan and keep the final confirmation that no managed resources remain.

Then check the Google Cloud console's billing report and Compute Engine pages.
Billing data can be delayed, so a zero current total immediately after deletion
is not proof that no charge was incurred.

## Why the CLI cannot certify “free”

The CLI validates the requested resource shape, but it cannot reliably determine:

- usage by other projects on the same billing account;
- accumulated VM hours, disk GB-months, or outbound traffic;
- credits, negotiated pricing, currency conversion, or taxes;
- delayed billing data;
- future Google Cloud pricing or Free Tier changes.

Google states that Free Tier terms can change with advance notice. Treat the
links above—not this repository—as the source of truth for current pricing.


## Free email cost alerts

Normal `up` runs set up alerts automatically after deployment approval and before
Terraform apply (also when verifying an unchanged deployment). The app discovers
the linked billing account and signed-in email and prepares the required APIs,
email channel and budget. Setup failures stop the deployment; existing resources
and any alert settings already created remain. Plan-only/cancelled runs don't
change alerts. `--skip-budget-alerts` explicitly opts out for separately managed
environments. `down` retains alerts for delayed charges. The command below is for
manual budget administration.

Run `gcp-free-deploy budget --project PROJECT --billing-account ACCOUNT --enable-api`
to create a project-scoped monthly budget, inclusive of all credits. The default
amount is **1 unit of the billing account currency**, with actual-spend thresholds
at 1%, 10%, and 100%. Use `--amount` to change the amount and `--dry-run` to inspect
without changes. The managed budget is reused only when its settings match;
existing differing budgets are never overwritten. API activation is opt-in.

Default email recipients are billing account administrators/users, not necessarily
the signed-in user or project owner. Use `--notification-channel
projects/PROJECT/notificationChannels/ID` with an existing Monitoring email channel
for a specific recipient. Budget APIs and email budget alerts are
[free](https://cloud.google.com/billing/v1/pricing); this command creates no metric
alert policy, Pub/Sub, Function, or BigQuery export. Credits may offset charges
and prevent alerts. Notifications and cost reporting can be delayed; these alerts
are not a spending cap or proof of zero charges. See the
[Korean setup guide](free-first-operations.ko.md#무료-이메일-비용-알림).
