package provider

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	"github.com/cursor/terraform-provider-cursor/internal/proto/v1/v1connect"
	"google.golang.org/protobuf/proto"
)

// This opt-in CLI test builds the released provider from its immutable tag,
// imports real Terraform state, and verifies the upgraded provider can read it
// without changing the legacy configuration. Only a local mock API is used.
// Run: TF_ACC=1 go test ./internal/provider -run TestTerraformWorkflowUpgrade -v
func TestTerraformWorkflowUpgrade(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the Terraform CLI upgrade regression")
	}
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	baseline := filepath.Join(tmp, "baseline")
	archive, err := exec.Command("git", "-C", root, "archive", "v0.7.0").Output()
	if err != nil {
		t.Fatalf("v0.7.0 tag required for upgrade test: %v", err)
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || !filepath.IsLocal(header.Name) {
			t.Fatalf("invalid baseline archive: %v", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		file := filepath.Join(baseline, header.Name)
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		writeWorkflowTestFile(t, file, content, os.FileMode(header.Mode))
	}
	mirror := filepath.Join(tmp, "mirror")
	oldBinary := filepath.Join(mirror, "registry.terraform.io/cursor/cursor/0.7.0", runtime.GOOS+"_"+runtime.GOARCH, "terraform-provider-cursor_v0.7.0")
	newDir := filepath.Join(tmp, "current")
	for _, build := range []struct{ dir, binary string }{{baseline, oldBinary}, {root, filepath.Join(newDir, "terraform-provider-cursor")}} {
		if err := os.MkdirAll(filepath.Dir(build.binary), 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "build", "-o", build.binary, ".")
		cmd.Dir = build.dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build provider: %v\n%s", err, output)
		}
	}
	id := "11111111-1111-4111-8111-111111111111"
	mock := &workflowCaptureServer{definition: &v1.AutomationWithOwner{Workflow: &v1.Automation{
		AutomationId: id, Name: "Legacy", Enabled: true, Scope: v1.AutomationScope_AUTOMATION_SCOPE_USER,
		Workflow: &v1.Workflow{
			Prompts: []*v1.Prompt{{Prompt: "review"}}, Model: proto.String("composer-2.5"),
			Triggers: []*v1.Trigger{{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: "C001"}}}},
			Actions:  []*v1.Action{{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Channel: "C001", RespondInThread: true}}}},
		},
	}}}
	_, handler := v1connect.NewAutomationsServiceHandler(mock)
	server := httptest.NewServer(handler)
	defer server.Close()
	dir := filepath.Join(tmp, "config")
	hcl := fmt.Sprintf(`terraform {
  required_providers { cursor = { source = "cursor/cursor", version = "0.7.0" } }
}
provider "cursor" { endpoint = %q }
resource "cursor_platform_workflow" "legacy" {
  name = "Legacy"
  prompt = "review"
  model = "composer-2.5"
  trigger = [{ slack = { channel = "C001" } }]
  action = [{ slack = { channel = "C001", respond_in_thread = true } }]
}
`, server.URL)
	writeWorkflowTestFile(t, filepath.Join(dir, "main.tf"), []byte(hcl), 0600)
	cli := filepath.Join(tmp, "terraform.rc")
	writeWorkflowTestFile(t, cli, []byte(fmt.Sprintf(`provider_installation { filesystem_mirror { path = %q } }`, mirror)), 0600)
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(terraform, args...)
		cmd.Dir = dir
		// Do not inherit live Cursor credentials or unrelated Terraform flags.
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "CURSOR_") && !strings.HasPrefix(env, "TF_") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Env = append(cmd.Env, "CURSOR_TOKEN=fixture", "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("terraform %v: %v\n%s", args, err, output)
		}
		return output
	}
	run("init", "-backend=false", "-input=false", "-no-color")
	run("import", "-input=false", "-no-color", "cursor_platform_workflow.legacy", id)
	run("plan", "-input=false", "-no-color", "-detailed-exitcode")
	writeWorkflowTestFile(t, cli, []byte(fmt.Sprintf(`provider_installation { dev_overrides { "cursor/cursor" = %q } }`, newDir)), 0600)
	run("plan", "-input=false", "-no-color", "-detailed-exitcode", "-out=upgrade.tfplan")
	output := run("show", "-json", "upgrade.tfplan")
	var plan struct {
		ResourceChanges []struct {
			Change struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(output, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.ResourceChanges) != 1 || strings.Join(plan.ResourceChanges[0].Change.Actions, ",") != "no-op" {
		t.Fatalf("upgrade must not mutate the legacy automation: %s", output)
	}
	if mock.update != nil {
		t.Fatal("upgrade plan unexpectedly called UpdateAutomation")
	}
	// Empty configured lists must use legacy routing and survive the API's
	// single-channel normalization without perpetual diffs.
	emptyHCL := strings.ReplaceAll(hcl, `channel = "C001"`, `channel = "C001", channels = []`)
	writeWorkflowTestFile(t, filepath.Join(dir, "main.tf"), []byte(emptyHCL), 0600)
	run("apply", "-auto-approve", "-input=false", "-no-color")
	run("plan", "-input=false", "-no-color", "-detailed-exitcode")
	if mock.update.Workflow.Triggers[0].GetSlackTrigger().GetChannel() != "C001" || mock.update.Workflow.Actions[0].GetSlack().GetChannel() != "C001" {
		t.Fatal("empty channels erased legacy routing")
	}
	// Resource-output references are genuinely unknown during planning and
	// resolve before apply. Verify they never turn into empty routing/false.
	unknownHCL := strings.ReplaceAll(emptyHCL, `channels = []`, `channels = terraform_data.inputs.output.channels`)
	unknownHCL = strings.Replace(unknownHCL,
		`trigger = [{ slack = { channel = "C001", channels = terraform_data.inputs.output.channels } }]`,
		`trigger = [
  { slack = { channel = "C001", channels = terraform_data.inputs.output.channels, top_level_only = terraform_data.inputs.output.top_level_only } },
  { git_label = { repos = ["example/repo"], on_added = terraform_data.inputs.output.on_added, pull_requests = terraform_data.inputs.output.pull_requests } }
]`, 1)
	unknownHCL += `
resource "terraform_data" "inputs" {
  input = { channels = ["C001", "C002"], top_level_only = true, on_added = true, pull_requests = true }
}
`
	writeWorkflowTestFile(t, filepath.Join(dir, "main.tf"), []byte(unknownHCL), 0600)
	run("apply", "-auto-approve", "-input=false", "-no-color")
	run("plan", "-input=false", "-no-color", "-detailed-exitcode")
	slack := mock.update.Workflow.Triggers[0].GetSlackTrigger()
	label := mock.update.Workflow.Triggers[1].GetGit().GetLabel()
	if strings.Join(slack.GetChannels(), ",") != "C001,C002" || !slack.GetTopLevelOnly() || !label.GetOnAdded() || !label.GetPullRequests() {
		t.Fatalf("unknown inputs lost routing or flags: %v", mock.update.Workflow)
	}
}

func writeWorkflowTestFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}
