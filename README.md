# gcp-free-deploy

필요할 때 저비용 GCP Compute Engine VM을 만들고, 공개 Docker 이미지 또는 Dockerfile이 있는 공개 GitHub 저장소를 배포한 뒤, startup·container·HTTP 상태를 확인하고 Terraform으로 전부 삭제하는 학습용 CLI입니다.

이 프로젝트는 장기 운영 VM의 패치 도구가 아닙니다. Ansible, Kubernetes, 다중 서버, 고가용성, 자동 확장, 백업, 대규모 트래픽, 완전한 FinOps 기능은 제공하지 않습니다. 기본 운영 전략은 기존 VM을 계속 고치는 대신 필요할 때 만들고 사용 후 삭제하는 ephemeral 방식입니다.

## 실행 흐름

`up`은 다음 순서를 지킵니다.

1. JSON 설정과 입력값 검증
2. 누락된 Terraform·provider lock 자산 준비
3. default workspace와 기존 state 주소·대상 검증
4. `terraform fmt -check`
5. `terraform init -backend=false`
6. `terraform validate`
7. Terraform plan 생성
8. project·zone·machine type·disk·공개 포트·생성 리소스 표시
9. 사용자 확인 후 저장된 plan 적용
10. IAP SSH로 startup 완료 대기
11. Docker container 실행 상태 확인
12. VM 내부와 로컬에서 HTTP health check
13. 결과 요약

`down`은 로컬 state와 Terraform output에서 삭제 대상을 확인하고, destroy plan과 project·VM·zone을 표시한 다음 사용자 확인 후 삭제합니다. 삭제 뒤 `terraform state list`가 비었는지도 검사합니다.

## 요구사항

- Go 1.24.1 이상
- Terraform 1.9 이상, 2.0 미만
- Google Cloud CLI (`gcloud`)
- 결제가 연결되고 Compute Engine API가 활성화된 GCP project
- Application Default Credentials(ADC)

로컬 사용자 인증 예시:

```bash
gcloud auth application-default login
gcloud auth login
```

저장소 루트의 장기 서비스 계정 JSON key는 권장하지 않습니다. `credentials.json`, `.tfvars`, state, 실제 설정 파일은 Git에서 제외되지만, 키 파일 자체가 만들어지지 않도록 사용자 ADC 또는 서비스 계정 impersonation을 우선하세요.

### GCP 권한 경계

조직 정책과 기존 권한에 따라 더 세분화할 수 있습니다. 이 프로젝트가 사용하는 기능 기준의 공식 predefined role은 다음과 같습니다.

- Compute Engine API를 직접 활성화할 때: `roles/serviceusage.serviceUsageAdmin`
- VPC와 subnet 생성·삭제: `roles/compute.networkAdmin`
- firewall rule 생성·삭제: `roles/compute.securityAdmin`
- VM 생성·삭제와 subnet/external IP 사용: `roles/compute.instanceAdmin.v1`
- IAP tunnel 사용: `roles/iap.tunnelResourceAccessor`
- OS Login: startup 진단의 `sudo`를 위해 project 수준의 `roles/compute.osAdminLogin`
- 배포자와 다른 모니터 전용 사용자가 접속할 때: `compute.instances.get/list` 조회 권한도 필요하며, predefined role을 사용한다면 `roles/compute.viewer`로 충족할 수 있음

`Owner`나 `Editor`를 요구하지 않습니다. 실제 배포자와 모니터링 사용자가 다르면 배포 역할과 IAP/OS Login 역할도 분리하세요. 이 Terraform은 Compute Engine API를 자동 활성화하지 않으므로, API 활성화 권한은 사전 준비 때만 필요합니다.

