package benchmark

import (
	"strings"
	"testing"
)

func TestScanAnonymousRejectsSensitiveUnixAbsolutePathsWithoutEcho(t *testing.T) {
	paths := []string{
		`/home/local-user/evidence.json`,
		`/root/private/runtime.json`,
		`/var/lib/aegisrt/state.json`,
		`/tmp/aegisrt-secret`,
		`/sys/fs/cgroup/runtime`,
		`/proc/self/cgroup`,
		`/etc/os-release`,
		`/usr/local/bin/aegisrt`,
		`/opt/aegisrt/config.json`,
		`/run/aegisrt/runtime.sock`,
	}
	for _, path := range paths {
		t.Run(strings.ReplaceAll(strings.Trim(path, "/"), "/", "-"), func(t *testing.T) {
			artifact := []byte(`{"local_path":"` + path + `"}`)
			err := scanAnonymous(artifact)
			if err == nil {
				t.Fatalf("scanAnonymous accepted sensitive Unix path %q", path)
			}
			if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "local-user") {
				t.Fatalf("scan error echoes sensitive path data: %v", err)
			}
		})
	}
	for _, encoded := range []string{
		`{"local_path":"\/home\/encoded-user\/artifact"}`,
		`{"local_path":"\u002fhome\u002fencoded-user\u002fartifact"}`,
	} {
		if err := scanAnonymous([]byte(encoded)); err == nil {
			t.Fatal("scanAnonymous accepted an escaped sensitive Unix path")
		}
	}
}

func TestScanAnonymousRejectsCurrentUnixIdentityWithoutEcho(t *testing.T) {
	tests := []struct {
		variable string
		value    string
	}{
		{variable: "USER", value: "unixuserprobe"},
		{variable: "LOGNAME", value: "lognameprobe"},
		{variable: "HOSTNAME", value: "hostnodeprobe"},
	}
	for _, test := range tests {
		t.Run(test.variable, func(t *testing.T) {
			t.Setenv("USERNAME", "")
			t.Setenv("COMPUTERNAME", "")
			t.Setenv("USER", "")
			t.Setenv("LOGNAME", "")
			t.Setenv("HOSTNAME", "")
			t.Setenv(test.variable, test.value)
			err := scanAnonymous([]byte(`{"operator":"prefix-` + test.value + `-suffix"}`))
			if err == nil {
				t.Fatalf("scanAnonymous accepted current %s identity", test.variable)
			}
			if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.value)) {
				t.Fatalf("scan error echoes current identity: %v", err)
			}
		})
	}
}

func TestScanAnonymousRejectsCredentialKeysWithoutEcho(t *testing.T) {
	tests := []string{
		`{"token":"credential-value-probe"}`,
		`{"access_token":"credential-value-probe"}`,
		`{"refresh-token":"credential-value-probe"}`,
		`{"apiKey":"credential-value-probe"}`,
		`{"client_secret":"credential-value-probe"}`,
		`{"password":"credential-value-probe"}`,
		`{"credentials":"credential-value-probe"}`,
		`AUTH_TOKEN=credential-value-probe`,
	}
	for index, artifact := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			err := scanAnonymous([]byte(artifact))
			if err == nil {
				t.Fatalf("scanAnonymous accepted credential marker in artifact %d", index)
			}
			if strings.Contains(err.Error(), "credential-value-probe") {
				t.Fatalf("scan error echoes credential value: %v", err)
			}
		})
	}
}

func TestScanAnonymousAllowsURIsURLsAndNonCredentialWords(t *testing.T) {
	t.Setenv("USERNAME", "identity-not-in-artifact")
	t.Setenv("COMPUTERNAME", "machine-not-in-artifact")
	t.Setenv("USER", "unix-identity-not-in-artifact")
	t.Setenv("LOGNAME", "login-identity-not-in-artifact")
	t.Setenv("HOSTNAME", "host-identity-not-in-artifact")
	artifact := []byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"id":"urn:aegisrt:experiment:manifest:v1",
		"documentation":"https://example.invalid/home/reference",
		"query":"https://example.invalid/search?next=/home/reference",
		"path_like_urn":"urn:example:/home/reference",
		"ratio":"used / total",
		"contains_credentials":false,
		"token_budget":4096,
		"provider_cache_mode":"disabled",
		"artifact":"environment.json"
	}`)
	if err := scanAnonymous(artifact); err != nil {
		t.Fatalf("scanAnonymous rejected a safe artifact: %v", err)
	}
}

func TestScanAnonymousRetainsExistingEmailWindowsPathAndBearerChecks(t *testing.T) {
	artifacts := []string{
		`{"owner":"local-person@example.invalid"}`,
		`{"path":"C:\\Users\\local-person\\artifact.json"}`,
		`{"authorization":"Bearer credential-value-probe"}`,
	}
	for _, artifact := range artifacts {
		err := scanAnonymous([]byte(artifact))
		if err == nil {
			t.Fatal("scanAnonymous accepted an existing blocked pattern")
		}
		for _, sensitive := range []string{"local-person", "credential-value-probe"} {
			if strings.Contains(err.Error(), sensitive) {
				t.Fatalf("scan error echoes sensitive data: %v", err)
			}
		}
	}
}

func TestScanAnonymousIgnoresAmbiguousShortIdentity(t *testing.T) {
	t.Setenv("USERNAME", "")
	t.Setenv("COMPUTERNAME", "")
	t.Setenv("USER", "abc")
	t.Setenv("LOGNAME", "xy")
	t.Setenv("HOSTNAME", "vm")
	if err := scanAnonymous([]byte(`{"label":"abc-xy-vm"}`)); err != nil {
		t.Fatalf("scanAnonymous rejected ambiguous short identities: %v", err)
	}
}
