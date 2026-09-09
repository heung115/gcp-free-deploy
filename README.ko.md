# Google Cloud Free Tier에 Docker 배포하기

**gcp-free-deploy**는 공개 Docker 이미지 또는 공개 GitHub 저장소 하나를
Terraform으로 Google Compute Engine VM에 배포하고, 상태를 확인한 뒤 안전하게
정리하는 Go CLI입니다.

[English](README.md)

> [!WARNING]
> **Free Tier 조건에 맞는 구성도 0원은 보장되지 않습니다.** 기본 VM·리전·디스크는
> Compute Engine Free Tier 범위를 목표로 하지만, 공개 접속을 위해 외부 IPv4를
> 할당합니다. **2026-09-07 가격 API 조회에서 계정당 월 720 주소·시간 무료,
> 이후 $0.005/시간**을 확인했습니다. VPC 가격표의 표기와는 여전히 다릅니다.
> 다른 IP 사용량이 없고 같은 요율이 적용되면 IP 하나를 30일 유지할 때 $0,
> 31일은 $0.12입니다(세금·크레딧 적용 전). 가격 응답 자체는 실제 청구 내역이 아닙니다.
> Standard Tier 송신에는 별도의 **월 200 GiB** 무료 구간이 있습니다.
> 계정 내 합산 사용량, 세금, 다른 리소스에 따라 비용이 발생할 수 있습니다.
> [비용 및 Free Tier 안내](docs/costs.md)를 확인하세요. IPv4·트래픽 자료 확인일:
> **2026-09-07**.

## 주요 기능

- 전용 VPC, subnet, HTTP firewall, IAP SSH firewall, VM 생성
- 명시적 태그 또는 digest가 있는 공개 Docker 이미지 배포
- 루트에 `Dockerfile`이 있는 공개 GitHub 저장소 빌드 및 배포
- 같은 region 안에서 zone 용량 부족 시 fallback
- IAP SSH를 통한 startup·container·내부 HTTP 상태 확인
- 외부 HTTP health check
- 저장된 Terraform plan 확인 후 배포·삭제
- 오프라인 비용 구성 점검 및 Free Tier VM·리전 범위를 벗어난 배포의 기본 차단
- 실행 중인 VM의 실제 네트워크 등급·프로젝트 디스크·VM 수 조회
- 선택한 실행 시간이 지나면 VM 자동 중지: 예제는 24시간
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

## 안내형 시작

필요한 도구를 설치한 뒤 프로그램만 실행하세요. `start`를 명시해도 같습니다.

```bash
gcp-free-deploy
```

새 배포를 선택하면 로그인 상태를 확인하고, 필요할 때 동의를 받아 브라우저 로그인을
진행합니다. 프로젝트 목록에서 대상을 고르고 이미지 또는 공개 GitHub 주소, 앱 포트,
테스트/상시 운영 방식, 접속할 IP를 입력합니다. 공인 IPv4 조회에는 `api.ipify.org`를
사용하며 조회값을 확인하거나 직접 입력할 수 있습니다.

안내를 승인하면 현재 폴더 아래에 별도 배포 폴더와 설정을 만들고 Compute Engine API를
준비합니다. 기존 배포 검증과 비용 알림 설정을 포함한 `up` 흐름으로 이어지며, 최종
Terraform 계획을 보고 승인한 뒤 실제 서버를 만듭니다. 테스트 운영은 24시간 후 중지,
상시 운영은 시간 제한 없음입니다. 기본 접근은 지정한 IPv4 한 곳만 허용합니다.

GCP 프로젝트 생성·결제 연결·권한 부여 및 도구 설치는 사용자가 준비해야 합니다.
누락되면 필요한 작업을 안내합니다. 기존 배포 확인·삭제는 출력된 배포 폴더로 이동해서
프로그램을 실행하거나 기존 `up`·`down` 명령을 사용하세요. 안내형 시작은 기존 파일을
덮어쓰지 않습니다. 아래는 설정 파일을 직접 관리하는 방법입니다.

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
  "disk_size_gb": 10,
  "max_runtime_hours": 24
}
```

`203.0.113.10/32`는 문서용 주소라 실제로 접속할 수 없습니다. 접속할 곳의 공인
IPv4 뒤에 `/32`를 붙여 바꾸세요.

startup image는 x86-64용 Ubuntu 24.04입니다. `machine_type`을 바꿀 때도 x86-64를
지원해야 하며 T2A, C4A 같은 Arm 전용 machine family는 지원하지 않습니다.

```bash
gcp-free-deploy validate
gcp-free-deploy cost
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
  "disk_size_gb": 10,
  "max_runtime_hours": 24
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

`up`은 기본적으로 결제 계정과 로그인 이메일을 찾아 무료 비용 이메일 알림을 자동 설정합니다.
필요한 API와 이메일 채널을 준비하고 기존 알림은 재사용합니다. 설정에 실패하면 VM 적용 전에
중단합니다. `--plan-only`와 배포 취소 시에는 알림을 변경하지 않습니다. 별도 관리 환경에서만
`--skip-budget-alerts`로 명시적으로 건너뛸 수 있습니다. `budget`은 개별 관리용 명령입니다.


## 명령

