package deploycheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenEulerSummaryTemplateIsValidJSON(t *testing.T) {
	content := readRepositoryFile(t, "deploy/openeuler/summary.template.json")
	var document struct {
		Checks map[string]json.RawMessage `json:"checks"`
	}
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		t.Fatalf("summary template is invalid JSON: %v", err)
	}
	for _, name := range []string{"resource_pressure_test", "fault_injection_test"} {
		var check struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(document.Checks[name], &check); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if check.Status != "SKIPPED" {
			t.Fatalf("%s status = %q", name, check.Status)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readRepositoryFile(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

func TestRaceGateSerializesProcessHeavyPackages(t *testing.T) {
	makefile := readRepositoryFile(t, "Makefile")
	if !strings.Contains(makefile, "go test -race -p 1 ./... -count=1") {
		t.Fatal("race gate must serialize packages that recursively launch race-instrumented helpers")
	}
}

func TestOpenEulerSystemdUnitUsesDelegatedUnprivilegedService(t *testing.T) {
	unit := readRepositoryFile(t, "deploy/openeuler/aegisrt.service")
	required := []string{
		"User=aegisrt", "Group=aegisrt", "UMask=0077",
		"StateDirectory=aegisrt", "StateDirectoryMode=0700",
		"ConfigurationDirectory=aegisrt", "ConfigurationDirectoryMode=0700",
		"Delegate=cpu memory pids", "KillMode=mixed",
		"-listen 127.0.0.1:8080", "-scheduler-policy dag-drcfs", "-cgroup-root auto",
	}
	for _, value := range required {
		if !strings.Contains(unit, value) {
			t.Errorf("systemd unit does not contain %q", value)
		}
	}
	if strings.Contains(unit, "ProtectControlGroups=") {
		t.Error("ProtectControlGroups conflicts with delegated cgroup management")
	}
	for _, conflict := range []string{"ProtectKernelTunables=", "ProcSubset=pid", "Documentation=http"} {
		if strings.Contains(unit, conflict) {
			t.Errorf("systemd unit contains conflicting or identifying value %q", conflict)
		}
	}
	if strings.Contains(unit, "User=root") {
		t.Error("systemd service must not run as root")
	}
}

func TestFaultKernelSmokeUnitIsDedicatedStaticAndBounded(t *testing.T) {
	unit := readRepositoryFile(t, "deploy/openeuler/aegisrt-bench-f4-smoke.service")
	required := []string{
		"Type=exec", "User=aegisrt-bench", "Group=aegisrt-bench", "UMask=0077",
		"RuntimeDirectory=aegisrt-bench", "RuntimeDirectoryMode=0700",
		"StateDirectory=aegisrt-bench", "StateDirectoryMode=0700",
		"ExecStart=/usr/local/libexec/aegisrt/aegisbench fault-kernel",
		"--profile smoke", "--output /var/lib/aegisrt-bench",
		"--seeds 104729,104743", "--repetitions 1",
		"Restart=no", "KillMode=control-group", "Delegate=cpu memory pids",
		"PrivateNetwork=yes", "NoNewPrivileges=yes", "ProtectSystem=strict",
	}
	for _, value := range required {
		if !strings.Contains(unit, value) {
			t.Errorf("F4 smoke unit does not contain %q", value)
		}
	}
	for _, forbidden := range []string{
		"[Install]", "ExecStopPost=", "ProtectControlGroups=", "PrivateUsers=",
		"aegisrt.service", "--daemon-pid", "--unit", "--cgroup",
	} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("F4 smoke unit contains forbidden value %q", forbidden)
		}
	}
	if !strings.Contains(unit, "ConditionFileIsExecutable=/usr/local/libexec/aegisrt/aegisrt") {
		t.Error("F4 smoke unit does not pin the sibling runtime daemon")
	}
	if strings.Contains(unit, "ConditionPathIsExecutable=") {
		t.Error("F4 smoke unit uses an unknown systemd condition directive")
	}
}

func TestFaultKernelBenchmarkHasSeparateServiceAccount(t *testing.T) {
	sysusers := readRepositoryFile(t, "deploy/openeuler/aegisrt-bench.sysusers")
	if !strings.Contains(sysusers, `u aegisrt-bench - "AegisRT benchmark account"`) {
		t.Error("benchmark sysusers file does not define the dedicated account")
	}
}

func TestResourceKernelSmokeUnitIsStaticDelegatedAndBounded(t *testing.T) {
	unit := readRepositoryFile(t, "deploy/openeuler/aegisrt-bench-resource-smoke.service")
	for _, value := range []string{
		"Type=exec", "User=aegisrt-bench", "Group=aegisrt-bench",
		"ExecStart=/usr/local/libexec/aegisrt/aegisbench resource-kernel",
		"--profile smoke", "--output /var/lib/aegisrt-bench",
		"Restart=no", "KillMode=control-group", "Delegate=cpu memory pids",
		"PrivateNetwork=yes", "NoNewPrivileges=yes", "ProtectSystem=strict",
	} {
		if !strings.Contains(unit, value) {
			t.Errorf("resource smoke unit does not contain %q", value)
		}
	}
	for _, forbidden := range []string{"[Install]", "User=root", "ProtectControlGroups=", "PrivateUsers="} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("resource smoke unit contains forbidden value %q", forbidden)
		}
	}
}

