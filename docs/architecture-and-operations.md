# Architecture and operations decisions

## 목적과 비목적

이 프로젝트는 데모 애플리케이션을 위해 GCP VM을 반복 가능하게 만들고, 배포 결과와 실패 증거를 확인하고, 사용 뒤 관련 리소스를 삭제하는 작은 도구다. 장기 VM 관리, 정기 patching, HA, 대규모 운영, FinOps 성과를 목표로 하지 않는다.

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

데모를 브라우저에서 보려면 TCP/80 접근이 필요하지만, 기본 `0.0.0.0/0`은 모든 IPv4 주소에 평문 HTTP를 공개한다. 그래서 접근 CIDR을 설정에 반드시 적고 전체 공개는 별도 CLI option으로 다시 확인한다. 접근성은 낮아지지만 실수로 인터넷에 서비스를 노출하는 위험을 줄인다.

SSH는 별도의 인터넷 전체 공개 rule을 만들지 않는다. OS Login을 활성화하고 IAP TCP forwarding 주소에서 VM 전용 tag의 22번으로만 들어오게 한다. 외부 IP는 HTTP 데모 때문에 존재하지만 관리 접속은 `--tunnel-through-iap`을 강제한다.

최초 부팅에는 APT package, Docker image, 공개 GitHub 저장소 접근이 필요하므로 outbound traffic은 기본 egress에 의존한다. 별도 egress allowlist는 이번 단일 VM 데모 범위에서 제외했으며, 애플리케이션 침해 뒤 데이터 유출이나 C2 통신을 막는 통제로 보아서는 안 된다.

## IAM과 VM identity

Terraform 실행 주체는 network, firewall, VM 관리 권한을 가진다. 모니터링 주체는 IAP tunnel과 OS Admin Login 권한을 사용한다. 프로젝트 `Owner`나 `Editor`를 요구하지 않는다.

