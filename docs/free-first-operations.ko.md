# 무료 사용을 우선하는 운영 순서

이 도구는 무료 대상에서 벗어난 구성을 줄이지만, 계정 전체 과금을 0원으로
고정하지는 않습니다. 불필요한 데모는 짧게 실행하고 사용이 끝나면 정리하세요.

## 새 배포

1. 계정 전체에서 기존 VM·디스크·IP 사용량을 확인합니다. 프로젝트를 새로 만들어도
   무료량이 추가되지 않습니다.
2. `e2-micro`, `us-west1`·`us-central1`·`us-east1`, `pd-standard`를 사용합니다.
   디스크 무료량은 계정 전체 30 GB-month이므로 필요한 만큼만 할당합니다.
3. `max_runtime_hours`를 `24`처럼 짧게 지정하고 `cost`와 Terraform plan을 확인합니다.
4. `up`을 실행합니다. 기본적으로 실제 VM의 Standard 적용까지 확인한 뒤 상태 검사와
   성공 보고로 넘어갑니다. 무료 사용이 우선이면 `--allow-paid-resources`를 사용하지 마세요.
5. 사용이 끝나면 원래 작업 폴더에서 `down`을 실행해 삭제 내역을 확인합니다.

```bash
gcp-free-deploy cost
gcp-free-deploy up --plan-only
gcp-free-deploy up
# 사용이 끝난 뒤
gcp-free-deploy down
```

`up`은 적용할 계획을 만들기 전에 프로젝트의 다른 VM을 확인합니다. 구버전 배포 등이
남아 있으면 두 번째 VM을 생성하지 않고 중단합니다. 조회 실패도 중단하며,
`--plan-only`와 명시적인 `--allow-paid-resources`만 이 검사를 건너뜁니다.

`max_runtime_hours`는 생략 또는 `0`이면 무제한이고, `1`–`168` 정수를 받습니다.
VM을 시작한 시점부터 지정 시간이 지나면 중지하며, 재시작하면 시간이 새로 계산됩니다.
**중지는 삭제가 아니므로 디스크가 남습니다.** 실행 중 발생한 트래픽도 취소되지 않습니다.
이 설정은 잊고 켜 둔 데모를 줄이는 기능이며 예산 상한이 아닙니다.

## 이미 사용 중인 VM

설정 파일의 Standard와 실제 VM의 Standard는 따로 확인해야 합니다.
다음 조회에는 `gcloud` 로그인과 Compute Engine 읽기 권한이 필요합니다.
세 대상 옵션을 모두 명시하며 로컬 Terraform state는 없어도 됩니다.

```bash
gcp-free-deploy audit --project YOUR_PROJECT_ID --vm YOUR_VM_NAME --zone us-central1-a
```

조회는 리소스를 변경하지 않습니다. 실제 VM 종류·리전·외부 네트워크 등급,
프로젝트 디스크 종류·용량·연결 여부, VM 수를 확인합니다. Premium이나 필요한 정보의
누락, 점검 범위 밖 구성은 오류로 처리합니다. VM이 여러 대면 무료 실행 시간을 나눠
소모할 수 있다고 알립니다. 과거 월 사용량, 다른 프로젝트, 다른 서비스나 실제 청구액은
점검하지 않으므로 통과를 무료 확정으로 해석하지 마세요.

옛 템플릿의 `access_config {}`에는 등급 지정이 없어서 기본 Premium이 적용됩니다.
나중에 코드에 `network_tier = "STANDARD"`를 넣어도 이미 만든 VM은 자동으로 바뀌지
않습니다. 등급을 바꿀 때는 현재 연결과 공인 IP 변경 영향을 확인하고 해당 리소스만
수정해야 합니다. 구버전 state에 최신 템플릿을 적용해 네트워크 등급을 바꾸려 하지 마세요.
이름·VPC 등도 달라져 의도하지 않은 생성이나 교체가 계획될 수 있습니다.

이번 실행 시간 기능도 관리 템플릿을 변경합니다. 기존 CLI·`main.tf`·작업 폴더는
정리까지 보관하세요. 파일은 자동으로 덮어쓰지 않으며, 새 CLI가 관리 파일 불일치로
중단하면 원래 바이너리와 폴더에서 `down`을 실행합니다. `audit`는 이와 별개로 사용할
수 있습니다. 새 실행 시간 설정이 기존 VM에 자동 적용된다고 가정하지 마세요.

## 트래픽과 IP는 별도 비용

Standard의 현재 미국 출발 인터넷 송신 무료 구간은 계정당 월 200 GiB입니다.
Premium의 무료 전송량을 여기에 더하지 않습니다. 다른 프로젝트의 송신도 합산해야 하며
이 도구는 200 GiB에서 통신을 자동 차단하지 않습니다.

