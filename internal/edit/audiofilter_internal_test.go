package edit

import (
	"strings"
	"testing"
)

func TestNormalizeDuckProfile(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", duckBalanced, false},
		{"subtle", duckSubtle, false},
		{"balanced", duckBalanced, false},
		{"strong", duckStrong, false},
		{"loud", "", true},
	}
	for _, tc := range cases {
		got, err := normalizeDuckProfile(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeDuckProfile(%q) error = nil, want error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("normalizeDuckProfile(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestDuckParamsOrdering(t *testing.T) {
	subtleT, subtleR := duckParams(duckSubtle)
	balT, balR := duckParams(duckBalanced)
	strongT, strongR := duckParams(duckStrong)
	// Stronger ducking means a lower threshold and a higher ratio.
	if !(subtleT > balT && balT > strongT) {
		t.Errorf("thresholds not descending: subtle=%.3f balanced=%.3f strong=%.3f", subtleT, balT, strongT)
	}
	if !(subtleR < balR && balR < strongR) {
		t.Errorf("ratios not ascending: subtle=%.1f balanced=%.1f strong=%.1f", subtleR, balR, strongR)
	}
}

func baseAudioSettings() RenderSettings {
	return RenderSettings{AudioSampleRate: 48000, AudioChannelLayout: "stereo"}
}

func TestBuildAudioFilterSingleSegmentNoMusic(t *testing.T) {
	var b strings.Builder
	label := buildAudioFilter(&b,
		[]audioSegmentPlan{{label: "a_0_0", start: 0, end: 6}},
		nil, 0, duckBalanced, 6, baseAudioSettings())
	got := b.String()
	if label != "[audio]" {
		t.Errorf("map label = %q, want [audio]", label)
	}
	for _, want := range []string{"[a_0_0]atrim=start=0", "loudnorm=", "alimiter=limit=0.95", "atrim=0:6", "[audio]"} {
		if !strings.Contains(got, want) {
			t.Errorf("filter missing %q:\n%s", want, got)
		}
	}
	// No music: no ducking or crossfade.
	for _, absent := range []string{"sidechaincompress", "acrossfade", "amix"} {
		if strings.Contains(got, absent) {
			t.Errorf("single-segment no-music filter should not contain %q:\n%s", absent, got)
		}
	}
}

func TestBuildAudioFilterCrossfadesSegments(t *testing.T) {
	var b strings.Builder
	buildAudioFilter(&b,
		[]audioSegmentPlan{
			{label: "a_0_0", start: 0, end: 6},
			{label: "a_1_0", start: 0, end: 6},
		},
		nil, 0, duckBalanced, 12, baseAudioSettings())
	got := b.String()
	if !strings.Contains(got, "acrossfade=d=0.25") {
		t.Errorf("expected a short crossfade between segments:\n%s", got)
	}
}

func TestBuildAudioFilterSilentSegmentUsesSilence(t *testing.T) {
	var b strings.Builder
	buildAudioFilter(&b,
		[]audioSegmentPlan{{label: "sil_0", start: 2, end: 8, silent: true}},
		nil, 0, duckBalanced, 6, baseAudioSettings())
	got := b.String()
	// A silent segment trims the silent source from 0 for its duration (6s),
	// not from the source range.
	if !strings.Contains(got, "[sil_0]atrim=start=0.000000:end=6.000000") {
		t.Errorf("silent segment should trim silence for its duration:\n%s", got)
	}
}

func TestBuildAudioFilterDucksMusicUnderDialogue(t *testing.T) {
	var b strings.Builder
	music := &musicRender{path: "/tmp/track.mp3", fadeOut: 2}
	buildAudioFilter(&b,
		[]audioSegmentPlan{{label: "a_0_0", start: 0, end: 6}},
		music, 3, duckStrong, 6, baseAudioSettings())
	got := b.String()
	for _, want := range []string{
		"[3:a:0]atrim=0:6",
		"afade=t=out",
		"[srcnorm]asplit=2[srckey][srcmix]",
		"sidechaincompress=threshold=0.050:ratio=8.0",
		"amix=inputs=2:normalize=0[premix]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("music-ducking filter missing %q:\n%s", want, got)
		}
	}
}
