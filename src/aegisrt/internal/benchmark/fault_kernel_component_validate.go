package benchmark

import "fmt"

func ValidateFaultKernelComponentManifest(manifest FaultKernelComponentManifest) error {
	if manifest.SchemaVersion != FaultKernelComponentManifestSchemaV1 || len(manifest.Components) != 3 {
		return fmt.Errorf("fault-kernel component manifest has invalid identity or size")
	}
	want := map[string]int{
		FaultKernelComponentHarness: 0, FaultKernelComponentRuntimeDaemon: 0,
		FaultKernelComponentAgentFixture: 0,
	}
	components := make(map[string]FaultKernelComponent, len(want))
	for _, component := range manifest.Components {
		if _, exists := want[component.Role]; !exists || component.Bytes <= 0 ||
			!validFaultKernelHash(component.SHA256) {
			return fmt.Errorf("fault-kernel component is invalid")
		}
		if component.Role == FaultKernelComponentAgentFixture && component.ArtifactKind != "embedded-fixture" ||
			component.Role != FaultKernelComponentAgentFixture && component.ArtifactKind != "executable" {
			return fmt.Errorf("fault-kernel component kind is invalid")
		}
		if component.SourceRevision != nil &&
			(len(*component.SourceRevision) != 40 || !isLowerHex(*component.SourceRevision)) {
			return fmt.Errorf("fault-kernel component source revision is invalid")
		}
		want[component.Role]++
		components[component.Role] = component
	}
	for role, count := range want {
		if count != 1 {
			return fmt.Errorf("fault-kernel component role %q occurs %d times", role, count)
		}
	}
	harness := components[FaultKernelComponentHarness]
	fixture := components[FaultKernelComponentAgentFixture]
	if fixture.Bytes != harness.Bytes || fixture.SHA256 != harness.SHA256 ||
		!sameFaultKernelRevision(fixture.SourceRevision, harness.SourceRevision) {
		return fmt.Errorf("fault-kernel embedded fixture is not bound to the harness executable")
	}
	return nil
}

func sameFaultKernelRevision(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
