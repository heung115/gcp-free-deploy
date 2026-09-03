# Google Cloud Free Tier에 Docker 배포하기

**gcp-free-deploy**는 공개 Docker 이미지 또는 공개 GitHub 저장소 하나를
Terraform으로 Google Compute Engine VM에 배포하고, 상태를 확인한 뒤 안전하게
정리하는 Go CLI입니다.

[English](README.md)

> [!WARNING]
> **Free Tier 조건에 맞는 구성도 0원은 보장되지 않습니다.** 기본 VM·리전·디스크는
> Compute Engine Free Tier 범위를 목표로 하지만, 공개 접속을 위해 외부 IPv4를
> 할당합니다. 현재 외부 IPv4의 무료 사용량은 결제 계정당 월 1시간뿐이므로 24시간
> 실행하면 주소 비용만 월 약 **$3.60~$3.72**가 생길 수 있습니다. 적용 전
> [비용 및 Free Tier 안내](docs/costs.md)를 확인하세요. 가격 확인일: **2026-09-03**.

## 주요 기능

- 전용 VPC, subnet, HTTP firewall, IAP SSH firewall, VM 생성
- 명시적 태그 또는 digest가 있는 공개 Docker 이미지 배포
- 루트에 `Dockerfile`이 있는 공개 GitHub 저장소 빌드 및 배포
- 같은 region 안에서 zone 용량 부족 시 fallback
- IAP SSH를 통한 startup·container·내부 HTTP 상태 확인
- 외부 HTTP health check
- 저장된 Terraform plan 확인 후 배포·삭제
- 기존 state와 부분 생성 state에 대한 안전 검사

## 요구사항

