terraform {
  required_version = ">= 1.9.0, < 2.0.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "7.12.0"
    }
  }
}

variable "project_id" {
  description = "GCP project ID to deploy into"
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "project_id must be 6-30 characters, start with a lowercase letter, end with a lowercase letter or digit, and contain only lowercase letters, digits, and hyphens."
  }
}

variable "region" {
  description = "GCP region where the subnet will be created"
  type        = string

  validation {
    condition     = can(regex("^[a-z]+(?:-[a-z]+)*[0-9]+$", var.region))
    error_message = "region has an invalid format."
  }
}

variable "zone" {
  description = "GCP zone where the VM will be created"
  type        = string

  validation {
    condition     = can(regex("^[a-z]+(?:-[a-z]+)*[0-9]+-[a-z]$", var.zone))
    error_message = "zone has an invalid format."
  }
}

variable "deployment_source" {
  description = "Deployment source: docker or github"
  type        = string

  validation {
    condition     = contains(["docker", "github"], var.deployment_source)
    error_message = "deployment_source must be docker or github."
  }
}

variable "docker_image" {
  description = "Public Docker image to deploy; empty for github deployments"
  type        = string
  default     = ""

  validation {
    condition     = var.docker_image == "" || can(regex("^[A-Za-z0-9][A-Za-z0-9._/:@-]+$", var.docker_image))
    error_message = "docker_image has an invalid format."
  }
}

variable "github_repo" {
  description = "Public GitHub HTTPS URL containing a Dockerfile; empty for docker deployments"
  type        = string
  default     = ""

  validation {
    condition     = var.github_repo == "" || can(regex("^https://github\\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\\.git)?$", var.github_repo))
    error_message = "github_repo must match https://github.com/<owner>/<repo>[.git]."
  }
}

variable "container_port" {
  description = "Port on which the containerized application listens"
  type        = number
  default     = 80

  validation {
    condition     = var.container_port >= 1 && var.container_port <= 65535 && floor(var.container_port) == var.container_port
    error_message = "container_port must be an integer between 1 and 65535."
  }
}

variable "allowed_source_ranges" {
  description = "IPv4 CIDR ranges allowed to access the demo HTTP port"
  type        = list(string)
  default     = ["127.0.0.1/32"]

  validation {
    condition = (
      length(var.allowed_source_ranges) > 0 &&
      alltrue([
        for source_range in var.allowed_source_ranges :
        can(cidrnetmask(source_range)) && !strcontains(source_range, ":")
      ])
    )
    error_message = "allowed_source_ranges must contain at least one IPv4 CIDR."
  }
}

variable "machine_type" {
  description = "Machine type for the demo VM"
  type        = string
  default     = "e2-micro"
}

