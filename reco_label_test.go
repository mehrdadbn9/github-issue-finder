package main

import "testing"

// The headline and the "How to claim" line are printed in the same message, so
// they must not contradict each other. kubernetes#141216 went out scoring 1.04
// with the headline "Strong claim" sitting directly above "not triaged yet -
// no maintainer has confirmed it. Claiming now is noise; watch it instead".
func TestRecoLabelAgreesWithNextStep(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		step  NextStep
		want  string
	}{
		{"untriaged outranks a high score", 1.04, StepTriage, "Watch (untriaged)"},
		{"repro wanted outranks a high score", 1.20, StepRepro, "Reproduce first"},
		{"design work outranks a high score", 1.10, StepDiscuss, "Discuss first"},
		{"claimable keeps the score headline", 1.04, StepPR, "Strong claim"},
		{"prow assign keeps the score headline", 0.90, StepProwAssign, "High fit"},
		{"ask keeps the score headline", 0.72, StepAsk, "Worth it"},
		{"low score still reads low", 0.30, StepPR, "Low priority"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recoLabel(tt.score, tt.step); got != tt.want {
				t.Errorf("recoLabel(%.2f, %q) = %q, want %q", tt.score, tt.step, got, tt.want)
			}
		})
	}
}

// A "do not claim yet" headline is worthless if the help text underneath still
// tells you how to claim, so pin that they stay in sync.
func TestNoClaimStepsExplainWhyNot(t *testing.T) {
	for _, step := range []NextStep{StepTriage, StepRepro, StepDiscuss} {
		help := stepHelp(step, ConvProw)
		if help == conventionHelp(ConvProw) {
			t.Errorf("step %q fell through to the convention help; it should explain why not to claim", step)
		}
	}
}
