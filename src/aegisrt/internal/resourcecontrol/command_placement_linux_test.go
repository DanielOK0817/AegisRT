//go:build linux

package resourcecontrol

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaceCommandInExistingGroupConfiguresClonePlacement(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-placement")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Create("daemon", Limits{}); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("unused")
	closer, err := driver.PlaceCommandInExistingGroup("daemon", command)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if command.SysProcAttr == nil || !command.SysProcAttr.UseCgroupFD || command.SysProcAttr.CgroupFD <= 0 {
		t.Fatalf("command placement was not configured: %+v", command.SysProcAttr)
	}
	if second, err := driver.PlaceCommandInExistingGroup("daemon", command); err == nil || second != nil {
		t.Fatalf("duplicate placement = (%v, %v)", second, err)
	}
}

func TestPlaceCommandInExistingGroupRejectsSymlinkWithoutDisclosure(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-secret")
	if err != nil {
		t.Fatal(err)
	}
	groupPath, err := driver.Create("daemon-secret", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	// Create writes controller fixtures on a regular test filesystem. Remove
	// the complete temporary group before replacing it with the attack symlink.
	if err := os.RemoveAll(groupPath); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, groupPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	closer, err := driver.PlaceCommandInExistingGroup("daemon-secret", exec.Command("unused"))
	if closer != nil || err == nil {
		t.Fatalf("placement = (%v, %v), want rejection", closer, err)
	}
	for _, secret := range []string{"daemon-secret", "runtime-secret", outside, driver.root} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error disclosed identifier %q: %v", secret, err)
		}
	}
}

func TestPlaceCommandInAttemptManagerUsesOnlyFixedChild(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-placement")
	if err != nil {
		t.Fatal(err)
	}
	attemptPath, err := driver.Create("daemon", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("unused")
	closer, err := driver.PlaceCommandInAttemptManager("daemon", command)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	managerInfo, err := os.Lstat(filepath.Join(attemptPath, "manager"))
	if err != nil {
		t.Fatal(err)
	}
	if !managerInfo.IsDir() || managerInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("manager is not a real directory: %v", managerInfo.Mode())
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.UseCgroupFD || command.SysProcAttr.CgroupFD <= 0 {
		t.Fatalf("command placement was not configured: %+v", command.SysProcAttr)
	}
}

func TestPlaceCommandInAttemptManagerRejectsSymlinkWithoutDisclosure(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-secret")
	if err != nil {
		t.Fatal(err)
	}
	attemptPath, err := driver.Create("daemon-secret", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(attemptPath, "manager")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	closer, err := driver.PlaceCommandInAttemptManager("daemon-secret", exec.Command("unused"))
	if closer != nil || !errors.Is(err, ErrUnsafeCgroupHierarchy) {
		t.Fatalf("placement = (%v, %v), want unsafe hierarchy", closer, err)
	}
	for _, secret := range []string{"daemon-secret", "runtime-secret", outside, driver.root} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error disclosed identifier %q: %v", secret, err)
		}
	}
}

func TestPlaceCommandInAttemptManagerAllowsPopulatedNestedRuntime(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-placement")
	if err != nil {
		t.Fatal(err)
	}
	attemptPath, err := driver.Create("daemon", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(attemptPath, "cgroup.events"), "populated 1\n")
	command := exec.Command("unused")
	closer, err := driver.PlaceCommandInAttemptManager("daemon", command)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if !command.SysProcAttr.UseCgroupFD {
		t.Fatal("clone3 placement was not configured")
	}
}
