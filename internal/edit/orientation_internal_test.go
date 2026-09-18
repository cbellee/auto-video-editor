package edit

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectOrientationByMajorityDuration(t *testing.T) {
	clips := []sourceClip{
		{width: 1080, height: 1920, duration: 10},
		{width: 1920, height: 1080, duration: 4},
	}
	got, err := selectOrientation(clips, Options{})
	if err != nil {
		t.Fatalf("selectOrientation returned error: %v", err)
	}
	if got != orientationPortrait {
		t.Errorf("orientation = %v, want portrait", got)
	}
}

func TestSelectOrientationAspectOverride(t *testing.T) {
	clips := []sourceClip{{width: 1080, height: 1920, duration: 10}}
	got, err := selectOrientation(clips, Options{Aspect: "landscape"})
	if err != nil {
		t.Fatalf("selectOrientation returned error: %v", err)
	}
	if got != orientationLandscape {
		t.Errorf("orientation = %v, want landscape override", got)
	}
}

func TestResolveOrientationTieInteractive(t *testing.T) {
	tests := map[string]orientation{
		"portrait\n":  orientationPortrait,
		"landscape\n": orientationLandscape,
		"p\n":         orientationPortrait,
		"nope\nl\n":   orientationLandscape,
	}
	for input, want := range tests {
		var prompt bytes.Buffer
		options := Options{Interactive: true, Stdin: strings.NewReader(input), Prompt: &prompt}
		got, err := resolveOrientationTie(options)
		if err != nil {
			t.Fatalf("input %q: unexpected error: %v", input, err)
		}
		if got != want {
			t.Errorf("input %q: orientation = %v, want %v", input, got, want)
		}
		if prompt.Len() == 0 {
			t.Errorf("input %q: expected a prompt to be written", input)
		}
	}
}

func TestResolveOrientationTieNonInteractive(t *testing.T) {
	_, err := resolveOrientationTie(Options{Interactive: false})
	if err == nil {
		t.Fatal("expected error when a tie cannot be resolved without a terminal")
	}
	if !strings.Contains(err.Error(), "--aspect") {
		t.Errorf("error = %q, want guidance to pass --aspect", err)
	}
}
