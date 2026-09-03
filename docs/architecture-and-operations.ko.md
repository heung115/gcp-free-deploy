# 설계와 운영 판단

[English](architecture-and-operations.md)

## 목적과 비목적

이 프로젝트는 데모 애플리케이션을 위해 GCP VM을 반복 가능하게 만들고 배포 결과와 실패 증거를 확인하며, 사용자가 `gcp-free-deploy down`으로 검토된 destroy plan을 거쳐 관련 리소스를 삭제하게 하는 작은 도구다. 장기 VM 관리, 정기 patching, HA, 대규모 운영, FinOps 성과를 목표로 하지 않는다.

## 왜 patch 대신 재생성하는가

데모 VM은 지속적인 사용자 데이터나 장기 서비스 상태를 보존하지 않는다. 기존 VM에 변경을 누적하면 수동 조치와 환경 편차가 쌓이지만, 선언한 Terraform과 startup script에서 다시 만들면 시작 상태와 삭제 범위가 분명하다. 데이터 지속성이 필요한 운영 서비스라면 별도 저장소, backup, migration, patch 정책이 필요하므로 이 도구의 범위를 벗어난다.

## 책임 분리

- Terraform은 전용 network, subnet, firewall, VM, disk와 output을 정의한다.
- startup script는 최초 부팅에서 Docker를 설치하고 지정한 한 개의 애플리케이션을 실행하며 식별 가능한 상태 log를 남긴다.
- Go CLI는 JSON 입력 검증, Terraform 명령 순서, plan과 승인, 실패 분류, 안전한 destroy를 담당한다.
- monitoring은 Terraform output을 구조화해 읽고 IAP SSH로 startup·container·내부 HTTP 증거를 모은 뒤 외부 HTTP를 확인한다.
- 문서는 IAM·network·secret·state·비용 경계와 실제 검증 범위를 구분한다.

이 경계를 지키면 Terraform 안에 대화형 로직을 넣거나, Go에서 GCP 리소스를 임의로 직접 삭제하거나, startup script가 인프라를 바꾸는 책임 혼합을 피할 수 있다.

## network와 접근 결정

default VPC는 프로젝트의 기존 firewall 규칙과 결합될 수 있어 이 도구가 만든 노출 범위를 코드만 보고 판단하기 어렵다. 따라서 전용 VPC와 subnet을 사용한다.

데모를 브라우저에서 보려면 TCP/80 접근이 필요하지만, `0.0.0.0/0`이나 합쳐서 모든 IPv4 주소를 덮는 여러 CIDR은 평문 HTTP를 전체 인터넷에 공개한다. 그래서 접근 CIDR을 설정에 반드시 적고 전체 공개는 별도 CLI option으로 다시 확인한다. 접근성은 낮아지지만 실수로 인터넷에 서비스를 노출하는 위험을 줄인다.

SSH는 별도의 인터넷 전체 공개 rule을 만들지 않는다. OS Login을 활성화하고 IAP TCP forwarding 주소에서 VM 전용 tag의 22번으로만 들어오게 한다. 외부 IP는 HTTP 데모 때문에 존재하지만 관리 접속은 `--tunnel-through-iap`을 강제한다.

최초 부팅에는 APT package, Docker image, 공개 GitHub 저장소 접근이 필요하므로 outbound traffic은 기본 egress에 의존한다. 별도 egress allowlist는 이번 단일 VM 데모 범위에서 제외했으며, 애플리케이션 침해 뒤 데이터 유출이나 C2 통신을 막는 통제로 보아서는 안 된다.

## IAM과 VM identity

Terraform 실행 주체는 network, firewall, VM 관리 권한을 가진다. 모니터링 주체는 IAP tunnel과 OS Admin Login 권한을 사용한다. 프로젝트 `Owner`나 `Editor`를 요구하지 않는다.