배포 애플리케이션은 GCP API를 호출하지 않는다. 잠금 파일과 정확한 version constraint로 고정한 Google provider 7.12.0은 `service_account { scopes = [] }`를 service account와 OAuth scope를 붙이지 않는 명시적 구성으로 처리한다([provider 구현](https://github.com/hashicorp/terraform-provider-google/blob/v7.12.0/google/services/compute/resource_compute_instance.go)). 향후 Cloud API 접근이 필요하면 별도 전용 service account를 만들고 필요한 역할만 부여해야 하며, 현재 설정에 광범위 scope나 project role을 덧붙이지 않는다.

## 공급망과 startup

Docker image는 고정 tag 또는 digest가 바람직하다. `latest`는 허용하되 경고한다. 공개 GitHub URL은 `https://github.com/<owner>/<repo>` 형태만 허용하며 shallow clone한다. 그래도 repository Dockerfile은 VM의 root Docker daemon에서 실행되므로 신뢰 경계 밖 코드를 실행하는 위험이 남는다.

Dockerfile이 없거나 build가 실패하면 배포가 실패한다. 다른 Nginx container를 띄우는 fallback은 원래 애플리케이션 실패를 성공처럼 보이게 하므로 제거했다.

## 완료와 실패 판정

startup process가 끝났다는 사실만으로 애플리케이션이 사용 가능하다고 볼 수 없다. 다음 세 증거를 함께 확인한다.

1. startup log의 `STARTUP_DONE`
2. container의 running 상태
3. VM 내부와 외부의 HTTP health check

VM은 생성됐지만 애플리케이션이 실패하면 startup service 상태, tagged startup log, container 목록, 제한된 container log, 마지막 health 결과를 수집한다. 입력과 Terraform 단계는 typed failure kind로 구분하고, runtime 단계는 startup script의 명시적 marker와 실제 상태를 함께 사용한다. 진단은 길이 제한과 일반 비밀 패턴 masking을 거치지만 모든 애플리케이션 로그 형식을 완전히 정화한다고 보장하지 않는다.

## apply와 destroy 승인

plan은 생성될 리소스와 변경 범위를 검토하는 마지막 경계다. 기본 apply는 저장된 plan과 명시적 `yes`가 필요하며, 자동 승인은 `--auto-approve`에서만 가능하다. HTTP 전체 공개는 자동 승인과 별개로 `--allow-public-http`가 필요하다.

destroy는 더 보수적이다. local state의 허용된 resource 주소, Terraform output의 project·VM·zone, 권한이 제한된 배포 변수 파일을 모두 확인한다. output과 변수 파일의 project·region·zone이 일치할 때만 destroy plan을 만들고 승인받는다. state가 없거나 대상이 불명확하면 실패한다. destroy 뒤 state에 리소스 주소가 남으면 성공으로 표시하지 않고 비용 위험과 확인 방법을 알린다.

## 비용과 운영 확장

작은 machine type과 disk, 임시 IP를 사용하지만 Free Tier나 무과금을 보장하지 않는다. 실제 비용 성과를 주장하지 않으며 plan 확인과 destroy 습관을 비용 안전장치로 사용한다.

운영 환경으로 확장하려면 최소한 TLS와 도메인, 인증·secret manager, remote state locking/encryption, CI identity federation, image provenance와 vulnerability scan, patch/base image 정책, centralized logging/metrics, backup/restore, health endpoint 설정, SLO와 alerting, rolling update 또는 HA가 더 필요하다. 이 항목들을 현재 프로젝트가 구현하거나 증명한다고 표현하면 안 된다.

## 검증 증거의 경계

현재 repository에서 unit/mock test와 Go/Terraform 정적 검증은 수행했다. 실제 GCP resource lifecycle은 수행하지 않았다. 따라서 “Terraform 자동화 흐름을 구현하고 로컬에서 검증했다”는 사실은 말할 수 있지만, “실제 운영 안정성을 확보했다”, “비용을 절감했다”, “무중단·고가용성을 구현했다”, “실제 장애를 복구했다”는 표현은 근거가 없다.

## 면접 대비 질문

1. **왜 VM을 계속 patch하지 않고 재생성하는가?** 데모 환경은 보존할 상태가 적어 선언된 구성으로 다시 만드는 편이 변경 누적과 환경 편차를 줄이기 때문이다.
2. **Terraform과 Go CLI의 책임을 왜 분리했는가?** Terraform은 리소스 상태를, Go는 입력 검증·승인·실패 분류 같은 실행 절차를 담당하게 해 경계를 분명히 했다.
3. **왜 default VPC를 사용하지 않았는가?** 기존 firewall rule의 영향을 피하고 이 도구가 만든 네트워크 노출 범위를 코드만으로 확인하기 위해서다.
4. **왜 전체 공개 HTTP에 별도 option이 필요한가?** `0.0.0.0/0`의 위험을 사용자가 적용 전에 명시적으로 인지하게 하기 위해서다.
5. **왜 VM service account가 없는가?** 배포 애플리케이션이 GCP API를 호출하지 않아 VM credential과 cloud API 권한이 필요하지 않기 때문이다.
6. **왜 plan을 저장한 뒤 적용하는가?** 사용자가 검토한 계획과 실제 적용 대상을 동일하게 유지하기 위해서다.
7. **왜 startup 종료만으로 성공 처리하지 않는가?** 프로세스가 끝나도 container나 애플리케이션은 실패할 수 있어 log·container·내부/외부 HTTP 상태를 함께 확인한다.
8. **zone fallback을 같은 region으로 제한한 이유는?** 기존 regional subnet을 유지하면서 capacity 부족만 우회하기 위해서다.
9. **destroy 전에 무엇을 확인하는가?** 허용된 state 주소와 output·변수 파일의 project, region, zone이 일치하는지 확인하고 별도 승인을 받는다.
10. **운영 환경으로 확장할 때 무엇을 먼저 추가할 것인가?** TLS·인증, remote state locking, CI identity federation, 공급망 검증, 중앙 모니터링을 위험도에 따라 추가한다.
