# gcp-free-deploy

GCP Compute Engine에 단일 Docker 애플리케이션을 배포하고, 상태를 확인한 뒤 Terraform으로 정리하는 CLI입니다.

## 주요 기능

- 전용 VPC, subnet, firewall, VM 생성
- 공개 Docker 이미지 또는 공개 GitHub 저장소의 `Dockerfile` 배포
- 같은 region 내 zone capacity fallback
- IAP SSH를 통한 startup 및 container 상태 확인
- VM 내부·외부 HTTP health check
- 저장된 Terraform plan 확인 후 배포·삭제
- 기존 state와 부분 생성 state에 대한 안전 검사

## 구조

```text
JSON 설정
   │
   ▼
Go CLI ── Terraform ── GCP
   │                    ├─ VPC / Subnet
   │                    ├─ HTTP / IAP SSH Firewall
   │                    └─ Compute Engine VM
   │
   └─ IAP SSH 및 HTTP 상태 확인
```

| 구성 요소 | 역할 |
| --- | --- |
| Go CLI | 입력 검증, 실행 순서, 승인, 상태 확인, 오류 처리 |
| Terraform | 네트워크, 방화벽, VM 생성과 삭제 |
| Docker | 공개 이미지 실행 또는 공개 저장소 빌드 |
| IAP SSH | 인터넷에 SSH를 직접 공개하지 않고 VM 상태 확인 |

## 요구사항

- Go 1.24.1 이상
- Terraform 1.9 이상, 2.0 미만
- Google Cloud CLI (`gcloud`)
- 결제가 연결되고 Compute Engine API가 활성화된 GCP project
- Application Default Credentials

```bash
gcloud auth application-default login
gcloud auth login
```

## 빠른 시작

### 소스에서 실행

```bash
git clone https://github.com/heung115/gcp-free-deploy.git
cd gcp-free-deploy
cp gcp-free-deploy.example.json gcp-free-deploy.json
```

### 릴리스 바이너리 실행

빈 작업 폴더에서 다음 명령을 실행하면 필요한 Terraform 파일과 예제 설정이 준비됩니다.

```bash
mkdir gcp-free-deploy-workdir
cd gcp-free-deploy-workdir
gcp-free-deploy init
cp gcp-free-deploy.example.json gcp-free-deploy.json
```

지원 플랫폼: Linux amd64/arm64, macOS amd64/arm64, Windows amd64

## 설정

`gcp-free-deploy.json`을 환경에 맞게 수정합니다.

```json
{
  "project_id": "your-project-id",
  "zone": "us-central1-a",
  "fallback_zones": ["us-central1-b", "us-central1-c"],
  "source": "docker",
  "docker_image": "nginx:1.27.4",
  "container_port": 80,
  "allowed_source_ranges": ["203.0.113.10/32"],
  "machine_type": "e2-micro",
  "disk_size_gb": 10
}
```

GitHub 저장소를 배포하려면 `source`를 `github`로 바꾸고 루트에 `Dockerfile`이 있는 공개 저장소를 지정합니다.

```json
{
  "source": "github",
  "github_repo": "https://github.com/example/demo-app.git"
}
```

`203.0.113.10/32`는 예시 주소입니다. 실제 접속할 공인 IPv4 CIDR로 변경하세요.

## 사용법

### 설정 검증

GCP 리소스를 조회하거나 변경하지 않습니다.

```bash
go run . validate
```

### 배포 계획 확인

GCP 상태를 조회하고 plan을 만들지만 적용하지 않습니다.

```bash
go run . up --plan-only
```

### 배포

```bash
go run . up
```

Terraform plan을 확인한 뒤 `yes`를 입력하면 적용됩니다.

startup 검증은 성공할 때까지 무조건 15분을 기다리지 않습니다. IAP로 상태를 주기적으로 확인해 container와 내부 HTTP가 준비되면 즉시 다음 단계로 넘어갑니다. 기본 15분은 APT mirror·Docker registry·VM 성능 차이로 무한 대기하거나 비용이 계속 발생하지 않게 하는 최대 상한이며, 1분~1시간 범위에서 조정할 수 있습니다.

```bash
go run . up --startup-timeout 20m
```

상한을 넘기면 제한 시각에 상태를 마지막으로 다시 확인하고 startup service·tagged log·container·내부 HTTP 진단을 수집합니다.

전체 인터넷에 HTTP를 공개하려면 설정에 `0.0.0.0/0`을 지정하고 위험을 명시적으로 허용해야 합니다.

```bash
go run . up --allow-public-http
```

### 삭제

```bash
go run . down
```

state의 project, region, zone, 리소스 이름을 검증하고 destroy plan을 보여준 뒤 삭제합니다. VM 생성 전에 실패해 네트워크나 방화벽만 남은 경우에도 정리할 수 있습니다.

## 생성되는 리소스

- 전용 VPC 1개
- `/28` subnet 1개
- 제한된 HTTP firewall rule 1개
- IAP 전용 SSH firewall rule 1개
- 임시 외부 IPv4를 사용하는 Compute Engine VM 1개
- 10GB `pd-standard` boot disk 1개

## 안전 장치

- default VPC를 사용하지 않습니다.
- SSH 22번은 IAP 주소 범위에서만 허용합니다.
- OS Login을 사용하고 project SSH key를 차단합니다.
- VM에 service account와 OAuth scope를 연결하지 않습니다.
- 기존 state에 다른 리소스나 구버전 주소가 섞여 있으면 실행을 중단합니다.
- 기존 Terraform 파일과 설정 파일을 자동으로 덮어쓰지 않습니다.
- state, 실제 설정, credential 파일은 Git에서 제외합니다.

> 기본 서비스는 평문 HTTP입니다. 민감한 데이터나 운영 서비스를 배포하지 마세요. GCP Free Tier 적용 여부와 관계없이 외부 IP, 디스크, 네트워크 등에 비용이 발생할 수 있습니다.

## 제한 사항

- 단일 VM과 단일 container만 지원합니다.
- HTTPS, 인증, 고가용성, 자동 확장, 백업은 제공하지 않습니다.
- Docker 이미지 서명과 취약점을 검사하지 않습니다.
- health check 경로는 `/`로 고정됩니다.
- 로컬 state를 사용하므로 동시에 여러 배포를 실행하면 안 됩니다.
- 구버전 state를 자동 migration하거나 삭제하지 않습니다.

자세한 설계와 운영 판단은 [architecture-and-operations.md](docs/architecture-and-operations.md)를 참고하세요.
