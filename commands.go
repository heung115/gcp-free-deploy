package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultConfigPath = "gcp-free-deploy.json"
	applyPlanName     = ".gcp-free-deploy.tfplan"
	destroyPlanName   = ".gcp-free-deploy-destroy.tfplan"
)

type upOptions struct {
	ConfigPath      string
	AutoApprove     bool
	PlanOnly        bool
	AllowPublicHTTP bool
}

type downOptions struct {
	AutoApprove bool
}

func runCLI(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, runner Runner, workdir string) error {
	if len(args) == 0 {
		printUsage(out)
		return nil
	}

	switch args[0] {
	case "init":
		if len(args) != 1 {
			return fmt.Errorf("init 명령에는 추가 인자를 사용할 수 없습니다")
		}
		if err := ensureRuntimeAssets(workdir); err != nil {
			return err
		}
		fmt.Fprintln(out, "실행 파일을 준비했습니다: main.tf, provider lock, example 설정")
		return nil
	case "up":
		opts, err := parseUpOptions(args[1:], errOut)
		if err != nil {
			return err
		}
		return deployTerraform(ctx, in, out, runner, workdir, opts)
	case "down":
		opts, err := parseDownOptions(args[1:], errOut)
		if err != nil {
			return err
		}
		return destroyTerraform(ctx, in, out, runner, workdir, opts)
	case "validate":
		return validateProject(ctx, args[1:], out, errOut, runner, workdir)
	case "help", "-h", "--help":
		printUsage(out)
		return nil
	default:
		printUsage(errOut)
		return fmt.Errorf("지원하지 않는 명령: %s", args[0])
	}
}

func parseUpOptions(args []string, errOut io.Writer) (upOptions, error) {
	var opts upOptions
	set := flag.NewFlagSet("up", flag.ContinueOnError)
	set.SetOutput(errOut)
	set.StringVar(&opts.ConfigPath, "config", defaultConfigPath, "deployment config JSON path")
	set.BoolVar(&opts.AutoApprove, "auto-approve", false, "skip the apply confirmation")
	set.BoolVar(&opts.PlanOnly, "plan-only", false, "validate and create a plan without applying it")
	set.BoolVar(&opts.AllowPublicHTTP, "allow-public-http", false, "allow 0.0.0.0/0 only when explicitly requested")
	if err := set.Parse(args); err != nil {
		return upOptions{}, err
	}
	if set.NArg() != 0 {
		return upOptions{}, fmt.Errorf("up 명령에 알 수 없는 인자가 있습니다: %s", strings.Join(set.Args(), " "))
	}
	return opts, nil
}

func parseDownOptions(args []string, errOut io.Writer) (downOptions, error) {
	var opts downOptions
	set := flag.NewFlagSet("down", flag.ContinueOnError)
	set.SetOutput(errOut)
	set.BoolVar(&opts.AutoApprove, "auto-approve", false, "skip the destroy confirmation")
	if err := set.Parse(args); err != nil {
		return downOptions{}, err
	}
	if set.NArg() != 0 {
		return downOptions{}, fmt.Errorf("down 명령에 알 수 없는 인자가 있습니다: %s", strings.Join(set.Args(), " "))
	}
	return opts, nil
}

