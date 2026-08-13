package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type DestroyTarget struct {
	ProjectID string
	Region    string
	Zone      string
	VMName    string
	VMCreated bool
	Resources []string
}

type terraformStateDocument struct {
	FormatVersion string `json:"format_version"`
	Values        *struct {
		RootModule *struct {
			Resources []terraformStateResource `json:"resources"`
		} `json:"root_module"`
	} `json:"values"`
}

type terraformStateResource struct {
	Address string `json:"address"`
	Type    string `json:"type"`
	Values  struct {
		Name    string `json:"name"`
		Project string `json:"project"`
		Region  string `json:"region"`
		Zone    string `json:"zone"`
	} `json:"values"`
}

type expectedStateResource struct {
	resourceType string
	name         string
}

var expectedStateResources = map[string]expectedStateResource{
	"google_compute_network.demo":     {resourceType: "google_compute_network", name: "gcp-free-deploy-network"},
	"google_compute_subnetwork.demo":  {resourceType: "google_compute_subnetwork", name: "gcp-free-deploy-subnet"},
	"google_compute_firewall.http":    {resourceType: "google_compute_firewall", name: "gcp-free-deploy-allow-http"},
	"google_compute_firewall.iap_ssh": {resourceType: "google_compute_firewall", name: "gcp-free-deploy-allow-iap-ssh"},
	"google_compute_instance.demo":    {resourceType: "google_compute_instance", name: "gcp-free-deploy-demo"},
}

func readDestroyTarget(ctx context.Context, runner Runner, dir, stateAddresses string, variables terraformVariables) (DestroyTarget, error) {
	result := runner.Run(ctx, Command{Name: "terraform", Args: []string{"show", "-json", "-no-color"}, Dir: dir})
	if result.ExitCode != 0 {
		return DestroyTarget{}, fmt.Errorf("Terraform state JSON을 읽지 못했습니다: %s", sanitizeDiagnostics(result.Stdout+"\n"+result.Stderr))
	}
	return parseDestroyTarget([]byte(result.Stdout), stateAddresses, variables)
}

func parseDestroyTarget(data []byte, stateAddresses string, variables terraformVariables) (DestroyTarget, error) {
	if !projectIDPattern.MatchString(variables.ProjectID) || !zonePattern.MatchString(variables.Zone) || variables.Region != regionFromZone(variables.Zone) {
		return DestroyTarget{}, fmt.Errorf("배포 변수 파일의 project, region 또는 zone이 올바르지 않습니다")
	}

	var document terraformStateDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return DestroyTarget{}, fmt.Errorf("Terraform state JSON 형식이 올바르지 않습니다")
	}
	if !strings.HasPrefix(document.FormatVersion, "1.") || document.Values == nil || document.Values.RootModule == nil {
		return DestroyTarget{}, fmt.Errorf("지원하지 않는 Terraform state JSON 구조입니다")
	}

	listed := make(map[string]bool)
	for _, address := range strings.Fields(stateAddresses) {
		listed[address] = true
	}
	if len(listed) == 0 {
		return DestroyTarget{}, fmt.Errorf("state에 관리 리소스 주소가 없습니다")
	}

	target := DestroyTarget{ProjectID: variables.ProjectID, Region: variables.Region, Zone: variables.Zone}
	seen := make(map[string]bool)
	for _, resource := range document.Values.RootModule.Resources {
		expected, ok := expectedStateResources[resource.Address]
		if !ok || !listed[resource.Address] {
			return DestroyTarget{}, fmt.Errorf("이 도구 소유로 확인되지 않은 state resource가 있습니다: %s", resource.Address)
		}
		if seen[resource.Address] {
			return DestroyTarget{}, fmt.Errorf("state resource 주소가 중복됩니다: %s", resource.Address)
		}
		seen[resource.Address] = true
		if resource.Type != expected.resourceType || resource.Values.Name != expected.name {
			return DestroyTarget{}, fmt.Errorf("state resource의 type 또는 name이 예상과 다릅니다: %s", resource.Address)
		}
		if resource.Values.Project != variables.ProjectID {
			return DestroyTarget{}, fmt.Errorf("state resource와 배포 변수 파일의 project가 일치하지 않습니다: %s", resource.Address)
		}

		switch resource.Address {
		case "google_compute_subnetwork.demo":
			if resource.Values.Region != variables.Region {
				return DestroyTarget{}, fmt.Errorf("state subnet과 배포 변수 파일의 region이 일치하지 않습니다")
			}
		case "google_compute_instance.demo":
			if resource.Values.Zone != variables.Zone {
				return DestroyTarget{}, fmt.Errorf("state VM과 배포 변수 파일의 zone이 일치하지 않습니다")
			}
			target.VMCreated = true
			target.VMName = resource.Values.Name
		}
		target.Resources = append(target.Resources, resource.Address)
	}

	if len(seen) != len(listed) {
		return DestroyTarget{}, fmt.Errorf("terraform state list와 state JSON의 리소스가 일치하지 않습니다")
	}
	return target, nil
}
