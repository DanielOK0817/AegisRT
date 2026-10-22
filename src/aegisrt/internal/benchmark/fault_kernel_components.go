package benchmark

func buildFaultKernelComponentManifest(
	harnessPath, daemonPath, sourceRevision string,
) (FaultKernelComponentManifest, []byte, string, error) {
	return buildFaultKernelComponentManifestForOwner(
		harnessPath, daemonPath, sourceRevision, 0,
	)
}

func buildFaultKernelComponentManifestForOwner(
	harnessPath, daemonPath, sourceRevision string, requiredOwner uint32,
) (FaultKernelComponentManifest, []byte, string, error) {
	harness, err := faultKernelComponent(
		FaultKernelComponentHarness, "executable", harnessPath, sourceRevision, requiredOwner,
	)
	if err != nil {
		return FaultKernelComponentManifest{}, nil, "", err
	}
	daemon, err := faultKernelComponent(
		FaultKernelComponentRuntimeDaemon, "executable", daemonPath, sourceRevision, requiredOwner,
	)
	if err != nil {
		return FaultKernelComponentManifest{}, nil, "", err
	}
	fixture := FaultKernelComponent{
		Role: FaultKernelComponentAgentFixture, ArtifactKind: "embedded-fixture",
		Bytes: harness.Bytes, SHA256: harness.SHA256,
	}
	if sourceRevision != "" {
		revision := sourceRevision
		fixture.SourceRevision = &revision
	}
	manifest := FaultKernelComponentManifest{
		SchemaVersion: FaultKernelComponentManifestSchemaV1,
		Components:    []FaultKernelComponent{harness, daemon, fixture},
	}
	if err := ValidateFaultKernelComponentManifest(manifest); err != nil {
		return FaultKernelComponentManifest{}, nil, "", err
	}
	payload, err := canonicalJSON(manifest)
	if err != nil {
		return FaultKernelComponentManifest{}, nil, "", err
	}
	return manifest, payload, hashBytes(payload), nil
}

func faultKernelComponent(
	role, artifactKind, path, sourceRevision string, requiredOwner uint32,
) (FaultKernelComponent, error) {
	bytes, digest, err := readFaultKernelComponent(path, requiredOwner)
	if err != nil {
		return FaultKernelComponent{}, err
	}
	if bytes <= 0 || !validFaultKernelHash(digest) {
		return FaultKernelComponent{}, faultKernelDaemonFailure("component-identity")
	}
	component := FaultKernelComponent{
		Role: role, ArtifactKind: artifactKind, Bytes: bytes, SHA256: digest,
	}
	if sourceRevision != "" {
		revision := sourceRevision
		component.SourceRevision = &revision
	}
	return component, nil
}