func deployTerraform(ctx context.Context, in io.Reader, out io.Writer, runner Runner, workdir string, opts upOptions) error {
	cfg, err := readOrPromptConfig(opts.ConfigPath, in, out)
	if err != nil {
		return &DeploymentError{Kind: FailureInvalidConfig, Operation: "설정 검증", Diagnostics: err.Error()}
	}
	if cfg.exposesHTTPToEveryone() && !opts.PlanOnly && !opts.AllowPublicHTTP {
		return &DeploymentError{
			Kind:        FailureConfirmationRequired,
			Operation:   "공개 HTTP 확인",
			Diagnostics: "0.0.0.0/0은 전체 인터넷 공개입니다. 실행하려면 --allow-public-http를 명시해야 합니다",
		}
	}
	if err := ensureRuntimeAssets(workdir); err != nil {
		return err
	}
	if err := requireTool("terraform"); err != nil {
		return err
	}
	if !opts.PlanOnly {
		if err := requireTool("gcloud"); err != nil {
			return err
		}
	}
	if err := guardUpState(ctx, runner, workdir, cfg); err != nil {
		return err
	}

	printDeploymentSummary(out, cfg, opts)
	if err := runTerraformPreflight(ctx, runner, workdir); err != nil {
		return err
	}

	zones := append([]string{cfg.Zone}, cfg.FallbackZones...)
	planPath := filepath.Join(workdir, applyPlanName)
	defer os.Remove(planPath)

	for index, zone := range zones {
		attempt := cfg
		attempt.Zone = zone
		if _, err := writeTerraformVariables(workdir, attempt); err != nil {
			return err
		}

		fmt.Fprintf(out, "\n[%d/%d] %s zone 계획을 생성합니다.\n", index+1, len(zones), zone)
		hasChanges, err := runTerraformPlan(ctx, runner, workdir, applyPlanName, false, out)
		if err != nil {
			return err
		}
		if opts.PlanOnly {
			if hasChanges {
				fmt.Fprintln(out, "\n계획 검증 완료: 실제 리소스는 변경하지 않았습니다.")
			} else {
				fmt.Fprintln(out, "\n계획 검증 완료: 변경 사항이 없습니다.")
			}
			return nil
		}

		approved := opts.AutoApprove
		if !approved {
			approved, err = confirm(in, out, "위 계획을 적용해 비용이 발생할 수 있는 리소스를 생성하려면 yes를 입력하세요: ")
			if err != nil {
				return err
			}
		}
		if err := runTerraformApply(ctx, runner, workdir, applyPlanName, approved); err != nil {
			var deployErr *DeploymentError
			if errors.As(err, &deployErr) && deployErr.Kind == FailureZoneCapacity && index+1 < len(zones) {
				fmt.Fprintf(out, "zone %s의 용량이 부족해 같은 region의 다음 zone으로 이동합니다.\n", zone)
				continue
			}
			return fmt.Errorf("%w\n일부 리소스가 남았을 수 있습니다. terraform state list와 down 명령으로 확인하세요", err)
		}

		outputs, err := readTerraformOutputs(ctx, runner, workdir)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "\n인프라 생성 완료. startup, container, HTTP 상태를 확인합니다.")
		monitor := NewDeploymentMonitor(runner, out)
		if err := monitor.Wait(ctx, outputs); err != nil {
			return fmt.Errorf("%w\nVM과 네트워크는 남아 있습니다. 진단 후 down으로 정리하세요", err)
		}

		fmt.Fprintf(out, "\n배포 검증 완료: %s\n", outputs.WebsiteURL.Value)
		fmt.Fprintln(out, "사용이 끝나면 go run . down 으로 리소스를 삭제하세요.")
		return nil
	}

	return &DeploymentError{Kind: FailureZoneCapacity, Operation: "terraform apply", Diagnostics: "지정한 모든 zone에서 용량 부족이 발생했습니다"}
}

func validateProject(ctx context.Context, args []string, out, errOut io.Writer, runner Runner, workdir string) error {
	set := flag.NewFlagSet("validate", flag.ContinueOnError)
	set.SetOutput(errOut)
	configPath := set.String("config", defaultConfigPath, "deployment config JSON path")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("validate 명령에 알 수 없는 인자가 있습니다")
	}
	if _, err := LoadDeployConfig(*configPath); err != nil {
		return &DeploymentError{Kind: FailureInvalidConfig, Operation: "설정 검증", Diagnostics: err.Error()}
	}
	if err := requireTool("terraform"); err != nil {
		return err
	}
	if err := ensureRuntimeAssets(workdir); err != nil {
		return err
	}
	if err := runTerraformPreflight(ctx, runner, workdir); err != nil {
		return err
	}
	fmt.Fprintln(out, "설정과 Terraform 정적 검증을 통과했습니다. 실제 GCP 조회나 변경은 수행하지 않았습니다.")
	return nil
}