배포 애플리케이션은 GCP API를 호출하지 않는다. 잠금 파일과 정확한 version constraint로 고정한 Google provider 7.12.0은 `service_account { scopes = [] }`를 service account와 OAuth scope를 붙이지 않는 명시적 구성으로 처리한다([provider 구현](https://github.com/hashicorp/terraform-provider-google/blob/v7.12.0/google/services/compute/resource_compute_instance.go)). 향후 Cloud API 접근이 필요하면 별도 전용 service account를 만들고 필요한 역할만 부여해야 하며, 현재 설정에 광범위 scope나 project role을 덧붙이지 않는다.

## 공급망과 startup

Docker image는 immutable digest 또는 적어도 `latest`가 아닌 명시적 tag가 바람직하다. `latest`는 허용하되 경고한다. 공개 GitHub URL은 `https://github.com/<owner>/<repo>` 형태만 허용하며 shallow clone한다. 최초 부팅은 container의 내부 HTTP 확인까지 성공한 뒤에만 완료 marker를 기록한다. 이후 부팅에서는 image를 다시 pull하거나 repository를 clone·build하지 않고 기존 container를 시작한 뒤 health check를 반복한다. 최초 배포가 실패하면 marker가 남지 않으므로 다음 부팅에서 일부 생성된 애플리케이션 상태를 정리하고 처음부터 다시 시도한다. root 소유 Docker daemon이 source를 build하고 시작하며 container process는 이미지가 지정한 사용자로 실행된다. 이미지에 `USER`가 없으면 root이므로 신뢰 경계 밖 코드를 실행하는 위험이 남는다.

Dockerfile이 없거나 build가 실패하면 배포가 실패한다. 다른 Nginx container를 띄우는 fallback은 원래 애플리케이션 실패를 성공처럼 보이게 하므로 제거했다.

## 완료와 실패 판정

startup process가 끝났다는 사실만으로 애플리케이션이 사용 가능하다고 볼 수 없다. 다음 세 증거를 함께 확인한다.

1. startup log의 `STARTUP_DONE`
2. container의 running 상태
3. VM 내부와 외부의 HTTP health check

VM은 생성됐지만 애플리케이션이 실패하면 startup service 상태, tagged startup log, container 목록, 제한된 container log, 마지막 health 결과를 수집한다. 입력과 Terraform 단계는 typed failure kind로 구분하고, runtime 단계는 startup script의 명시적 marker와 실제 상태를 함께 사용한다. 진단은 길이 제한과 일반 비밀 패턴 masking을 거치지만 모든 애플리케이션 로그 형식을 완전히 정화한다고 보장하지 않는다.

startup 시간은 VM 성능, package mirror, image registry와 zone 상태에 따라 달라지므로 고정 횟수로 성공 시점을 가정하지 않는다. 주기적으로 상태를 확인해 준비되면 즉시 종료하고, 기본 15분의 설정 가능한 wall-clock 상한으로 CLI의 무한 대기를 막는다. 상한 경계에서는 별도 context로 마지막 상태를 다시 확인하고 실패라면 제한된 진단을 수집한다. 이 timeout은 리소스를 삭제하지 않으므로 사용자가 `down`을 실행할 때까지 VM과 network에서 비용이 계속 생길 수 있다.

## apply와 destroy 승인

plan은 생성될 리소스와 변경 범위를 검토하는 마지막 경계다. 기본 apply는 저장된 plan과 명시적 `yes`가 필요하며, 자동 승인은 `--auto-approve`에서만 가능하다. HTTP 전체 공개는 자동 승인과 별개로 `--allow-public-http`가 필요하다.

destroy는 더 보수적이다. local state의 허용된 resource 주소와 `terraform show -json`의 실제 resource type/name/project/region/zone을 권한이 제한된 배포 변수 파일과 교차 확인한다. 이 검사는 VM output에 의존하지 않아 VM 전에 network나 firewall만 만들어진 부분 생성 상태도 정리할 수 있다. state가 없거나 대상이 불명확하면 실패한다. destroy 뒤 state에 리소스 주소가 남으면 성공으로 표시하지 않고 비용 위험과 확인 방법을 알린다.

`up`도 기존 local state가 있으면 plan보다 먼저 같은 안전 경계를 적용한다. 현재 resource 주소가 아닌 legacy·unrelated 주소가 있거나 기존 배포와 요청한 project·region·zone이 다르면 중단한다. state 파일을 삭제해 우회하지 않고 실제 GCP 리소스를 확인한 뒤 명시적으로 migration하거나 별도 작업 폴더로 격리해야 한다.

local state를 쓰므로 `init`, `validate`, `up`, `down`은 같은 작업 폴더에서 실행해야 한다. CLI 명령끼리는 폴더별 process lock으로 직렬화하지만 remote state나 팀 단위 lock은 제공하지 않는다. 배포별로 빈 폴더를 따로 쓰되, resource 이름이 고정되어 있으므로 하나의 GCP project에는 활성 배포 하나만 둔다.

workspace나 state 명령 전에 CLI는 관리 대상 구성으로 `terraform init -reconfigure`를 실행한다. 이 구성에는 remote backend block이 없으므로 작업 폴더에 남은 예전 backend metadata가 state 작업을 다른 위치로 돌리지 못한다.

## 릴리스 실행 모델

릴리스 바이너리는 Terraform 구성, provider lock, example 설정을 내장한다. `init`, `validate`, `up`, `down`은 실행 폴더에 누락된 자산만 materialize하며 사용자 파일을 덮어쓰지 않는다. plan·apply·destroy 전에는 관리 대상 파일의 변경이나 최상위 경로의 추가 Terraform 파일을 거부해 문서화된 소유권·정리 경계를 유지한다. 기존 구성 버전은 자동 migration하지 않는다. 빌드 대상은 고정 provider 7.12.0이 제공되는 Linux amd64/arm64, macOS amd64/arm64, Windows amd64로 제한한다.

새 배포는 실행 중인 릴리스에 내장된 자산과 정확히 일치해야 한다. `down`만 정리 호환성이 검토된 이전 untagged 자산 checksum을 제한적으로 허용하며, 임의 변경이나 legacy 자산은 계속 거부한다.

활성 배포에 사용한 바이너리와 작업 폴더는 정리가 끝날 때까지 보관한다. 새 바이너리가 버전에 묶인 자산을 거부하면 원래 바이너리와 파일로 `down`을 실행하며, state만 새로 초기화한 폴더에 복사하지 않는다.

태그된 `v0.1.3` 릴리스의 legacy state는 호환 대상이 아니다. `v0.2.0`으로 올리기 전에 원래 바이너리와 작업 폴더로 해당 배포를 정리한다.

## 비용과 운영 확장

작은 machine type과 disk, 임시 IP를 사용하지만 Free Tier나 무과금을 보장하지 않는다. 실제 비용 성과를 주장하지 않으며 plan 확인과 destroy 습관을 비용 안전장치로 사용한다.

운영 환경으로 확장하려면 최소한 TLS와 도메인, 인증·secret manager, remote state locking/encryption, CI identity federation, image provenance와 vulnerability scan, patch/base image 정책, centralized logging/metrics, backup/restore, health endpoint 설정, SLO와 alerting, rolling update 또는 HA가 더 필요하다. 이 항목들을 현재 프로젝트가 구현하거나 증명한다고 표현하면 안 된다.

## 검증 증거의 경계

unit/mock test와 Go/Terraform 정적 검증에 더해 격리된 local state에서 실제 GCP resource lifecycle을 검증했다. 2026-08-12에는 `us-central1`의 e2-micro capacity 실패에서 같은 region fallback과 VM 없는 부분 생성 state의 삭제를 확인했고, `us-west1-a`에서는 전용 VPC·VM 생성, IAP SSH, Docker image 배포, container·내부/외부 HTTP 확인, 전체 destroy와 잔여 리소스 없음까지 확인했다. 2026-08-13에는 startup monitor를 설정 가능한 wall-clock 상한으로 바꾼 뒤 기본 15분 상한에서 약 7분 9초에 준비 상태를 감지해 CLI가 즉시 성공 종료하는 경로를 재검증했다. 같은 프로젝트의 관련 없는 기존 workload는 검증 전후 계속 실행 상태였다. 이 결과는 해당 시점의 임시 데모 생명주기 검증 근거이지만, 지속적인 운영 안정성·비용 절감·무중단·고가용성 근거로 확대하지 않는다.

## 면접 대비 질문

1. **왜 VM을 계속 patch하지 않고 재생성하는가?** 데모 환경은 보존할 상태가 적어 선언된 구성으로 다시 만드는 편이 변경 누적과 환경 편차를 줄이기 때문이다.
2. **Terraform과 Go CLI의 책임을 왜 분리했는가?** Terraform은 리소스 상태를, Go는 입력 검증·승인·실패 분류 같은 실행 절차를 담당하게 해 경계를 분명히 했다.
3. **왜 default VPC를 사용하지 않았는가?** 기존 firewall rule의 영향을 피하고 이 도구가 만든 네트워크 노출 범위를 코드만으로 확인하기 위해서다.
4. **왜 전체 공개 HTTP에 별도 option이 필요한가?** `0.0.0.0/0`의 위험을 사용자가 적용 전에 명시적으로 인지하게 하기 위해서다.
5. **왜 VM service account가 없는가?** 배포 애플리케이션이 GCP API를 호출하지 않아 VM credential과 cloud API 권한이 필요하지 않기 때문이다.
6. **왜 plan을 저장한 뒤 적용하는가?** 사용자가 검토한 계획과 실제 적용 대상을 동일하게 유지하기 위해서다.
7. **왜 startup 종료만으로 성공 처리하지 않는가?** 프로세스가 끝나도 container나 애플리케이션은 실패할 수 있어 log·container·내부/외부 HTTP 상태를 함께 확인한다.
8. **zone fallback을 같은 region으로 제한한 이유는?** 기존 regional subnet을 유지하면서 capacity 부족만 우회하기 위해서다.
9. **state 혼입을 어떻게 막는가?** `up`과 `destroy` 모두 default workspace와 허용된 state 주소를 확인한다. `destroy`는 state JSON의 실제 resource type/name/project/region/zone과 변수 파일까지 교차 확인하며, `up`은 기존 완전 배포 output·변수 파일과 요청 대상을 비교해 불명확하면 plan 전에 중단한다.
10. **운영 환경으로 확장할 때 무엇을 먼저 추가할 것인가?** TLS·인증, remote state locking, CI identity federation, 공급망 검증, 중앙 모니터링을 위험도에 따라 추가한다.
