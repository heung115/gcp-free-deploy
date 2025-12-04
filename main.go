// main.go
package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
)

//go:embed main.tf
var mainTfContent string

func main() {
	if err := run(); err != nil {
		fmt.Printf("\n❌ %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 테라폼 파일 추출
	if err := extractTerraformFile(); err != nil {
		return err
	}

	// 2. 인증 설정 (하이브리드 방식)
	if err := setupAuth(); err != nil {
		return err
	}

	// 3. 타이틀 출력
	fmt.Println("================================")
	fmt.Println("🚀  GCP Free Tier Deployer  🚀")
	fmt.Println("================================")
	fmt.Println("")

	if len(os.Args) < 2 {
		printUsage()
		return nil
	}

	command := os.Args[1]

	switch command {
	case "up":
		return deployTerraform()
	case "down":
		return destroyTerraform()
	default:
		printUsage()
		return fmt.Errorf("잘못된 명령어: %s", command)
	}
}

// 인증 설정 함수
func setupAuth() error {
	keyFile := "credentials.json"
	if _, err := os.Stat(keyFile); err == nil {
		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", keyFile)
		fmt.Println("🔑 [인증] 'credentials.json' 감지됨. 키 파일 사용.")
		return nil
	}

	fmt.Println("☁️  [인증] 키 파일 없음. gcloud 확인 중...")
	if _, err := exec.LookPath("gcloud"); err != nil {
		return fmt.Errorf("❌ 인증 실패: credentials.json을 넣거나 gcloud를 설치하세요")
	}

	fmt.Println("✅ gcloud 감지됨. 시스템 인증 사용.")
	return nil
}

func printUsage() {
	fmt.Println("사용법: my-deployer [up|down]")
}
