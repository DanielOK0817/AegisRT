# Third-party inventory

AegisRT production runtime code primarily uses the Go standard library. The
module also includes the following dependencies:

| Dependency | Version | Use | Source | License |
| --- | --- | --- | --- | --- |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.2` | Validate experiment evidence against JSON Schema in tests | https://github.com/santhosh-tekuri/jsonschema | Apache-2.0 |
| `golang.org/x/text` | `v0.14.0` | Indirect dependency of `jsonschema/v6` | https://pkg.go.dev/golang.org/x/text | BSD-3-Clause |

Future dependencies must be recorded here with their version, purpose, source,
and license before they are merged. AegisRT is a new implementation and does
not copy source code from an existing multi-agent application or Agent Runtime.