func guardUpState(ctx context.Context, runner Runner, workdir string, desired DeployConfig) error {
	workspace := runner.Run(ctx, Command{Name: "terraform", Args: []string{"workspace", "show", "-no-color"}, Dir: workdir})
	if workspace.ExitCode != 0 {
		return newUnsafeStateError("Terraform workspace를 확인하지 못했습니다")
	}
	if strings.TrimSpace(workspace.Stdout) != "default" {
		return newUnsafeStateError("default가 아닌 Terraform workspace는 지원하지 않습니다")
	}

	statePath := filepath.Join(workdir, "terraform.tfstate")
	info, err := os.Lstat(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			backupPath := filepath.Join(workdir, "terraform.tfstate.backup")
			if _, backupErr := os.Lstat(backupPath); backupErr == nil {
				return newUnsafeStateError("활성 state 없이 terraform.tfstate.backup만 남아 있습니다")
			} else if !errors.Is(backupErr, os.ErrNotExist) {
				return newUnsafeStateError("terraform.tfstate.backup을 확인하지 못했습니다")
			}
			return nil
		}
		return newUnsafeStateError("terraform.tfstate를 확인하지 못했습니다")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return newUnsafeStateError("terraform.tfstate가 비어 있거나 안전한 일반 파일이 아닙니다")
	}

	stateList := runner.Run(ctx, Command{Name: "terraform", Args: []string{"state", "list"}, Dir: workdir})
	if stateList.ExitCode != 0 {
		return newUnsafeStateError("Terraform state 주소를 읽지 못했습니다")
	}
	if strings.TrimSpace(stateList.Stdout) == "" {
		return nil
	}
	if err := validateOwnedState(stateList.Stdout); err != nil {
		return newUnsafeStateError(err.Error())
	}

	variables, err := readTerraformVariables(filepath.Join(workdir, terraformVariablesName))
	if err != nil {
		return newUnsafeStateError("기존 배포 변수 파일을 안전하게 확인하지 못했습니다")
	}
	outputs, err := readTerraformOutputs(ctx, runner, workdir)
	if err != nil {
		return newUnsafeStateError("기존 state output에서 project와 VM을 확인하지 못했습니다")
	}
	if err := validateDestroyTarget(outputs, variables); err != nil {
		return newUnsafeStateError(err.Error())
	}
	if err := validateDesiredStateTarget(desired, variables); err != nil {
		return newUnsafeStateError(err.Error())
	}
	return nil
}

func validateDesiredStateTarget(desired DeployConfig, existing terraformVariables) error {
	if desired.ProjectID != existing.ProjectID || regionFromZone(desired.Zone) != existing.Region {
		return fmt.Errorf("요청한 project 또는 region이 기존 state 대상과 일치하지 않습니다")
	}
	allowedZones := append([]string{desired.Zone}, desired.FallbackZones...)
	for _, zone := range allowedZones {
		if zone == existing.Zone {
			return nil
		}
	}
	return fmt.Errorf("요청한 zone 목록에 기존 state의 zone이 없습니다")
}

func newUnsafeStateError(reason string) error {
	return &DeploymentError{
		Kind:      FailureUnsafeState,
		Operation: "up state 안전 검사",
		Diagnostics: reason + ". state를 삭제하지 말고 실제 GCP 리소스를 확인한 뒤 state migration을 수행하거나 " +
			"새 작업 폴더에서 격리해 실행하세요",
	}
}

func readOrPromptConfig(path string, in io.Reader, out io.Writer) (DeployConfig, error) {
	cfg, err := LoadDeployConfig(path)
	if err == nil {
		fmt.Fprintf(out, "설정 파일 사용: %s\n", path)
		return cfg, nil
	}
	if !errors.Is(rootCause(err), os.ErrNotExist) {
		return DeployConfig{}, err
	}
	if path != defaultConfigPath {
		return DeployConfig{}, err
	}
	fmt.Fprintln(out, "gcp-free-deploy.json이 없어 대화형으로 설정을 받습니다.")
	return promptDeployConfig(in, out)
}

func promptDeployConfig(in io.Reader, out io.Writer) (DeployConfig, error) {
	reader := bufio.NewReader(in)
	read := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}

	var cfg DeployConfig
	var err error
	if cfg.ProjectID, err = read("GCP project ID: "); err != nil {
		return DeployConfig{}, err
	}
	if cfg.Zone, err = read("기본 zone (예: us-central1-a): "); err != nil {
		return DeployConfig{}, err
	}
	fallbacks, err := read("같은 region의 fallback zone들(쉼표 구분, 선택): ")
	if err != nil {
		return DeployConfig{}, err
	}
	if fallbacks != "" {
		cfg.FallbackZones = strings.Split(fallbacks, ",")
	}
	if cfg.Source, err = read("배포 소스(docker/github): "); err != nil {
		return DeployConfig{}, err
	}
	if strings.EqualFold(cfg.Source, "docker") {
		if cfg.DockerImage, err = read("고정 tag 또는 digest를 포함한 Docker image: "); err != nil {
			return DeployConfig{}, err
		}
	} else {
		if cfg.GithubRepo, err = read("공개 GitHub HTTPS URL: "); err != nil {
			return DeployConfig{}, err
		}
	}
	port, err := read("container port (기본 80): ")
	if err != nil {
		return DeployConfig{}, err
	}
	if port == "" {
		cfg.ContainerPort = 80
	} else if cfg.ContainerPort, err = strconv.Atoi(port); err != nil {
		return DeployConfig{}, &ValidationError{Field: "container_port", Message: "숫자여야 합니다"}
	}
	ranges, err := read("HTTP 허용 IPv4 CIDR(쉼표 구분, 예: 내공인IP/32): ")
	if err != nil {
		return DeployConfig{}, err
	}
	if ranges != "" {
		cfg.AllowedSourceRanges = strings.Split(ranges, ",")
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return DeployConfig{}, err
	}
	return cfg, nil
}