- 소스 설치 시 Go 1.26.8 이상
- Terraform 1.9 이상, 2.0 미만
- [Google Cloud CLI](https://cloud.google.com/sdk/docs/install)
- 외부 상태 확인에 사용할 로컬 `curl`
- 결제가 연결되고 Compute Engine API가 활성화된 GCP project
- Application Default Credentials와 `gcloud` 로그인

```bash
gcloud auth login
gcloud auth application-default login
gcloud config set project YOUR_PROJECT_ID
gcloud services enable compute.googleapis.com
```

필요 권한은 [영문 문제 해결 문서](docs/troubleshooting.md#permissions-and-apis)에
정리되어 있습니다. 편의를 위해 광범위한 `Owner`나 `Editor`를 부여하지 마세요.

## 설치

### Go로 설치

```bash
go install github.com/heung115/gcp-free-deploy@latest
```

### 릴리스 바이너리 사용

[최신 릴리스](https://github.com/heung115/gcp-free-deploy/releases/latest)에서
운영체제와 CPU에 맞는 압축 파일과 `SHA256SUMS`를 받은 뒤, 압축을 풀기 전에 checksum을
검증하세요. 이 릴리스 파일들에는 GitHub build provenance attestation이 별도로
게시됩니다. 지원 대상은 Linux amd64/arm64, macOS amd64/arm64, Windows amd64입니다.

Windows PowerShell에서는 다음 명령으로 ZIP의 SHA-256 값을 확인한 뒤
`SHA256SUMS`의 해당 항목과 비교할 수 있습니다.

```powershell
(Get-FileHash .\gcp-free-deploy-v0.2.0-windows-amd64.zip -Algorithm SHA256).Hash
```

## 빠른 시작

Terraform state가 다른 배포와 섞이지 않도록 배포마다 빈 작업 폴더를 사용하세요.

```bash
mkdir my-gcp-deployment
cd my-gcp-deployment
gcp-free-deploy init
cp gcp-free-deploy.example.json gcp-free-deploy.json
```

Windows PowerShell에서는 `mkdir`, `cd`, `cp` 대신 각각
`New-Item -ItemType Directory`, `Set-Location`, `Copy-Item`을 사용하세요.

`gcp-free-deploy.json`을 수정합니다.

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

`203.0.113.10/32`는 문서용 주소라 실제로 접속할 수 없습니다. 접속할 곳의 공인
IPv4 뒤에 `/32`를 붙여 바꾸세요.

startup image는 x86-64용 Ubuntu 24.04입니다. `machine_type`을 바꿀 때도 x86-64를
지원해야 하며 T2A, C4A 같은 Arm 전용 machine family는 지원하지 않습니다.

```bash
gcp-free-deploy validate
gcp-free-deploy up --plan-only
gcp-free-deploy up
```

Terraform plan을 확인한 뒤에만 `yes`를 입력하세요. 사용이 끝나면 다음 명령으로
로컬 state가 관리하는 리소스를 확인하고 삭제합니다.

```bash
gcp-free-deploy down
```

## GitHub 저장소 배포

`source`를 `github`로 바꾸고 `docker_image`는 제거한 뒤, 기본 브랜치 루트에
`Dockerfile`이 있는 공개 저장소를 지정합니다. 아래 예시는 완전한 설정입니다.

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

컨테이너는 `localhost`가 아니라 `0.0.0.0`에서 수신해야 하고, `container_port`가
애플리케이션 포트와 같아야 합니다. 비공개 저장소, 기본 브랜치가 아닌 브랜치,
하위 폴더 Dockerfile, 빌드 인자, 런타임 secret은 지원하지 않습니다. VM은 Linux
amd64이므로 이미지와 빌드 결과도 이를 지원해야 합니다. GitHub 모드는 VM 생성 때
한 번만 clone하며 지속 배포가 아닙니다. 같은 URL의 새 commit을 반영하려면 삭제 후
다시 생성해야 합니다.

Docker tag도 registry에서 같은 이름으로 바뀔 수 있습니다. 같은 tag로 `up`을 다시
실행해도 기존 VM의 이미지는 갱신되지 않으므로, 가능하면 digest를 사용하고 새 이미지를
반영할 때는 `down` 후 다시 생성하세요.

## 명령

| 명령 | 동작 |
| --- | --- |
| `gcp-free-deploy init` | 기존 파일은 덮어쓰지 않고 누락된 Terraform·예제 파일 준비 |
| `gcp-free-deploy validate` | GCP 조회·변경 없이 설정과 Terraform 정적 검증 |
| `gcp-free-deploy up --plan-only` | GCP를 조회해 plan만 만들고 적용하지 않음 |
| `gcp-free-deploy up` | plan 확인 후 생성·상태 검증 |
| `gcp-free-deploy down` | 로컬 state가 관리하는 리소스 확인 후 삭제 |
| `gcp-free-deploy version` | CLI 버전 출력 |

전체 인터넷에 평문 HTTP를 공개하려면 설정에 `0.0.0.0/0`을 넣고 위험을 별도로
허용해야 합니다. 여러 CIDR의 합이 전체 IPv4 주소를 덮는 경우에도 같은 허용이
필요합니다.

```bash
gcp-free-deploy up --allow-public-http
```

## 안전 장치

- default VPC 대신 전용 VPC를 만듭니다.
- SSH 22번은 IAP 주소 범위에서만 허용합니다.
- OS Login을 사용하고 project SSH key를 차단합니다.
- VM에 service account와 OAuth scope를 연결하지 않습니다.
- HTTP 전체 공개는 별도 옵션 없이는 거부합니다.
- 기존 state에 다른 리소스나 구버전 주소가 섞이면 중단합니다.
- 기존 Terraform 파일과 설정 파일을 자동으로 덮어쓰지 않습니다.
- 관리 대상 Terraform 파일이 바뀌었거나 최상위 경로에 다른 Terraform 파일이 있으면 중단합니다.
- state, 실제 설정, credential 파일은 Git에서 제외합니다.

기본 서비스는 평문 HTTP입니다. root 소유 Docker daemon이 선택한 소스를 build하고
container를 시작하며, container process는 이미지가 지정한 사용자로 실행됩니다.
이미지가 사용자를 지정하지 않으면 root입니다. 신뢰할 수 있는 코드만 사용하고 민감한
데이터나 운영 서비스는 배포하지 마세요.

## 제한 사항

- 단일 VM과 단일 container만 지원
- HTTPS, 도메인, 인증, 고가용성, 자동 확장, 백업 미지원
- Docker 이미지 서명·취약점 검사 미지원
- health check 경로 `/` 고정
- 작업 폴더별 프로세스 잠금은 제공하지만 remote state·팀 단위 잠금은 없는 local state 사용
- 구버전 state 자동 migration 미지원
- 비공개 registry·GitHub 저장소 미지원
- 고정된 리소스 이름 사용: GCP project당 활성 배포 1개만 지원
- 자동 만료·중지·정리 없음: `down`이 성공할 때까지 리소스가 계속 실행됨
- `e2-micro`의 메모리나 disk 용량을 넘는 container build는 실패할 수 있음
- VM 교체 등 lifecycle 작업 뒤 임시 외부 IP가 바뀔 수 있음

`down`이 끝날 때까지 작업 폴더와 local state를 보관하세요. 먼저 옮기거나 삭제하면
리소스와 비용이 남아도 안전하게 정리하지 못할 수 있습니다.

활성 배포를 만든 CLI 바이너리도 정리가 끝날 때까지 보관하세요. 새 버전이 관리 자산
불일치를 보고하면 원래 바이너리와 작업 폴더에서 `down`을 실행해야 합니다. state만 새
폴더로 복사하면 안 됩니다.

태그된 `v0.1.3` 릴리스로 만든 배포는 legacy state 구조를 사용합니다. `v0.2.0`으로
올리기 전에 원래 작업 폴더와 `v0.1.3` 바이너리로 `down`을 실행하세요. 새 CLI는 legacy
state를 임의로 migration하거나 삭제하지 않습니다.

## 문서

- [비용 및 Free Tier 안내](docs/costs.md)
- [문제 해결](docs/troubleshooting.md)
- [설계와 운영 판단](docs/architecture-and-operations.ko.md)
- [기여 안내](CONTRIBUTING.md)
- [보안 정책](SECURITY.md)
