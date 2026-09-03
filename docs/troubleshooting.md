# Troubleshooting

Start by recording versions and validating the local files:

```bash
gcp-free-deploy version
terraform version
gcloud version
gcp-free-deploy validate
```

Do not paste access tokens, credential files, private keys, full Terraform state,
or unreviewed application logs into a public issue.

## Permissions and APIs

The deployment identity needs permissions equivalent to these focused roles:

- Compute Instance Admin (v1): `roles/compute.instanceAdmin.v1`
- Compute Network Admin: `roles/compute.networkAdmin`
- IAP-Secured Tunnel User: `roles/iap.tunnelResourceAccessor`
- Compute OS Admin Login: `roles/compute.osAdminLogin`

Enabling `compute.googleapis.com` additionally requires the relevant Service
Usage permission, commonly provided by `roles/serviceusage.serviceUsageAdmin`.
An administrator can enable the API once without permanently granting that role
to the deployment user.

Exact organizational policies differ. Prefer custom or predefined least-privilege
roles over project-wide `Owner` or `Editor` access.

```bash
gcloud config set project YOUR_PROJECT_ID
gcloud services enable compute.googleapis.com
gcloud services list --enabled --filter='name:compute.googleapis.com'
```

## No Application Default Credentials

Terraform uses Application Default Credentials (ADC), while the CLI also uses
your regular `gcloud` session for IAP SSH. Configure both:

```bash
gcloud auth login
gcloud auth application-default login
gcloud auth list
gcloud auth application-default print-access-token >/dev/null
```

In Windows PowerShell, replace the last line with:

```powershell
gcloud auth application-default print-access-token | Out-Null
```

If you intentionally use another ADC source, such as workload identity, do not
replace it with a personal login merely to satisfy this example.

## The Compute Engine API is disabled

Typical Terraform messages mention `accessNotConfigured`, `SERVICE_DISABLED`, or
`Compute Engine API has not been used`. Enable the API, wait briefly for the
change to propagate, and rerun the plan:

```bash
gcloud services enable compute.googleapis.com --project YOUR_PROJECT_ID
gcp-free-deploy up --plan-only
```

## IAP SSH never connects

Confirm all of the following:

1. Your identity has both IAP tunnel and OS Login permissions.
2. The VM exists and startup has had time to install SSH prerequisites.
3. An organization policy is not blocking IAP or OS Login.
4. Your local `gcloud` session targets the same account and project as Terraform.

Inspect the generated VM without opening TCP/22 to the public internet:

```bash
gcloud compute ssh gcp-free-deploy-demo \
  --project YOUR_PROJECT_ID \
  --zone YOUR_ZONE \
  --tunnel-through-iap
```

Do not “fix” IAP problems by adding `0.0.0.0/0` to the SSH firewall.

## Zone capacity is exhausted

`e2-micro` can be unavailable in a particular zone even when your project still
has quota. Put alternative zones from the **same region** in `fallback_zones`.
The CLI retries only failures it can classify as capacity exhaustion.

```json
{
  "zone": "us-central1-a",
  "fallback_zones": ["us-central1-b", "us-central1-c"]
}
```

Zones from another region are rejected because the deployment uses one regional
subnet. Quota does not guarantee physical capacity.

## The external HTTP health check fails

The shipped `203.0.113.10/32` value is a documentation-only TEST-NET address. An
unchanged example intentionally grants no real client access. Replace it with
the public IPv4 CIDR of the machine that runs the CLI.

Also verify that:

- the application listens on `0.0.0.0`, not only `127.0.0.1` or `localhost`;
- `container_port` matches the port inside the container;
- an unauthenticated `GET /` returns an HTTP success status;
- your network's public IPv4 did not change after the plan was created;
- no organization policy or upstream firewall blocks TCP/80.

The host always exposes TCP/80. The configured `container_port` is mapped to that
host port.

To make a disposable demo reachable from every IPv4 address, set
`allowed_source_ranges` to `["0.0.0.0/0"]` and pass
`--allow-public-http`. This serves unencrypted HTTP to the entire internet and is
not appropriate for sensitive data.

## A GitHub deployment does not build

GitHub mode supports a deliberately small contract:

- public `https://github.com/OWNER/REPOSITORY[.git]` URLs only;
- the repository's default branch only;
- a file named `Dockerfile` at the repository root;
- no subdirectory, ref, build argument, secret, or private dependency support.

The VM performs a shallow clone and runs the Docker build as root. Pinning a
repository URL does not pin its content. Review the repository before running it.
Changes pushed to the same URL are not continuously deployed; destroy and create
the deployment again to rebuild the current default-branch content.

## Unsafe or unexpected Terraform state

The CLI refuses state containing resource addresses it does not own, legacy
addresses, a non-default workspace, mismatched project/region/zone values, or an
orphaned backup with no active state. This is intentional.

It also rejects a managed `main.tf` or provider lock file that differs from the
running CLI release, and any additional top-level `*.tf` or `*.tf.json` file.
Terraform automatically loads those files, so accepting them would break the
tool's reviewed ownership and cleanup boundary. Custom Terraform is not
supported.

- Do not delete `terraform.tfstate` to bypass the guard.
- Do not edit `.gcp-free-deploy.tfvars.json` by hand after creating resources.
- Run `down` from the exact working directory used for `up`.
- Use a separate empty directory for every deployment.
- Because resource names are fixed, use only one active deployment per Google
  Cloud project.

If an existing deployment reports a managed-asset version mismatch, keep its
state and use the same CLI release and runtime files that created it for cleanup.
Do not copy only the state into a freshly initialized directory.

The tagged `v0.1.3` release uses a legacy Terraform state layout that `v0.2.0`
does not migrate. Clean it up with the `v0.1.3` binary in its original working
directory before upgrading.

If state was moved or lost, first inspect the real resources in Google Cloud and
recover or explicitly migrate state. Blindly starting in a new directory can
create name conflicts while leaving billable resources unmanaged.

## Apply failed after creating some resources

Do not discard the working directory. The local state can contain a VPC, subnet,
or firewall even when VM creation failed. Run:

```bash
terraform state list
gcp-free-deploy down
```

The `down` path validates partial state and shows a destroy plan before deleting
anything. Afterward, confirm the Compute Engine instances, disks, IP addresses,
networks, and firewall rules in the intended project.

## Reporting a useful bug

Include:

- `gcp-free-deploy version`;
- operating system and CPU architecture;
- Terraform and Google Cloud CLI versions;
- the command and failure category;
- a minimal config with the project ID, repository URL, and public IP redacted;
- sanitized output around the failing step.

Use the [bug report form](https://github.com/heung115/gcp-free-deploy/issues/new/choose).