func TestOpenEulerScriptsHaveSafeShellBaseline(t *testing.T) {
	names := []string{
		"preflight.sh", "build.sh", "install.sh", "verify-platform.sh", "anonymize.sh",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			content := readRepositoryFile(t, "scripts/openeuler/"+name)
			if !strings.HasPrefix(content, "#!/usr/bin/env bash\nset -euo pipefail\n") {
				t.Error("script must use bash and set -euo pipefail")
			}
			for _, forbidden := range []string{"rm -rf", "curl http://", "wget ", "eval ", "source /etc/os-release"} {
				if strings.Contains(content, forbidden) {
					t.Errorf("script contains forbidden fragment %q", forbidden)
				}
			}
		})
	}
}

func TestPreflightIsReadOnlyAndChecksPlatformRequirements(t *testing.T) {
	script := readRepositoryFile(t, "scripts/openeuler/preflight.sh")
	for _, value := range []string{
		"--json", "/etc/os-release", "openEuler", "cgroup2fs", "cgroup.controllers",
		"cpu", "memory", "pids", "/proc/pressure/", "systemctl", "GOTOOLCHAIN=local", "1.23",
	} {
		if !strings.Contains(script, value) {
			t.Errorf("preflight does not contain %q", value)
		}
	}
	for _, mutating := range []string{"mkdir ", "install ", "systemctl start", "systemctl enable", "> /sys/fs/cgroup"} {
		if strings.Contains(script, mutating) {
			t.Errorf("preflight contains mutating operation %q", mutating)
		}
	}
}

func TestBuildIsOfflineAndBuildsRuntimeAndBenchmarkBinaries(t *testing.T) {
	script := readRepositoryFile(t, "scripts/openeuler/build.sh")
	for _, value := range []string{
		"GOTOOLCHAIN=local", "./cmd/aegisrt", "./cmd/aegisbench",
		"sha256sum", "build-manifest.json", "-trimpath", "-buildvcs=false",
	} {
		if !strings.Contains(script, value) {
			t.Errorf("build script does not contain %q", value)
		}
	}
	for _, network := range []string{"go mod download", "go get ", "curl ", "wget "} {
		if strings.Contains(script, network) {
			t.Errorf("build script may not download dependencies: %q", network)
		}
	}
}

func TestInstallAddsStaticFaultKernelUnitWithoutStartingIt(t *testing.T) {
	script := readRepositoryFile(t, "scripts/openeuler/install.sh")
	for _, value := range []string{
		"aegisbench", "aegisrt-bench.sysusers", "aegisrt-bench-f4-smoke.service",
		"/usr/local/libexec/aegisrt/aegisbench",
	} {
		if !strings.Contains(script, value) {
			t.Errorf("install script does not contain %q", value)
		}
	}
	for _, unsafe := range []string{
		"systemctl start aegisrt-bench-f4-smoke.service",
		"systemctl enable aegisrt-bench-f4-smoke.service",
	} {
		if strings.Contains(script, unsafe) {
			t.Errorf("install script must not activate benchmark unit: %q", unsafe)
		}
	}
}

func TestOpenEulerReadmeDocumentsFixedFaultKernelSmoke(t *testing.T) {
	readme := readRepositoryFile(t, "deploy/openeuler/README.md")
	for _, value := range []string{
		"aegisrt-bench-f4-smoke.service", "static", "Restart=no",
		"/var/lib/aegisrt-bench", "SHA256SUMS", "two",
	} {
		if !strings.Contains(readme, value) {
			t.Errorf("openEuler README does not document %q", value)
		}
	}
}

