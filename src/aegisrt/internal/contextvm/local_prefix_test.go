package contextvm

import (
	"context"
	"testing"
)

type opaqueLocalCounter struct{ Counter }

func TestLocalPrefixMatchesExhaustiveUnicodeAndWhitespaceSelection(t *testing.T) {
	for _, counter := range []Counter{WhitespaceCounter{}, UnicodeSegmentCounter{}} {
		fast, _ := NewExtractiveSummarizer(counter)
		reference, _ := NewExtractiveSummarizer(opaqueLocalCounter{counter})
		for _, text := range []string{"alpha beta gamma delta", "中英文 Mixed_text 123，测试!", "a\n\nb\t中文🙂 cafe\u0301", "第一行\n第二行 third"} {
			page, err := NewPage(PageSpec{Content: text, Scope: ScopeSystem}, counter)
			if err != nil {
				t.Fatal(err)
			}
			for budget := 1; budget <= page.TokenCount; budget++ {
				request := SummaryRequest{Pages: []Page{page}, TokenBudget: budget}
				got, err := fast.Summarize(context.Background(), request)
				want, referenceErr := reference.Summarize(context.Background(), request)
				if err != nil || referenceErr != nil || got != want {
					t.Fatalf("text=%q budget=%d got=%q want=%q errors=%v/%v", text, budget, got, want, err, referenceErr)
				}
			}
		}
	}
}