외부 IPv4 요율은 등급을 Standard로 바꿔도 별도입니다. 2026-09-07 가격 API 관찰값은
계정당 월 **720 주소·시간 무료, 초과 시간당 $0.005**입니다. 31일 내내 주소 하나를
쓰면 744시간이므로 같은 요율과 다른 사용량이 없다는 조건에서도 초과분이 생깁니다.
현재 관찰값을 영구 무료 조건으로 간주하지 마세요. 공식 페이지 표기 차이와 근거는
[비용 안내](costs.md)에 정리했습니다.

결제 보고서는 SKU별 사용 비용과 크레딧을 함께 확인하세요. 표준 예산 알림은 지출을
자동으로 막지 않으며, 청구 반영 지연 때문에 삭제 직후의 0원도 최종 청구액은 아닙니다.


## 무료 이메일 비용 알림

Cloud Billing 예산과 기본 이메일 알림은 무료입니다. **일반 사용자는 `up`만 실행하면 됩니다.**
배포 승인 뒤 VM 적용 전에 연결된 청구 계정과 현재 로그인 이메일을 찾아 필요한 API,
이메일 채널, 월 예산을 자동으로 준비합니다. 기존 배포의 `up`에서도 알림을 확인합니다.
예산 설정 실패 시 적용을 중단하며, 이미 만든 API·알림과 기존 서버는 그대로 남습니다.
계획 확인·배포 취소에는 알림 변경이 없습니다. 자동화 계정 등 별도 알림 관리가 있는 환경만
`--skip-budget-alerts`를 사용할 수 있습니다. `down`은 뒤늦게 반영되는 비용 알림을 받도록
예산과 이메일 채널을 남깁니다.

아래 `budget` 명령은 알림을 별도로 관리할 때 사용합니다.
청구 계정 통화의 **1 단위**를 월 예산으로 잡고 실제 비용이 1%·10%·100%에 도달하면
알립니다. USD 계정은 $0.01·$0.10·$1, KRW 계정은 0.01원·0.1원·1원 기준입니다.
금액의 통화를 확인하고 필요하면 `--amount 100`처럼 조정하세요. KRW 계정에서
`--amount 100`을 쓰면 1원·10원·100원 알림이 됩니다.

```bash
# 조회만 수행하고 생성할 설정을 확인
gcp-free-deploy budget --project YOUR_PROJECT_ID --billing-account YOUR_BILLING_ACCOUNT --dry-run
# 필요한 무료 Budget API 활성화와 예산 생성
gcp-free-deploy budget --project YOUR_PROJECT_ID --billing-account YOUR_BILLING_ACCOUNT --enable-api
```

`gcloud` 로그인, 프로젝트 조회·청구 연결 조회·예산 조회/생성 권한이 필요합니다.
`--enable-api`에는 프로젝트 API 활성화 권한도 필요합니다. 예산은 해당 프로젝트의
모든 서비스 비용을 월별로 합산하고 무료 할인과 크레딧을 적용합니다. 시험 크레딧으로
상쇄되는 비용은 알림 기준에 도달하지 않을 수 있습니다. 다른 프로젝트 비용은 제외됩니다.

기본 수신자는 **청구 계정 관리자·사용자 역할 보유자**입니다. 프로젝트 소유자나 현재
로그인 사용자라는 이유만으로 이메일 수신이 보장되지는 않습니다. 특정 이메일로
받으려면 Cloud Monitoring의 이메일 알림 채널을 만들고 다음 옵션을 함께 사용하세요.

```bash
gcp-free-deploy budget --project YOUR_PROJECT_ID --billing-account YOUR_BILLING_ACCOUNT \
  --notification-channel projects/YOUR_PROJECT_ID/notificationChannels/CHANNEL_ID
```

동일한 관리 이름과 설정의 예산은 재사용하며, 기존 예산 설정이 다르면 덮어쓰지 않고
중단합니다. 개인이 만든 다른 예산은 보존합니다. 알림을 받으려고 Pub/Sub, Functions,
BigQuery 또는 Monitoring 메트릭 알림 정책을 추가할 필요는 없습니다.

알림 설정 저장과 이메일 실제 수신은 별개입니다. 첫 알림까지 수 시간이 걸릴 수 있고
사용 비용 반영도 지연됩니다. 비용을 일부러 발생시켜 테스트하지 마세요. 예산은
Compute Engine 지출을 자동 차단하지 않습니다. 현재 미리보기 Spend cap 역시
Compute Engine VM·디스크의 고정 사용 비용을 멈추는 기능이 아닙니다.

근거: [Cloud Billing 가격](https://cloud.google.com/billing/v1/pricing),
[예산·알림 및 반영 지연](https://docs.cloud.google.com/billing/docs/how-to/budgets),
[Spend cap 지원 범위](https://docs.cloud.google.com/billing/docs/how-to/budgets-spend-caps).