func printDeploymentSummary(out io.Writer, cfg DeployConfig, opts upOptions) {
	fmt.Fprintln(out, "\n배포 전 확인")
	fmt.Fprintf(out, "- project: %s\n", cfg.ProjectID)
	fmt.Fprintf(out, "- region / zone: %s / %s\n", regionFromZone(cfg.Zone), cfg.Zone)
	if len(cfg.FallbackZones) > 0 {
		fmt.Fprintf(out, "- fallback zones: %s\n", strings.Join(cfg.FallbackZones, ", "))
	}
	fmt.Fprintf(out, "- VM: %s, boot disk: %dGB pd-standard\n", cfg.MachineType, cfg.DiskSizeGB)
	fmt.Fprintf(out, "- source: %s, host TCP/80 -> container TCP/%d\n", cfg.Source, cfg.ContainerPort)
	fmt.Fprintf(out, "- HTTP source ranges: %s\n", strings.Join(cfg.AllowedSourceRanges, ", "))
	fmt.Fprintln(out, "- resources: dedicated VPC, subnet, HTTP firewall, IAP SSH firewall, one VM with ephemeral external IP")
	fmt.Fprintln(out, "- VM service account: none; HTTPS: not configured")
	if cfg.exposesHTTPToEveryone() {
		fmt.Fprintln(out, "- WARNING: TCP/80 will be reachable from the entire IPv4 internet")
	}
	if strings.HasSuffix(strings.Split(cfg.DockerImage, "@")[0], ":latest") {
		fmt.Fprintln(out, "- WARNING: Docker latest tag is mutable; use a fixed tag or digest when possible")
	}
	if opts.PlanOnly {
		fmt.Fprintln(out, "- mode: plan only (no resource mutation)")
	} else {
		fmt.Fprintln(out, "- cost: Free Tier eligibility is not guaranteed; VM, disk, IP, and network usage can be billed")
	}
}

func confirm(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprint(out, prompt)
	reader := bufio.NewReader(in)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(answer), "yes"), nil
}