참고: [Compute Engine IAM roles](https://cloud.google.com/compute/docs/access/iam), [IAP TCP forwarding](https://cloud.google.com/iap/docs/using-tcp-forwarding), [OS Login setup](https://cloud.google.com/compute/docs/oslogin/set-up-oslogin), [Service Usage](https://cloud.google.com/service-usage/docs/enable-disable)

## 빠른 시작

릴리스 바이너리를 받은 경우 빈 작업 폴더에서 먼저 실행 자산을 준비합니다. `main.tf`, provider lock, example 설정이 없을 때만 내장 사본을 생성하며 기존 파일은 덮어쓰지 않습니다.

릴리스 파일명은 `gcp-free-deploy-<OS>-<ARCH>` 형식이며 Windows 파일에는 `.exe` 확장자가 붙습니다. 필요하면 내려받은 파일명을 `gcp-free-deploy`로 바꾸고 실행 권한을 부여하세요.

```bash
mkdir gcp-free-deploy-workdir
cd gcp-free-deploy-workdir
gcp-free-deploy init
cp gcp-free-deploy.example.json gcp-free-deploy.json
```

소스에서 실행하려면 다음처럼 저장소를 복제합니다.

```bash
git clone https://github.com/heung115/gcp-free-deploy.git
cd gcp-free-deploy
cp gcp-free-deploy.example.json gcp-free-deploy.json
```

`gcp-free-deploy.json`의 project, zone, 배포 source, HTTP 접근 CIDR을 수정합니다. `203.0.113.10/32`는 문서용 주소이므로 실제 공인 IPv4 CIDR로 바꿔야 합니다.

Docker 이미지 예시:

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

GitHub 저장소 예시에서는 `source`를 `github`로 바꾸고 `docker_image` 대신 다음 필드를 사용합니다.

```json
{
  "source": "github",
  "github_repo": "https://github.com/example/demo-app.git"
}
```

GitHub 저장소에는 루트 `Dockerfile`이 있어야 합니다. 없으면 실패하며, 다른 Nginx 앱으로 몰래 대체하지 않습니다.

## 로컬 검증과 plan

설정과 Terraform 정적 검증만 수행합니다. GCP 조회나 리소스 변경은 하지 않습니다.

```bash
go run . validate
```

GCP provider가 실제 project 상태를 읽어 plan까지 만들되 적용하지 않습니다.

```bash
go run . up --plan-only
```

`--plan-only`는 read-only cloud 조회가 필요할 수 있지만 `apply`는 실행하지 않습니다.

### 기존 state가 있는 경우

`up`은 plan 전에 local state 주소와 기존 배포의 project·region·zone을 확인합니다. 공개 v0.1.x의 `free_vm`, `allow_http`, `compute_api` 같은 legacy 주소나 다른 대상이 섞여 있으면 중단합니다. 이때 `terraform.tfstate`를 삭제하거나 새 변수 파일을 억지로 맞추지 마세요.

1. `terraform state list`로 현재 관리 주소를 확인합니다.
2. GCP Console 또는 read-only `gcloud` 조회로 실제 VM·firewall·API 상태와 project·zone을 확인합니다.
3. 기존 리소스를 보존해야 하면 별도 작업 폴더로 state/configuration을 격리하거나 명시적인 `terraform state mv`·`moved` block migration을 검토합니다.
4. 삭제가 필요해도 기존 configuration과 state를 함께 보존한 상태에서 별도 계획·승인을 거칩니다.

현재 저장소의 `down`은 legacy state를 자동 삭제하지 않습니다.

## 배포

```bash
go run . up
```

계획과 비용 경고를 읽고 `yes`를 입력해야 적용됩니다. 자동 승인은 CI나 명시적 비대화형 실행에서만 사용하세요.

```bash
go run . up --auto-approve
```

`allowed_source_ranges`에 `0.0.0.0/0`이 있으면 전체 IPv4 인터넷에 평문 HTTP를 공개합니다. 실제 적용은 위험을 다시 드러내는 별도 옵션 없이는 차단됩니다.

```bash
go run . up --allow-public-http
```

비대화형 전체 공개라면 두 옵션을 함께 명시해야 합니다. 공개 Docker image의 `latest` tag는 시간이 지나며 내용이 바뀔 수 있으므로 고정 tag 또는 digest를 권장합니다.

## 상태 확인과 실패 진단

VM은 `gcp-free-deploy` tag의 startup log를 남깁니다. CLI는 IAP SSH로 다음 증거를 확인합니다.

- `google-startup-scripts.service` 상태
- `STARTUP_DONE` 또는 실패 단계
- `docker ps -a`
- 최대 120줄의 container log
- VM 내부 `127.0.0.1:80` health check
- 로컬에서 external URL health check

주요 실패 종류는 입력 오류, Terraform fmt/init/validate/plan/apply, zone capacity, IAP SSH, startup, Docker pull/build/run, container stopped, HTTP health check로 구분됩니다. 진단 출력은 8 KiB로 제한하고 일반적인 token·password·private key 패턴을 마스킹합니다. 애플리케이션이 별도 형식으로 비밀값을 출력한다면 완전한 제거는 보장할 수 없으므로 container log에 비밀을 남기지 마세요.

수동 확인 예시:

```bash
terraform output -json
gcloud compute ssh gcp-free-deploy-demo \
  --project YOUR_PROJECT_ID \
  --zone YOUR_ZONE \
  --tunnel-through-iap
```

## 안전한 삭제

```bash
go run . down
```

`terraform.tfstate`와 배포 변수 파일이 없거나 output에서 project·VM을 확인할 수 없으면 안전하게 중단합니다. 계획을 확인하고 `yes`를 입력해야 삭제됩니다.

```bash
go run . down --auto-approve
```

destroy가 일부 실패하면 다음으로 남은 대상을 확인하세요. state와 Cloud Console의 project·zone을 함께 확인하기 전 임의의 다른 리소스를 수동 삭제하지 마세요.

```bash
terraform state list
terraform plan -destroy -var-file=.gcp-free-deploy.tfvars.json
```

## 보안 경계

- 전용 VPC와 `/28` subnet을 만들며 default VPC를 사용하지 않습니다.
- HTTP firewall은 VM 전용 tag와 `allowed_source_ranges`에만 적용됩니다.
- SSH 22번은 인터넷 전체가 아니라 IAP 주소 `35.235.240.0/20`에서만 허용됩니다.
- OS Login을 활성화하고 project metadata SSH key를 차단합니다.
- VM 애플리케이션은 Google Cloud API를 호출하지 않으므로 service account와 OAuth scope를 붙이지 않습니다.
- VM에는 임시 external IPv4만 사용하며 static IP는 만들지 않습니다.
- Docker·APT·GitHub 접근에 필요한 outbound traffic은 기본 egress를 사용하며 별도로 제한하지 않습니다. 침해된 애플리케이션의 외부 통신을 차단하는 운영 보안 경계는 제공하지 않습니다.
- 외부 서비스 secret, Docker registry credential, GitHub token을 받거나 Terraform output에 기록하지 않습니다.
- 로컬 state는 암호화되지 않은 파일일 수 있습니다. 개인 장치에서만 보관하고 공유·commit하지 마세요. 운영 확장 시 access control과 encryption이 있는 remote backend가 필요합니다.
- 기본 서비스는 HTTP입니다. TLS, 인증, WAF, rate limiting이 없으므로 민감한 앱이나 실제 사용자 데이터를 배포하면 안 됩니다.

## 비용 주의사항

기본값은 `e2-micro`, 10GB `pd-standard`, 임시 외부 IPv4입니다. GCP Free Tier 조건, 대상 region, 외부 IP와 network 과금은 바뀔 수 있고 사용자의 계정·region·사용량에 따라 과금될 수 있습니다. 이 저장소는 비용 절감 성과를 증명하지 않으며, 생성 전 plan 확인과 사용 후 destroy로 비용 사고 가능성을 줄이는 수준입니다.

## 현재 한계

- 단일 VM·단일 container만 지원합니다.
- GitHub 배포는 임의 공개 저장소의 Dockerfile을 root Docker daemon으로 build하므로 신뢰한 저장소만 사용해야 합니다.
- Docker image signature, vulnerability, SBOM을 검증하지 않습니다.
- HTTPS는 이번 범위에서 제외했습니다. 미추적 Vaultwarden/Caddy/cloudflared 스크립트는 특정 개인 데모용이며 공통 배포 흐름에 포함되지 않습니다.
- health endpoint 경로는 `/`로 고정되어 있습니다.
- zone fallback은 동일 region 안에서만 허용합니다. subnet을 region별로 다시 만들지 않습니다.
- 공개 v0.1.x의 기존 state는 자동 migration·destroy 대상이 아닙니다. 새 변수 파일을 억지로 맞추지 말고 기존 configuration과 state를 별도로 검토해야 합니다.
- 바이너리의 내장 자산은 누락 파일을 준비하기 위한 bootstrap용입니다. 기존 `main.tf`나 lock 파일의 버전이 바이너리와 다른 경우 자동으로 덮어쓰거나 migration하지 않습니다.
- state lock이 없는 로컬 state이므로 동시에 두 실행을 시작하면 안 됩니다.
- HA, autoscaling, SLO, 장기 patching, backup/restore, centralized logging을 제공하지 않습니다.

## 검증 범위

로컬에서 다음을 검증했습니다.

- Go unit test와 race test
- Go vet와 gofmt
- Terraform fmt, `init -backend=false`, validate
- 입력·설정 파일·승인 게이트·zone capacity·startup/container/HTTP 실패·마스킹·state 없는 destroy·Terraform output parsing의 mock test
- 추적 파일의 민감정보 패턴과 ignored runtime 파일 검사

실제 GCP `plan`, `apply`, IAP SSH, HTTP 접근, `destroy`는 사용자 승인 없이 실행하지 않았습니다. 따라서 실제 quota, 조직 정책, IAM binding, zone capacity, 이미지별 startup 시간과 애플리케이션 동작은 아직 검증되지 않았습니다.

설계와 운영 판단의 배경은 [docs/architecture-and-operations.md](docs/architecture-and-operations.md)를 참고하세요.
