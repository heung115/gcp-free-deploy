# Cost and Google Cloud Free Tier limits

Last verified against the linked Google Cloud documentation: **2026-09-03**.

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

## External IPv4 pricing is tiered

This tool assigns one ephemeral external IPv4 address so that the deployed HTTP
service can be opened in a browser and checked from outside the VM. Google's VPC
price table lists **$0.005 per address-hour** for an in-use static or ephemeral
IPv4 address on a standard VM. That paid rate is not applied to every hour from
zero, however.

The Billing Pricing API for the `External IP Charge on a Standard VM` SKU
(`C054-7F72-A02E`) exposes a monthly, billing-account-wide zero-price tier before
the paid tier. The threshold can appear as 720 or 744 address-hours depending on
the month and billing context. It often covers most or all of one continuously
attached address. If a 31-day month uses a 720-hour threshold, for example, only
the final 24 hours enter the paid tier. Therefore, multiplying a full month
directly by `$0.005` and presenting `$3.60–$3.72` as the expected charge is
incorrect.

The allowance is shared by the billing account. Two overlapping addresses, an
additional VM, or other consumption of the same SKU can exhaust it sooner; hours
above the account's current threshold use the paid tier. Currency, contract
pricing, credits, and delayed usage attribution can also change the amount shown
on an invoice. Delete the VM when finished so its ephemeral address is released.
Merely stopping a VM can leave other billable resources such as its disk in
place.

Sources:

- [VPC external IP address pricing](https://cloud.google.com/vpc/network-pricing#ipaddress)
- [Get Google Cloud pricing information with the Pricing API](https://cloud.google.com/billing/docs/how-to/get-pricing-information-api)
- [Pricing API tier model](https://cloud.google.com/billing/docs/reference/pricing-api/rest/v1beta/skus.price/get)

## How this repository maps to the profile

| Setting or resource | Project behavior | Cost implication |
| --- | --- | --- |
| `machine_type` | Defaults to `e2-micro`; other x86-64 machine types may be used | Other machine types are outside the Compute Engine Free Tier; Arm-only families are incompatible with the selected image |
| `zone` | Example uses `us-central1-a`; any valid zone is allowed | Only the three documented US regions are eligible |
| Boot disk | Fixed to `pd-standard`; config allows 10–30 GB | Other disks in the billing account count toward the same allowance |
| VM image | Standard Ubuntu 24.04 LTS | Premium OS images are not used |
| External IP | One ephemeral IPv4 using Standard Network Tier | Uses the account-wide monthly address-hour tier; excess usage is billed |
| Snapshots | None created | Snapshots would have separate storage and possible network charges |
| DNS and TLS | Not created | Cloud DNS and managed frontend services would be separate resources |

Standard Network Tier has its own current transfer pricing. Do not treat a
network pricing tier as an extension of the Compute Engine Free Tier guarantee;
check both the Free Program page and the live
[VPC network pricing](https://cloud.google.com/vpc/network-pricing) page.

A small non-zero bill can come from IPv4 hours just beyond the zero-price tier,
outbound traffic just beyond an applicable transfer allowance, overlapping
resources, a partially billed month, taxes, or rounding. In Billing Reports,
group by SKU and inspect `External IP Charge on a Standard VM`, persistent-disk,
instance-core/RAM, and network data transfer rows instead of inferring the cause
from the total alone.

## Before `up`

- Confirm that the project is linked to the intended active billing account.
- Check existing `e2-micro`, disk, external IP, and outbound-transfer usage across
  that billing account.
- Keep `machine_type` set to `e2-micro`.
- Use a zone in `us-west1`, `us-central1`, or `us-east1` if you want the VM to be
  eligible for the current Compute Engine Free Tier.
- Keep total eligible `pd-standard` usage at or below the current account-wide
  allowance.
- Review the Terraform plan instead of using `--auto-approve` on a first run.
- Check whether other external IPv4 addresses already consume the shared monthly
  address-hour tier.
- Set your own reminder or operational expiry: the CLI does not stop or delete
  a deployment automatically.
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
