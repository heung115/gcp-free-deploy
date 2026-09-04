# Security policy

## Supported versions

Security fixes are made on the default branch and included in the newest release.
Older releases are not maintained separately.

## Reporting a vulnerability

Use GitHub's
[private vulnerability reporting](https://github.com/heung115/gcp-free-deploy/security/advisories/new)
when it is available. If that option is unavailable, open a minimal public issue
asking the maintainer for a private contact channel, without including exploit
details, credentials, project identifiers, IP addresses, Terraform state, or
application logs.

Include the affected version, impact, prerequisites, and a minimal reproduction.
Please allow time for the report to be reviewed before publishing details.

## Scope and trust boundaries

This tool:

- creates cloud resources that can incur charges;
- serves plain HTTP on TCP/80;
- uses the VM's root-owned Docker daemon to build and start the selected source;
  the container uses the image-configured user, which is root if unspecified;
- stores Terraform state and deployment variables in the local working directory;
- invokes Terraform, Google Cloud CLI, and `curl` from the local environment.

It does not provide TLS, application authentication, secret management, image
signature verification, vulnerability scanning, workload isolation, backup, high
availability, or production hardening. Only deploy trusted code and do not place
sensitive data in the demo service.

The VM is created without a service account or OAuth scopes. SSH is restricted to
Google's IAP TCP forwarding range, but HTTP access follows the CIDRs supplied in
the config. `0.0.0.0/0`, or several ranges whose union covers all IPv4
addresses, exposes the application to the entire IPv4 internet.