| 명령 | 동작 |
| --- | --- |
| `gcp-free-deploy` / `start` | 안내에 따라 로그인·프로젝트·앱 설정 후 배포 |
| `gcp-free-deploy init` | 기존 파일은 덮어쓰지 않고 누락된 Terraform·예제 파일 준비 |
| `gcp-free-deploy validate` | GCP 조회·변경 없이 설정과 Terraform 정적 검증 |
| `gcp-free-deploy budget --project PROJECT --billing-account ACCOUNT` | 무료 이메일 비용 알림 생성·재사용, 기본 월 1 청구 통화 단위 |
| `gcp-free-deploy cost` | 외부 도구·인증·파일 생성 없이 설정의 비용 구성을 오프라인 점검 |
| `gcp-free-deploy audit --project PROJECT --vm VM --zone ZONE` | 실제 VM·네트워크 등급·프로젝트 디스크·VM 수 조회, state 불필요 |
| `gcp-free-deploy up --plan-only` | GCP를 조회해 plan만 만들고 적용하지 않음 |
| `gcp-free-deploy up` | plan 확인 후 생성·상태 검증 |
| `gcp-free-deploy down` | 로컬 state가 관리하는 리소스 확인 후 삭제 |
| `gcp-free-deploy version` | CLI 버전 출력 |

`gcp-free-deploy cost --config other.json`으로 다른 설정 파일도 점검할 수 있습니다.
`cost`는 Terraform이나 `gcloud`, 로그인 없이 설정만 읽으며 파일을 만들지 않습니다.
VM이 `e2-micro`가 아니거나 리전이 `us-west1`, `us-central1`, `us-east1` 밖이면
오류로 종료합니다. 디스크는 기존 설정 검증에서 10–30 GB로 제한합니다. 실제 리소스,
청구 내역, 계정 자격이나 사용량은 조회하지 않으므로 통과해도 0원을 보장하지 않습니다.

`up`은 이 범위를 벗어난 설정을 실행 파일 준비·외부 도구 확인·GCP 접근 전에 차단합니다.
의도적으로 배포하려면 `gcp-free-deploy up --allow-paid-resources`를 사용하세요.
`--auto-approve`로는 이 검사를 건너뛸 수 없습니다. `up --plan-only`는 별도 허용 없이
계획을 확인할 수 있으며, 기존 범위 밖 배포도 기존 state·관리 자산 검사를 통과하면
`down`으로 정리할 수 있습니다.

적용할 계획을 만들기 전에는 프로젝트에 다른 VM이 있는지도 확인합니다. 구버전 배포를
포함해 다른 VM이 있으면 기본적으로 중단하며, 조회 권한 부족이나 불완전한 결과를 빈
프로젝트로 간주하지 않습니다. `compute.instances.list` 권한이 필요합니다.
`--plan-only`와 `--allow-paid-resources`는 이 검사를 건너뜁니다. 같은 결제 계정의
다른 프로젝트 사용량은 별도로 확인해야 합니다.

기존 VM은 배포 도구나 로컬 state와 관계없이 다음처럼 조회합니다.

```bash
gcp-free-deploy audit --project YOUR_PROJECT_ID --vm YOUR_VM_NAME --zone us-central1-a
```

`audit`에는 `gcloud` 로그인과 Compute Engine 읽기 권한이 필요합니다. 실제 등급이
Premium이거나 필요한 정보가 없으면 오류로 종료하며, VM·디스크 구성도 점검합니다.
프로젝트의 VM이 여러 대면 주의를 표시합니다. 다른 프로젝트까지 합친 월 사용량과
청구액은 알 수 없으며, 클라우드 리소스를 변경하지 않습니다. `up`도 상태 검사와 성공
보고 전에 실제 Standard 적용을 확인합니다. `--allow-paid-resources`를 지정하면 이
검사를 건너뜁니다. 확인 실패 시에는 리소스가 남으므로 조회하거나 정리해야 합니다.

예제의 `max_runtime_hours: 24`는 VM을 시작한 뒤 24시간이 지나면 중지합니다.
생략하거나 `0`이면 제한이 없고, `1`–`168` 정수를 지정할 수 있습니다. 다시 시작하면
시간도 새로 계산합니다. **중지해도 디스크는 남습니다.** 잊고 켜 둔 데모를 줄이는
기능이며 과금 상한이나 0원 보장이 아닙니다. 사용이 끝나면 `down`으로 정리하세요.

구버전 템플릿의 빈 `access_config {}`는 기본 Premium을 사용합니다. 이후 로컬 코드를
Standard로 바꿔도 기존 VM에는 자동 반영되지 않습니다. 이번 버전도 실행 시간 설정으로
관리 템플릿이 바뀌었으므로 기존 배포의 CLI·`main.tf`·작업 폴더를 보관하세요.
새 CLI가 관리 파일 불일치로 중단하면 원래 바이너리로 `down`을 실행합니다.
네트워크 등급만 바꾸려고 구버전 state에 최신 템플릿을 적용하지 마세요.
자세한 순서는 [무료 우선 운영 안내](docs/free-first-operations.ko.md)를 참고하세요.

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
- 자동 삭제 없음: 실행 시간 제한으로 중지해도 디스크는 남으며 `down`으로 정리 필요
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
- [무료 우선 운영 안내](docs/free-first-operations.ko.md)
- [문제 해결](docs/troubleshooting.md)
- [설계와 운영 판단](docs/architecture-and-operations.ko.md)
- [기여 안내](CONTRIBUTING.md)
- [보안 정책](SECURITY.md)

## 라이선스

[MIT License](LICENSE)로 배포됩니다.
