package main

import (
	"fmt"

	"aegisrt/internal/contextvm"
)

func localContextComponents(options daemonOptions) (contextvm.NamedCounter, contextvm.NamedSummarizer, error) {
	var counter contextvm.NamedCounter
	switch options.contextCounter {
	case "", "whitespace-v1":
		counter = contextvm.WhitespaceCounter{}
	case "unicode-segment-v1":
		counter = contextvm.UnicodeSegmentCounter{}
	default:
		return nil, nil, fmt.Errorf("context-counter must be whitespace-v1 or unicode-segment-v1")
	}
	var summarizer contextvm.NamedSummarizer
	switch options.contextCompression {
	case "", "none-v1":
	case "deterministic-extractive-v1":
		var err error
		summarizer, err = contextvm.NewExtractiveSummarizer(counter)
		if err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("context-compression must be none-v1 or deterministic-extractive-v1")
	}
	return counter, summarizer, nil
}
