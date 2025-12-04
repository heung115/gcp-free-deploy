### gcp-free-deploy

**GCP Free Tier에서 간단한 데모 앱을 자동으로 배포/삭제하는 Go + Terraform 툴**입니다.  
로컬에서 `go run . up` / `go run . down` 명령만으로 VM 생성, 앱 설치 로그 모니터링, 리소스 삭제까지 처리합니다.

---

### 요구사항

-   **Go** 1.20+
-   **Terraform CLI**
-   **GCP 프로젝트** 1개 (Free Tier 가능)
-   **인증 방법** (둘 중 하나)
    -   프로젝트 **서비스 계정 키 파일** `credentials.json` (프로젝트 루트에 위치)
    -   또는 로컬에 **`gcloud` 설치 + `gcloud auth login` 완료**

---

### 설치

```bash
git clone https://github.com/kimhyungho/gcp-free-deploy.git
cd gcp-free-deploy

# 의존성 정리
go mod tidy
```

---

### 사용법

#### 인프라 생성 (배포)

```bash
go run . up
```

실행 후 순서:

1. **GCP Project ID** 입력
2. **GitHub URL** 입력 (엔터 시 기본값 사용)

그 다음 Terraform이:

-   VM, 네트워크 등 인프라를 생성하고
-   VM 내부 `startup-script` 로 필요한 패키지/앱을 설치합니다.

터미널에서는 `journalctl` 로그를 실시간으로 보여주며,  
설치가 끝나면 **`설치 완료!`** 메시지가 출력되고 모니터링이 종료됩니다.

#### 인프라 삭제

```bash
go run . down
```

-   한 번 더 뜨는 경고 프롬프트에서 `y` 입력하면
-   Terraform으로 생성했던 리소스를 전부 삭제합니다.

---

### 주요 파일 설명

-   **`main.go`**

    -   엔트리 포인트
    -   인증 설정 (`setupAuth`)
    -   CLI 명령 분기 (`up` / `down`)

-   **`commands.go`**

    -   Terraform 명령 실행 (`init`, `apply`, `destroy`)
    -   `terraform.tfvars` 생성 (`project_id`, `github_repo`)

-   **`monitor.go`**

    -   배포 완료 후 VM에 `gcloud compute ssh` 접속
    -   `journalctl` 로그를 실시간 스트리밍
    -   특정 완료 로그가 나오면 설치 완료 메시지 출력 후 모니터링 종료

-   **`main.tf`**

    -   GCP 리소스 정의 (VM, 메타데이터 `startup-script` 등)

-   **`terraform.tfvars`**
    -   프로젝트 ID, GitHub 리포지토리 URL
    -   실행 시 자동 생성, **민감 정보 가능하므로 Git에 커밋 금지**
