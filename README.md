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

### 바이너리(릴리즈)로 사용하기

이 프로젝트는 Go 소스 코드를 직접 실행하는 것 외에, **GitHub Releases에 올려진 빌드된 실행 파일(바이너리)** 로도 사용할 수 있습니다.

-   **1단계**: 레포지토리의 **Releases 페이지**에서 본인 OS에 맞는 바이너리 파일을 다운로드합니다.
-   **2단계**: 실행 권한 부여 (macOS / Linux)

    ```bash
    chmod +x gcp-free-deploy
    ```

-   **3단계**: 위의 사용법과 동일하게 실행
    -   macOS / Linux: `./gcp-free-deploy up`
    -   Windows: `gcp-free-deploy.exe up`

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

---

### GCP 프로젝트 및 서비스 계정 키 파일 준비하기

#### 1. GCP 프로젝트 생성하기

1. 브라우저에서 [Google Cloud Console](https://console.cloud.google.com/) 에 접속합니다.
2. 상단 프로젝트 선택 드롭다운을 클릭하고 **"새 프로젝트"** 를 선택합니다.
3. 프로젝트 이름을 입력하고, 결제 계정을 연결합니다. (Free Tier 사용 시 무료 크레딧으로 자동 연결될 수 있습니다)
4. **프로젝트 ID** 를 확인합니다.
    - 이 값이 나중에 `go run . up` 실행 시 입력하는 **Project ID** 입니다.

#### 2. 서비스 계정 생성하기

1. Cloud Console에서 상단 프로젝트가 방금 만든 프로젝트로 선택되어 있는지 확인합니다.
2. 좌측 메뉴에서 **"IAM 및 관리자" → "서비스 계정"** 메뉴로 이동합니다.
3. **"서비스 계정 만들기"** 버튼을 클릭합니다.
4. 서비스 계정 이름을 입력합니다. (예: `terraform-deployer`)
5. **역할(Role)** 은 예시로 다음과 같이 부여할 수 있습니다.
    - `Editor` (데모/개인 프로젝트용, 권한이 넉넉함)
    - 실제 운영 환경에서는 더 좁은 권한으로 구성하는 것을 권장합니다.
6. 나머지 단계는 기본값으로 두고 **"완료"** 를 눌러 서비스 계정을 생성합니다.

#### 3. 서비스 계정 키 파일(JSON) 발급하기

1. 방금 만든 서비스 계정 목록에서 해당 계정을 클릭합니다.
2. 상단 탭에서 **"키"** 를 선택합니다.
3. **"키 추가" → "새 키 만들기"** 를 클릭합니다.
4. 키 유형으로 **JSON** 을 선택하고 **"만들기"** 를 누릅니다.
5. 브라우저에서 `something.json` 파일이 다운로드됩니다.

#### 4. 키 파일을 프로젝트에 배치하기

1. 방금 다운로드한 JSON 파일의 이름을 **`credentials.json`** 으로 변경합니다.
2. 이 래포지토리를 클론한 디렉토리 (`gcp-free-deploy`)의 **루트 경로**에 `credentials.json` 파일을 복사합니다.
3. `.gitignore` 에 이미 포함되어 있으므로, Git에 커밋되지 않도록 안전하게 유지됩니다.

이제 이 저장소 루트에 `credentials.json` 이 있는 상태에서:

```bash
go run . up
```

명령을 실행하면, 서비스 계정 키 파일을 사용해 자동으로 GCP에 인증한 뒤 인프라를 생성합니다.
