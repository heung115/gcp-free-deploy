// commands.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// 테라폼 파일 추출
func extractTerraformFile() error {
	err := os.WriteFile("main.tf", []byte(mainTfContent), 0644)
	if err != nil {
		return fmt.Errorf("추출 실패: %w", err)
	}
	return nil
}

// 배포 함수 (UP)
func deployTerraform() error {
	fmt.Println("테라폼 배포 모드(UP) 시작")
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("👉 GCP Project ID: ")
	projectID, _ := reader.ReadString('\n')
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("프로젝트 ID 필수")
	}

	fmt.Print("👉 GitHub URL (엔터 시 기본값): ")
	repoURL, _ := reader.ReadString('\n')
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		repoURL = "https://github.com/googlecodelabs/container-engine-hello.git"
	}

	tfvarsContent := fmt.Sprintf("project_id = \"%s\"\ngithub_repo = \"%s\"\n", projectID, repoURL)
	os.WriteFile("terraform.tfvars", []byte(tfvarsContent), 0644)

	fmt.Println("📦 테라폼 초기화...")
	if !runCommand("terraform", "init") {
		return fmt.Errorf("초기화 실패")
	}

	fmt.Println("\n☁️  인프라 생성 시작! (약 2~3분 소요)")
	if !runCommand("terraform", "apply", "-auto-approve") {
		return fmt.Errorf("배포 실패")
	}

	fmt.Println("\n✅ 인프라 생성 완료! 이제 내부 설치 로그를 연결합니다.")

	monitorInstallation(projectID)

	return nil
}

// 삭제 함수 (DOWN)
func destroyTerraform() error {
	fmt.Println("테라폼 삭제 모드(DOWN)")

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\n🔥 [경고] 정말로 모든 인프라를 삭제하시겠습니까?")
	fmt.Print("👉 'y'를 입력하여 확인: ")
	input, _ := reader.ReadString('\n')
	if strings.TrimSpace(input) != "y" && strings.TrimSpace(input) != "Y" {
		fmt.Println("❌ 취소됨")
		return nil
	}

	if !runCommand("terraform", "destroy", "-auto-approve") {
		return fmt.Errorf("삭제 실패")
	}
	fmt.Println("✅ 삭제 완료!")
	return nil
}

// 명령어 실행 헬퍼
func runCommand(command string, args ...string) bool {
	cmd := exec.Command(command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	return err == nil
}