func TestInstallProtectsCredentialAndDoesNotStartService(t *testing.T) {
	script := readRepositoryFile(t, "scripts/openeuler/install.sh")
	for _, value := range []string{
		"EUID", "/usr/local/libexec/aegisrt", "/etc/aegisrt", "/var/lib/aegisrt",
		"/dev/urandom", "chmod 0600", "systemctl daemon-reload",
	} {
		if !strings.Contains(script, value) {
			t.Errorf("install script does not contain %q", value)
		}
	}
	for _, unsafe := range []string{"cat \"$token", "echo \"$token", "\nsystemctl start", "\nsystemctl enable"} {
		if strings.Contains(script, unsafe) {
			t.Errorf("install script contains unsafe action %q", unsafe)
		}
	}
}

func TestVerificationKeepsUnimplementedClaimSkipped(t *testing.T) {
	script := readRepositoryFile(t, "scripts/openeuler/verify-platform.sh")
	for _, value := range []string{
		"preflight.json", "systemctl is-active", "/healthz", "/readyz",
		"summary.template.json", "read -r manager_pid", "SUDO_UID", "SUDO_GID",
		"chown -R",
	} {
		if !strings.Contains(script, value) {
			t.Errorf("verification script does not contain %q", value)
		}
	}
	if strings.Contains(script, `-s "$cgroup_dir/manager/cgroup.procs"`) {
		t.Error("verification must not use regular-file size for virtual cgroup.procs")
	}
}

func TestAnonymizerFailsClosed(t *testing.T) {
	script := readRepositoryFile(t, "scripts/openeuler/anonymize.sh")
	for _, value := range []string{
		"Authorization", "Bearer", "password", "PRIVATE KEY", "/home/", "/root/",
		"refusing to publish", "SHA256SUMS", `chmod 0755 "$temp_dir"`,
	} {
		if !strings.Contains(script, value) {
			t.Errorf("anonymizer does not contain %q", value)
		}
	}
}

func TestPublishedOpenEulerEvidenceIntegrity(t *testing.T) {
	attributes := readRepositoryFile(t, ".gitattributes")
	if !strings.Contains(attributes, "evidence/openeuler/*-run-*/* -text") {
		t.Fatal("published evidence must be exempt from line-ending normalization")
	}
	bundles := map[string]int{
		"evidence/openeuler/f4-run-20260722-f96b663": 9,
	}
	for bundle, expectedCount := range bundles {
		t.Run(filepath.Base(bundle), func(t *testing.T) {
			lines := strings.Split(strings.TrimSpace(readRepositoryFile(t, bundle+"/SHA256SUMS")), "\n")
			if len(lines) != expectedCount {
				t.Fatalf("checksum entries = %d, want %d", len(lines), expectedCount)
			}
			for _, line := range lines {
				expected, name, found := strings.Cut(strings.TrimSuffix(line, "\r"), "  ")
				if !found || len(expected) != 64 || filepath.Base(name) != name {
					t.Fatalf("malformed checksum line %q", line)
				}
				content, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(bundle), name))
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(content)
				if actual := hex.EncodeToString(digest[:]); actual != expected {
					t.Fatalf("%s digest = %s, want %s", name, actual, expected)
				}
			}
		})
	}

	var f4 struct {
		Status   string `json:"status"`
		Software struct {
			RuntimeCommit string `json:"runtime_commit"`
			RuntimeDirty  bool   `json:"runtime_dirty"`
		} `json:"software"`
		Anonymization struct {
			ScanResult string `json:"scan_result"`
		} `json:"anonymization"`
	}
	if err := json.Unmarshal([]byte(readRepositoryFile(t,
		"evidence/openeuler/f4-run-20260722-f96b663/manifest.json")), &f4); err != nil {
		t.Fatal(err)
	}
	if f4.Status != "COMPLETED" || f4.Software.RuntimeDirty ||
		f4.Software.RuntimeCommit != "f96b66344e174167f25acd101990adb89a9df790" ||
		f4.Anonymization.ScanResult != "PASSED" {
		t.Fatalf("unexpected F4 manifest boundary: %+v", f4)
	}
}
