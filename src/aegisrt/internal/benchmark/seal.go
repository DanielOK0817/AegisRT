package benchmark

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	emailPattern         = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	windowsPathPattern   = regexp.MustCompile(`(?:^|[[:space:]"'=(:,;{}\[\]])[A-Za-z]:[\\/]`)
	credentialKeyPattern = regexp.MustCompile(`(?i)(?:"(?:token|auth[_-]?token|access[_-]?token|refresh[_-]?token|session[_-]?token|api[_-]?key|apikey|access[_-]?key|secret[_-]?access[_-]?key|client[_-]?secret|password|passwd|secret|credential|credentials|authorization|proxy[_-]?authorization|cookie|set[_-]?cookie|private[_-]?key|ssh[_-]?key)"[[:space:]]*:|(?:^|[[:space:],;{"'])(?:token|auth[_-]?token|access[_-]?token|refresh[_-]?token|session[_-]?token|api[_-]?key|apikey|access[_-]?key|secret[_-]?access[_-]?key|client[_-]?secret|password|passwd|secret|credential|credentials|authorization|proxy[_-]?authorization|cookie|set[_-]?cookie|private[_-]?key|ssh[_-]?key)[[:space:]]*[:=])`)
	runIDPattern         = regexp.MustCompile(`^run-[0-9a-f]{24}$`)
)

const minimumIdentityLength = 4

var sensitiveUnixPathPrefixes = []string{
	"/home/", "/root/", "/var/", "/tmp/", "/sys/", "/proc/", "/etc/", "/usr/",
	"/opt/", "/run/", "/dev/", "/mnt/", "/media/", "/srv/",
}

var identityEnvironmentVariables = []string{
	"USERNAME", "COMPUTERNAME", "USER", "LOGNAME", "HOSTNAME", "SUDO_USER", "USERDOMAIN",
}

func validateOutputParent(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("output parent directory is required")
	}
	parent := filepath.Clean(path)
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("output parent: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("output parent is not a directory")
	}
	return parent, nil
}

func validateRunID(runID string) error {
	if !runIDPattern.MatchString(runID) {
		return fmt.Errorf("run id must be run- followed by 24 lowercase hexadecimal characters")
	}
	return nil
}

func runPaths(parent, runID string) (target, staging string, err error) {
	if err := validateRunID(runID); err != nil {
		return "", "", err
	}
	target = filepath.Join(parent, runID)
	staging = filepath.Join(parent, "."+runID+".staging")
	if err := requireAbsent("target", target); err != nil {
		return "", "", err
	}
	if err := requireAbsent("staging", staging); err != nil {
		return "", "", err
	}
	return target, staging, nil
}

func requireAbsent(label, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("output %s already exists", label)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output %s: %w", label, err)
	}
	return nil
}

func createStaging(staging string) error {
	if err := os.Mkdir(staging, 0o700); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	return nil
}

func publishStaging(staging, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("output target already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output target: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("publish run: %w", err)
	}
	return nil
}

func randomRunID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return "run-" + hex.EncodeToString(buffer), nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func artifactFor(kind, path, media string, records int, data []byte) Artifact {
	sum := sha256.Sum256(data)
	return Artifact{Kind: kind, Path: path, MediaType: media, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Records: records}
}

func writeChecksums(dir string, names []string) error {
	sort.Strings(names)
	var output strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&output, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.ToSlash(name))
	}
	return writeExclusive(filepath.Join(dir, "SHA256SUMS"), []byte(output.String()))
}

func scanAnonymous(artifacts ...[]byte) error {
	identities := currentLocalIdentities()
	for _, artifact := range artifacts {
		text := string(artifact)
		lower := strings.ToLower(text)
		if emailPattern.MatchString(text) || windowsPathPattern.MatchString(text) || containsSensitiveUnixPath(text) {
			return fmt.Errorf("artifact contains an identity or absolute path pattern")
		}
		if strings.Contains(lower, "bearer ") || credentialKeyPattern.MatchString(text) {
			return fmt.Errorf("artifact contains blocked credential marker")
		}
		for _, identity := range identities {
			if strings.Contains(lower, identity) {
				return fmt.Errorf("artifact contains local identity")
			}
		}
	}
	return nil
}

func currentLocalIdentities() []string {
	identities := make([]string, 0, len(identityEnvironmentVariables))
	seen := make(map[string]struct{}, len(identityEnvironmentVariables))
	for _, variable := range identityEnvironmentVariables {
		identity := strings.ToLower(strings.TrimSpace(os.Getenv(variable)))
		if len(identity) < minimumIdentityLength {
			continue
		}
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	return identities
}

func containsSensitiveUnixPath(text string) bool {
	normalized := strings.NewReplacer(`\/`, "/", `\u002f`, "/", `\u002F`, "/").Replace(text)
	for _, prefix := range sensitiveUnixPathPrefixes {
		searchAt := 0
		for {
			relative := strings.Index(normalized[searchAt:], prefix)
			if relative < 0 {
				break
			}
			index := searchAt + relative
			if unixPathBoundary(normalized, index) && !insideHTTPURL(normalized, index) {
				return true
			}
			searchAt = index + len(prefix)
		}
	}
	return false
}

func unixPathBoundary(text string, index int) bool {
	if index == 0 {
		return true
	}
	previous := text[index-1]
	if previous <= ' ' {
		return true
	}
	return strings.ContainsRune(`"'=(),;[]{}/\\`, rune(previous))
}

func insideHTTPURL(text string, index int) bool {
	start := index
	for start > 0 {
		previous := text[start-1]
		if previous <= ' ' || strings.ContainsRune("\"'<>`()", rune(previous)) {
			break
		}
		start--
	}
	prefix := strings.ToLower(text[start:index])
	return strings.HasPrefix(prefix, "http://") || strings.HasPrefix(prefix, "https://")
}
