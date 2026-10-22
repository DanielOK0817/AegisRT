package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"aegisrt/internal/benchmark"
)

// Set by the reproducible build wrapper after hashing the source inventory.
var communicationSourceSHA256 string

func parseCommunicationOptions(args []string) (cliOptions, error) {
	set := flag.NewFlagSet("communication", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var output, profile string
	set.StringVar(&output, "output", "", "existing parent for a communication run")
	set.StringVar(&profile, "profile", "smoke", "fixed local communication profile")
	if err := set.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if set.NArg() != 0 || strings.TrimSpace(output) == "" || profile != "smoke" {
		return cliOptions{}, fmt.Errorf("communication requires --output and supports only --profile smoke")
	}
	return cliOptions{kind: commandCommunication, output: output, profile: profile,
		communicationPlan: benchmark.NewCommunicationSmokePlan()}, nil
}
