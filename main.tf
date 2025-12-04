# main.tf

# 1. 변수 설정 
variable "project_id" {
  description = "GCP 프로젝트 ID"
  type        = string
}

variable "github_repo" {
  description = "배포할 공개 깃허브 주소"
  type        = string
  default     = "https://github.com/googlecodelabs/container-engine-hello.git" # 테스트용 기본값
}

# 2. GCP 프로바이더 설정
provider "google" {
  project = var.project_id
  region  = "us-central1"
}

resource "google_project_service" "compute_api" {
  project = var.project_id
  service = "compute.googleapis.com"

  disable_on_destroy = false
}

# 3. 무료 VM 인스턴스 생성
resource "google_compute_instance" "free_vm" {

  depends_on = [google_project_service.compute_api]

  name         = "my-free-portfolio"
  machine_type = "e2-micro"
  zone         = "us-central1-a"

  # 부팅 디스크 설정
  boot_disk {
    initialize_params {
      image = "ubuntu-os-cloud/ubuntu-2204-lts"
      size  = 30
      type  = "pd-standard"
    }
  }

  # 네트워크 설정 (외부 IP 부여)
  network_interface {
    network = "default"
    access_config {
    }
  }

  # 방화벽 태그 (아래 방화벽 규칙과 연결됨)
  tags = ["http-server"]

  # 4. [핵심] 스타트업 스크립트
  metadata_startup_script = <<-EOF
    #!/bin/bash
    
    # 1. 시스템 업데이트 및 Docker 설치
    apt-get update
    apt-get install -y docker.io git

    # 2. 작업 폴더 생성 및 소스코드 클론
    mkdir -p /app
    git clone ${var.github_repo} /app/repo

    # 3. Docker 이미지 빌드 및 실행
    cd /app/repo
    
    # Dockerfile이 없으면 Nginx로 대체하는 안전장치 (테스트용)
    if [ ! -f Dockerfile ]; then
      docker run -d -p 80:80 nginx
    else
      docker build -t my-web-app .
      docker run -d -p 80:80 my-web-app
    fi
  EOF
}

# 5. 방화벽 오픈 (80번 포트)
resource "google_compute_firewall" "allow_http" {

  depends_on = [google_project_service.compute_api]

  name    = "allow-http-traffic"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["80"]
  }

  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["http-server"]
}

# 6. 결과 
output "website_url" {
  value = "http://${google_compute_instance.free_vm.network_interface.0.access_config.0.nat_ip}"
}
output "vm_name" {
  value = google_compute_instance.free_vm.name
}
output "vm_zone" {
  value = google_compute_instance.free_vm.zone
}