variable "disk_size_gb" {
  description = "Boot disk size in GB"
  type        = number
  default     = 10

  validation {
    condition     = var.disk_size_gb >= 10 && var.disk_size_gb <= 30 && floor(var.disk_size_gb) == var.disk_size_gb
    error_message = "disk_size_gb must be an integer between 10 and 30."
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}

locals {
  http_tag = "gcp-free-deploy-http"
  ssh_tag  = "gcp-free-deploy-iap-ssh"
}

resource "google_compute_network" "demo" {
  name                    = "gcp-free-deploy-network"
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"
}

resource "google_compute_subnetwork" "demo" {
  name          = "gcp-free-deploy-subnet"
  region        = var.region
  network       = google_compute_network.demo.id
  ip_cidr_range = "10.42.0.0/28"
}

resource "google_compute_firewall" "http" {
  name      = "gcp-free-deploy-allow-http"
  network   = google_compute_network.demo.id
  direction = "INGRESS"

  allow {
    protocol = "tcp"
    ports    = ["80"]
  }

  source_ranges = var.allowed_source_ranges
  target_tags   = [local.http_tag]
}

# Allow SSH only from the fixed IAP TCP forwarding range; do not expose port 22 to the entire internet.
resource "google_compute_firewall" "iap_ssh" {
  name      = "gcp-free-deploy-allow-iap-ssh"
  network   = google_compute_network.demo.id
  direction = "INGRESS"

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = ["35.235.240.0/20"]
  target_tags   = [local.ssh_tag]
}

resource "google_compute_instance" "demo" {
  name         = "gcp-free-deploy-demo"
  machine_type = var.machine_type
  zone         = var.zone

  labels = {
    managed-by = "gcp-free-deploy"
    lifecycle  = "ephemeral"
  }

  tags = [local.http_tag, local.ssh_tag]

  boot_disk {
    auto_delete = true

    initialize_params {
      image = "ubuntu-os-cloud/ubuntu-2404-lts-amd64"
      size  = var.disk_size_gb
      type  = "pd-standard"
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.demo.id

    # Use an ephemeral external IPv4 address for demo HTTP access; do not create a static IP.
    access_config {
      network_tier = "STANDARD"
    }
  }

  metadata = {
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
  }

  # This app does not call Google Cloud APIs. With pinned provider 7.12.0,
  # scopes = [] explicitly configures no service account and no OAuth scopes.
  # https://github.com/hashicorp/terraform-provider-google/blob/v7.12.0/google/services/compute/resource_compute_instance.go
  service_account {
    scopes = []
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  metadata_startup_script = <<-STARTUP
    #!/usr/bin/env bash
    set -Eeuo pipefail

    readonly DEPLOYMENT_SOURCE='${var.deployment_source}'
    readonly DOCKER_IMAGE='${var.docker_image}'
    readonly GITHUB_REPO='${var.github_repo}'
    readonly CONTAINER_PORT='${var.container_port}'
    readonly STATE_DIR=/var/lib/gcp-free-deploy
    readonly COMPLETED_MARKER="$STATE_DIR/startup-complete"
    CURRENT_STEP=bootstrap

    log() {
      logger -t gcp-free-deploy -- "$1"
    }

    apt_retry() {
      local attempt
      for attempt in $(seq 1 5); do
        if "$@"; then
          return 0
        fi
        log "APT_RETRY step=$CURRENT_STEP attempt=$attempt"
        sleep 5
      done
      return 1
    }

    on_error() {
      local exit_code=$?
      log "STARTUP_FAILED step=$CURRENT_STEP line=$1 exit_code=$exit_code"
      exit "$exit_code"
    }
    trap 'on_error $LINENO' ERR

    install -d -m 0755 "$STATE_DIR"

    if [[ -f "$COMPLETED_MARKER" ]]; then
      log "STARTUP_BEGIN source=$DEPLOYMENT_SOURCE mode=restart"
      CURRENT_STEP=restart_existing
      systemctl enable --now docker
      if ! docker start web >/dev/null; then
        rm -f "$COMPLETED_MARKER"
        log "FAILURE_DOCKER_RUN reason=restart_existing"
        exit 1
      fi
    else
      log "STARTUP_BEGIN source=$DEPLOYMENT_SOURCE mode=provision"
      export DEBIAN_FRONTEND=noninteractive
      CURRENT_STEP=apt_update
      apt_retry apt-get -o Acquire::Retries=3 update -y
      CURRENT_STEP=install_docker
      apt_retry apt-get -o Acquire::Retries=3 -o DPkg::Lock::Timeout=120 install -y --no-install-recommends ca-certificates curl docker.io git
      systemctl enable --now docker

      # A failed first boot leaves no completion marker. Clean up its partial
      # application state so the next boot can retry provisioning from scratch.
      docker rm -f web >/dev/null 2>&1 || true

      if [[ "$DEPLOYMENT_SOURCE" == "docker" ]]; then
        CURRENT_STEP=docker_pull
        if ! docker pull "$DOCKER_IMAGE"; then
          log "FAILURE_DOCKER_PULL"
          exit 1
        fi
        APP_IMAGE="$DOCKER_IMAGE"
      else
        CURRENT_STEP=git_clone
        install -d -m 0755 /opt/gcp-free-deploy
        rm -rf /opt/gcp-free-deploy/repo
        git clone --depth 1 -- "$GITHUB_REPO" /opt/gcp-free-deploy/repo

        if [[ ! -f /opt/gcp-free-deploy/repo/Dockerfile ]]; then
          log "FAILURE_DOCKER_BUILD reason=no_dockerfile"
          exit 1
        fi

        CURRENT_STEP=docker_build
        if ! docker build --tag gcp-free-deploy-app:local /opt/gcp-free-deploy/repo; then
          log "FAILURE_DOCKER_BUILD"
          exit 1
        fi
        APP_IMAGE=gcp-free-deploy-app:local
      fi

      CURRENT_STEP=docker_run
      if ! docker run --detach --name web --restart unless-stopped --publish "80:$CONTAINER_PORT" "$APP_IMAGE"; then
        log "FAILURE_DOCKER_RUN"
        exit 1
      fi
    fi

    if [[ "$(docker inspect --format '{{.State.Running}}' web 2>/dev/null || true)" != "true" ]]; then
      log "CONTAINER_NOT_RUNNING name=web"
      exit 1
    fi

    CURRENT_STEP=http_health
    for attempt in $(seq 1 30); do
      if curl --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:80/ >/dev/null; then
        log "HTTP_HEALTH_OK attempt=$attempt"
        CURRENT_STEP=mark_complete
        touch "$COMPLETED_MARKER"
        log "STARTUP_DONE"
        exit 0
      fi
      sleep 4
    done

    log "FAILURE_HTTP_HEALTH attempts=30"
    exit 1
  STARTUP
}

output "project_id" {
  value = var.project_id
}

output "region" {
  value = var.region
}

output "vm_name" {
  value = google_compute_instance.demo.name
}

output "vm_zone" {
  value = google_compute_instance.demo.zone
}

output "website_url" {
  value = "http://${google_compute_instance.demo.network_interface[0].access_config[0].nat_ip}"
}

output "generated_resources" {
  value = [
    google_compute_network.demo.name,
    google_compute_subnetwork.demo.name,
    google_compute_firewall.http.name,
    google_compute_firewall.iap_ssh.name,
    google_compute_instance.demo.name,
  ]
}
