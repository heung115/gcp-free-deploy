# GCP 사용량·트래픽 과금 조사

확인일: 2026-09-07. 공식 공개 요금표와 프로젝트 코드를 비교하고, 추가로
공개·계정별 가격 API를 조회했다. 이 문서에는 실제 청구 내역이나 개인 사용량을 포함하지 않는다.
아래 금액은 USD이며 세금, 환율, 개별 크레딧·계약 할인은 제외한다.

## 이 프로젝트에 적용되는 기준

`main.tf`는 VM 한 대, `pd-standard` 부팅 디스크, 임시 외부 IPv4,
명시적인 `network_tier = "STANDARD"`를 사용한다.

| 항목 | 계산 기준 | 무료 범위와 초과 조건 |
| --- | --- | --- |
| VM | 실행한 시간 | us-west1/us-central1/us-east1의 비선점형 e2-micro를 월 한 대분의 시간까지. 같은 결제 계정의 대상 VM 시간을 합산 |
| 디스크 | 할당 용량 × 유지 시간 | 대상 standard persistent disk 30 GB-month. 실제 저장한 파일 크기가 아니라 할당 크기 기준 |
| 인터넷에서 VM으로 수신 | 전송 데이터 | 수신 전송 자체는 무료. 별도 처리 서비스의 비용은 다를 수 있음 |
| VM에서 인터넷으로 송신 | Standard Tier 전송량 | 월 200 GiB까지 계정 전체·전체 리전 합산 무료. 미국 출발 기준 이후 총 10,240 GiB 구간까지 $0.085/GiB |
| 사용 중인 외부 IPv4 | 주소 사용 시간 합계 | 가격 API에서 계정당 월 720시간 무료, 이후 $0.005/시간 확인. VPC 표와의 충돌은 남아 있음 |

VM 무료 조건과 계정 전체 합산은 [Free Tier 문서](https://docs.cloud.google.com/free/docs/free-cloud-features#compute),
실행 시간 기준은 [VM 요금표](https://cloud.google.com/products/compute/pricing),
할당 디스크 기준은 [디스크 요금표](https://cloud.google.com/compute/disks-image-pricing)를 따른다.
트래픽은 [Network Service Tiers 요금표](https://cloud.google.com/network-tiers/pricing)와
[VPC 요금표](https://cloud.google.com/vpc/network-pricing)를 대조했다.

## 1GB와 200GiB라는 설명이 모두 나오는 이유

Compute Engine Free Tier 안내에는 북미발 송신 1GB(중국·호주 제외)가 있다.
Network Service Tiers 요금표는 이 Always Free 혜택이 Premium Tier에 적용되고
Standard Tier에는 적용되지 않는다고 구분한다. 대신 Standard에는 별도 월 200GiB
0원 구간이 있다. 따라서 이 프로젝트를 1GB 한도로 설명하거나 201GB 무료로
합산하는 것은 부정확하다. 단위도 GB와 GiB를 구별해야 한다.

Premium의 목적지별 가격도 다르다. 확인 당시 Iowa 출발 요금표에서 한국은
$0.19/GiB 구간에 속하고, 일부 다른 목적지와 달리 그 행에는 첫 1GiB 0원 구간이
표시되지 않는다. Free Tier 요약의 국가 제외 설명만으로 한국행 청구를 단정하지
말아야 한다. 현재 프로젝트의 Standard 요금은 출발 리전 기준이다.

## 실제 트래픽 예

- VM이 Docker 이미지나 패키지를 내려받는 데이터: VM으로 들어오는 수신.
- 사용자가 웹페이지·이미지·파일을 받아가는 데이터: VM이 보내는 송신.
- VM에서 외부 저장소로 백업 파일을 업로드: 송신.
- 방문자뿐 아니라 봇, 모니터링 요청에 대한 응답도 송신량에 포함.

미국 출발 Standard 인터넷 송신만 있고 다른 계정 사용량이 없다고 가정하면,
50GiB와 200GiB는 전송료 $0, 250GiB는 `(250−200)×0.085 = $4.25`,
1,000GiB는 `(1,000−200)×0.085 = $68`이다. IP·디스크·VM 비용은 별도다.
서로 다른 GCP 존·리전·서비스 사이의 통신에는 다른 표가 적용될 수 있으므로
이 계산을 모든 네트워크 전송에 적용하면 안 된다.

## 외부 IP 설명 정정

처음 확인한 두 공개 화면은 서로 달랐다.

- [VPC 가격표](https://cloud.google.com/vpc/network-pricing#ipaddress): 계정당 월 1시간 무료, 이후 $0.005/시간.
- [SKU C054-7F72-A02E 카탈로그](https://cloud.google.com/skus?currency=USD&filter=C054-7F72-A02E): 0~1 month 구간 $0/시간, 이후 $0.005/시간.

같은 날 추가로 [공개 가격 API](https://cloudbilling.googleapis.com/v1beta/skus/C054-7F72-A02E/price?currencyCode=USD)와
인증된 계정별 가격 API를 조회한 결과, 둘 다 **0~720시간은 $0, 720시간부터
$0.005/시간**으로 응답했다. 단위는 `h`, 수량은 `1`, 합산 범위는 계정 단위·월 단위다.
계정별 응답의 계약 가격도 목록 가격과 같았고 가격 사유는 `default-price`였다.
따라서 이번에 관측한 가격의 무료 기준은 **월 720 주소·시간**으로 확인했다.
월 길이에 따라 744시간까지 늘어나는 혜택으로 해석하면 안 된다.
[민감 정보 없는 관측 기록](evidence/ipv4-sku-2026-09-07.md)

같은 가격 구간이 해당 기간 내내 적용되고 다른 IP 사용량이 무료량을 소모하지 않는다면,
IP 하나를 30일(720시간) 유지한 요금은 **$0**, 31일(744시간)은
`(744−720)×0.005 = $0.12`다. 세금과 크레딧 적용 전 예시다.
Standard/Premium 선택은 IP 요금을 바꾸지 않는다.

VPC 페이지의 1시간 표기와 API 사이의 충돌은 남아 있다. 계정별 가격 응답은
실제 사용량·청구 내역이 아니므로, 확정 청구액은 Billing에서 해당 SKU의 사용량과
크레딧을 확인해야 한다. 가격 조회만으로 무료량 잔여분이나 최종 청구액을 알 수는 없다.

임시 IP는 VM 중지·삭제 시 해제되지만 디스크는 남으면 계속 용량·시간을 사용한다.
고정 IP는 중지한 VM에 붙어 있어도 사용 중으로 간주되고, 분리해 예약만 유지하면
다른 요율이 적용된다. 이 프로젝트는 임시 IP를 사용한다.

## 실제 청구를 판별하는 방법

Billing Reports에서 대상 기간·프로젝트를 선택하고 SKU별로 그룹화한다.
VM CPU/RAM, persistent disk, 외부 IP, Standard 인터넷 전송을 각각 확인하고
사용 비용과 크레딧·할인 이후 금액을 구분한다. 무료 한도 소진 여부는 다른 프로젝트도
포함한 결제 계정 범위에서 확인해야 한다.

요금 데이터는 보통 하루 안에 나오지만 24시간 이상 지연될 수 있다. 배포 직후
0원이라는 사실만으로 무료를 입증할 수 없다.
[공식 Billing Reports 안내](https://docs.cloud.google.com/billing/docs/how-to/reports)

CLI의 `cost`는 설정 적합성만 검사한다. 실제 사용량 조회, 청구액 확정, 트래픽
자동 차단 또는 지출 상한 보장 기능은 아니다.