func destroyTerraform(ctx context.Context, in io.Reader, out io.Writer, runner Runner, workdir string, opts downOptions) error {
	statePath := filepath.Join(workdir, "terraform.tfstate")
	info, err := os.Lstat(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy 안전 검사", Diagnostics: "terraform.tfstate가 없어 삭제 대상을 확인할 수 없습니다"}
		}
		return fmt.Errorf("state 확인: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy 안전 검사", Diagnostics: "terraform.tfstate가 비어 있거나 일반 파일이 아닙니다"}
	}
	if err := requireTool("terraform"); err != nil {
		return err
	}
	workspace := runner.Run(ctx, Command{Name: "terraform", Args: []string{"workspace", "show", "-no-color"}, Dir: workdir})
	if workspace.ExitCode != 0 || strings.TrimSpace(workspace.Stdout) != "default" {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy 안전 검사", Diagnostics: "default Terraform workspace만 삭제할 수 있습니다"}
	}
	stateList := runner.Run(ctx, Command{Name: "terraform", Args: []string{"state", "list"}, Dir: workdir})
	if stateList.ExitCode != 0 {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy 안전 검사", Diagnostics: "Terraform state 주소를 읽지 못했습니다"}
	}
	if err := validateOwnedState(stateList.Stdout); err != nil {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy 안전 검사", Diagnostics: err.Error()}
	}

	variables, err := readTerraformVariables(filepath.Join(workdir, terraformVariablesName))
	if err != nil {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy 안전 검사", Diagnostics: "배포 변수 파일을 안전하게 확인하지 못했습니다: " + err.Error()}
	}
	target, err := readDestroyTarget(ctx, runner, workdir, stateList.Stdout, variables)
	if err != nil {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy 안전 검사", Diagnostics: err.Error()}
	}
	if err := ensureRuntimeAssets(workdir); err != nil {
		return err
	}
	if err := runTerraformPreflight(ctx, runner, workdir); err != nil {
		return err
	}

	fmt.Fprintln(out, "\n삭제 전 확인")
	fmt.Fprintf(out, "- project: %s\n", target.ProjectID)
	fmt.Fprintf(out, "- region / configured zone: %s / %s\n", target.Region, target.Zone)
	if target.VMCreated {
		fmt.Fprintf(out, "- VM: %s\n", target.VMName)
	} else {
		fmt.Fprintln(out, "- VM: 생성되지 않음 (부분 생성 state 정리)")
	}
	if len(target.Resources) > 0 {
		fmt.Fprintf(out, "- Terraform 관리 리소스: %s\n", strings.Join(target.Resources, ", "))
	}
	fmt.Fprintln(out, "- 로컬 state가 관리하는 이 프로젝트 리소스만 삭제합니다.")

	planPath := filepath.Join(workdir, destroyPlanName)
	defer os.Remove(planPath)
	hasChanges, err := runTerraformPlan(ctx, runner, workdir, destroyPlanName, true, out)
	if err != nil {
		return err
	}
	if !hasChanges {
		fmt.Fprintln(out, "삭제할 Terraform 리소스가 없습니다.")
		return nil
	}

	approved := opts.AutoApprove
	if !approved {
		approved, err = confirm(in, out, "위 project와 리소스를 삭제하려면 yes를 입력하세요: ")
		if err != nil {
			return err
		}
	}
	if err := runTerraformDestroyApply(ctx, runner, workdir, destroyPlanName, approved); err != nil {
		return err
	}

	stateList = runner.Run(ctx, Command{Name: "terraform", Args: []string{"state", "list"}, Dir: workdir})
	if stateList.ExitCode != 0 || strings.TrimSpace(stateList.Stdout) != "" {
		diagnostics := sanitizeDiagnostics(stateList.Stdout + "\n" + stateList.Stderr)
		return &DeploymentError{
			Kind:        FailureTerraformDestroy,
			Operation:   "destroy 결과 검증",
			Diagnostics: "일부 리소스가 state에 남았습니다: " + diagnostics + ". Google Cloud Console에서 project와 zone을 확인하세요. 남은 리소스는 비용이 발생할 수 있습니다",
		}
	}
	fmt.Fprintln(out, "삭제 완료: Terraform state에 관리 리소스가 남지 않았습니다.")
	return nil
}

func validateDestroyTarget(outputs TerraformOutputs, variables terraformVariables) error {
	if variables.ProjectID == "" || variables.Region == "" || variables.Zone == "" {
		return fmt.Errorf("배포 변수 파일의 project, region 또는 zone이 비어 있습니다")
	}
	if variables.Region != regionFromZone(variables.Zone) {
		return fmt.Errorf("배포 변수 파일의 region과 zone이 일치하지 않습니다")
	}
	if outputs.ProjectID.Value != variables.ProjectID {
		return fmt.Errorf("Terraform state output과 배포 변수 파일의 project가 일치하지 않습니다")
	}
	if outputs.Region.Value != variables.Region || outputs.VMZone.Value != variables.Zone {
		return fmt.Errorf("Terraform state output과 배포 변수 파일의 region 또는 zone이 일치하지 않습니다")
	}
	if outputs.VMName.Value != "gcp-free-deploy-demo" && outputs.VMName.Value != "my-free-portfolio" {
		return fmt.Errorf("이 도구가 관리하는 VM 이름으로 확인되지 않습니다")
	}
	return nil
}

func validateOwnedState(raw string) error {
	allowed := map[string]bool{
		"google_compute_network.demo":     true,
		"google_compute_subnetwork.demo":  true,
		"google_compute_firewall.http":    true,
		"google_compute_firewall.iap_ssh": true,
		"google_compute_instance.demo":    true,
	}
	count := 0
	for _, address := range strings.Fields(raw) {
		count++
		if !allowed[address] {
			return fmt.Errorf("이 도구 소유로 확인되지 않은 state 주소가 있습니다: %s", address)
		}
	}
	if count == 0 {
		return fmt.Errorf("state에 관리 리소스 주소가 없습니다")
	}
	return nil
}

func requireTool(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("필수 도구 %s를 찾을 수 없습니다", name)
	}
	return nil
}

func rootCause(err error) error {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}
		err = unwrapped
	}
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "사용법:")
	fmt.Fprintln(out, "  gcp-free-deploy init")
	fmt.Fprintln(out, "  gcp-free-deploy validate [--config path]")
	fmt.Fprintln(out, "  gcp-free-deploy up [--config path] [--plan-only] [--auto-approve] [--allow-public-http]")
	fmt.Fprintln(out, "  gcp-free-deploy down [--auto-approve]")
}
