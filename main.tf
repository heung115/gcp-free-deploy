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
  description = "배포 대상 GCP project ID"
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "project_id는 6~30자의 소문자, 숫자, 하이픈 형식이어야 합니다."
  }
}

variable "region" {
  description = "subnet을 생성할 GCP region"
  type        = string

  validation {
    condition     = can(regex("^[a-z]+(?:-[a-z]+)*[0-9]+$", var.region))
    error_message = "region 형식이 올바르지 않습니다."
  }
}

variable "zone" {
  description = "VM을 생성할 GCP zone"
  type        = string

  validation {
    condition     = can(regex("^[a-z]+(?:-[a-z]+)*[0-9]+-[a-z]$", var.zone))
    error_message = "zone 형식이 올바르지 않습니다."
  }
}

variable "deployment_source" {
  description = "docker 또는 github"
  type        = string

  validation {
    condition     = contains(["docker", "github"], var.deployment_source)
    error_message = "deployment_source는 docker 또는 github여야 합니다."
  }
}

variable "docker_image" {
  description = "배포할 공개 Docker 이미지. github 배포에서는 빈 문자열"
  type        = string
  default     = ""

  validation {
    condition     = var.docker_image == "" || can(regex("^[A-Za-z0-9][A-Za-z0-9._/:@-]+$", var.docker_image))
    error_message = "docker_image 형식이 올바르지 않습니다."
  }
}

variable "github_repo" {
  description = "Dockerfile이 있는 공개 GitHub HTTPS URL. docker 배포에서는 빈 문자열"
  type        = string
  default     = ""

  validation {
    condition     = var.github_repo == "" || can(regex("^https://github\\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\\.git)?$", var.github_repo))
    error_message = "github_repo는 https://github.com/<owner>/<repo>[.git] 형식이어야 합니다."
  }
}

variable "container_port" {
  description = "컨테이너 애플리케이션이 수신하는 포트"
  type        = number
  default     = 80

  validation {
    condition     = var.container_port >= 1 && var.container_port <= 65535 && floor(var.container_port) == var.container_port
    error_message = "container_port는 1~65535 범위의 정수여야 합니다."
  }
}

variable "allowed_source_ranges" {
  description = "데모 HTTP 포트에 접근할 수 있는 IPv4 CIDR 목록"
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
    error_message = "allowed_source_ranges에는 IPv4 CIDR을 하나 이상 지정해야 합니다."
  }
}

variable "machine_type" {
  description = "데모 VM machine type"
  type        = string
  default     = "e2-micro"
}

variable "disk_size_gb" {
  description = "부팅 디스크 크기(GB)"
  type        = number
  default     = 10

  validation {
    condition     = var.disk_size_gb >= 10 && var.disk_size_gb <= 30 && floor(var.disk_size_gb) == var.disk_size_gb
    error_message = "disk_size_gb는 10~30 범위의 정수여야 합니다."
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

# IAP TCP forwarding의 고정 주소 범위만 SSH를 허용한다. 인터넷 전체에 22번을 열지 않는다.
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
      image = "ubuntu-os-cloud/ubuntu-2204-lts"
      size  = var.disk_size_gb
      type  = "pd-standard"
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.demo.id

    # 데모 HTTP 접근을 위한 임시 외부 IPv4다. 정적 IP는 생성하지 않는다.
    access_config {
      network_tier = "STANDARD"
    }
  }

  metadata = {
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
  }

  # 이 앱은 Google Cloud API를 호출하지 않는다. 잠근 provider 7.12.0에서
  # scopes=[]는 service account와 OAuth scope를 연결하지 않는 명시적 설정이다.
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
    CURRENT_STEP=bootstrap

    log() {
      logger -t gcp-free-deploy -- "$1"
    }

    on_error() {
      local exit_code=$?
      log "STARTUP_FAILED step=$CURRENT_STEP line=$1 exit_code=$exit_code"
      exit "$exit_code"
    }
    trap 'on_error $LINENO' ERR

    log "STARTUP_BEGIN source=$DEPLOYMENT_SOURCE"

    CURRENT_STEP=install_docker
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    apt-get install -y --no-install-recommends ca-certificates curl docker.io git
    systemctl enable --now docker

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

    if [[ "$(docker inspect --format '{{.State.Running}}' web 2>/dev/null || true)" != "true" ]]; then
      log "CONTAINER_NOT_RUNNING name=web"
      exit 1
    fi

    CURRENT_STEP=http_health
    for attempt in $(seq 1 30); do
      if curl --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:80/ >/dev/null; then
        log "HTTP_HEALTH_OK attempt=$attempt"
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
