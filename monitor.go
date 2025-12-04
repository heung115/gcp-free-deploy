// monitor.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 테라폼 Output 구조체
type TfOutput struct {
	VmName struct {
		Value string `json:"value"`
	} `json:"vm_name"`
	VmZone struct {
		Value string `json:"value"`
	} `json:"vm_zone"`
	WebsiteURL struct {
		Value string `json:"value"`
	} `json:"website_url"`
}

type logMonitor struct {
	w           *os.File
	cmd         *exec.Cmd
	done        chan struct{}
	onFirstByte func()
	triggered   bool
	targetLog   string
}

func (l *logMonitor) Write(p []byte) (n int, err error) {
	if !l.triggered && len(p) > 0 {
		l.triggered = true
		l.onFirstByte()
	}
	logContent := string(p)
	if strings.Contains(logContent, l.targetLog) {

		n, err = l.w.Write(p)

		fmt.Println("설치 완료!")

		select {
		case <-l.done:
		default:
			close(l.done)

			return n, err
		}
	}
	return l.w.Write(p)
}

// 지능형 모니터링 함수
func monitorInstallation(projectID string) {
	done := make(chan struct{})
	cmdDone := make(chan error)

	// 1. 테라폼 정보 가져오기
	cmd := exec.Command("terraform", "output", "-json")
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("⚠️  정보 가져오기 실패.")
		return
	}

	var tfOut TfOutput
	if err := json.Unmarshal(output, &tfOut); err != nil {
		fmt.Println("⚠️  정보 파싱 실패.")
		return
	}

	vmName := tfOut.VmName.Value
	vmZone := tfOut.VmZone.Value

	// 2. 스피너 설정
	stopSpinner := make(chan bool)
	updateStatus := make(chan string)
	spinnerStopped := false

	go func() {
		chars := `-\|/`
		i := 0
		currentMsg := "접속 준비 중..."

		for {
			select {
			case <-stopSpinner:
				return
			case msg := <-updateStatus:
				currentMsg = msg
			default:
				fmt.Printf("\r%-70s %c", "⏳ "+currentMsg, chars[i%4])
				i++
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	cleanStopSpinner := func() {
		if !spinnerStopped {
			spinnerStopped = true
			stopSpinner <- true
			fmt.Printf("\r%s\r", strings.Repeat(" ", 80))
			fmt.Println("📺 연결 성공! 실시간 로그를 출력합니다 (종료: Ctrl+C)")
			fmt.Println("-------------------------------------------------------")
		}
	}

	// 3. 접속 재시도 루프
	maxRetries := 30

	for i := 0; i < maxRetries; i++ {
		sshCmd := exec.Command("gcloud", "compute", "ssh", vmName,
			"--project", projectID,
			"--zone", vmZone,
			"--command", "sudo journalctl -u google-startup-scripts.service -f -n 20",
			"--quiet",
			"--", "-o", "ConnectTimeout=5",
		)

		var stderrBuf bytes.Buffer
		sshCmd.Stderr = &stderrBuf

		sshCmd.Stdout = &logMonitor{
			w:           os.Stdout,
			cmd:         sshCmd,
			done:        done,
			onFirstByte: cleanStopSpinner,
			targetLog:   "Finished Google Compute Engine Startup Scripts",
		}
		go func() {
			cmdDone <- sshCmd.Run()

		}()

		select {
		case <-done:
			fmt.Println("배포가 완료되었습니다. 모니터링을 종료합니다!")
			fmt.Println("웹사이트 URL: ", tfOut.WebsiteURL.Value)
			return
		case err := <-cmdDone:
			if err == nil {
				return
			}

			errorMsg := stderrBuf.String()
			if strings.Contains(errorMsg, "Connection refused") {
				updateStatus <- fmt.Sprintf("VM이 켜지는 중입니다... (부팅 단계 %d/%d)", i+1, maxRetries)
			} else if strings.Contains(errorMsg, "timed out") {
				updateStatus <- "네트워크 응답 대기 중..."
			} else {
				updateStatus <- fmt.Sprintf("접속 재시도 중... (%d/%d)", i+1, maxRetries)
			}
		}

		time.Sleep(4 * time.Second)
	}

	cleanStopSpinner()
	fmt.Println("\n❌ 최종 접속 실패.")
}
