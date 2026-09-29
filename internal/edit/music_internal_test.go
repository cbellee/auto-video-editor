package edit

import (
	"reflect"
	"testing"
)

func rankedFixture(relPath string, start, end, base float64) rankedCandidate {
	return rankedCandidate{
		eligibleCandidate: eligibleCandidate{relPath: relPath, rng: candidateRange{start: start, end: end}},
		base:              base,
	}
}

func TestCleanCuesSortsDedupesAndClamps(t *testing.T) {
	got := cleanCues([]float64{2.0, 2.0, -1.0, 1.0, 5.0, 12.0}, 10.0)
	want := []float64{1.0, 2.0, 5.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanCues = %v, want %v", got, want)
	}
}

func TestDerivePhrasesAndSections(t *testing.T) {
	beats := make([]float64, 64)
	for i := range beats {
		beats[i] = float64(i)
	}
	phrases, sections := derivePhrasesAndSections(beats)
	if len(phrases) != 8 {
		t.Errorf("expected 8 phrase cues for 64 beats, got %d", len(phrases))
	}
	if len(sections) != 2 {
		t.Errorf("expected 2 section cues for 64 beats, got %d", len(sections))
	}
	if phrases[0] != 0 || sections[0] != 0 {
		t.Errorf("expected the first beat to anchor phrase and section, got %v/%v", phrases[0], sections[0])
	}
}

func TestNearestPriorCueTrimsWithinTolerance(t *testing.T) {
	cues := []cuePriority{{musicCueBeat, []float64{1.0, 2.0, 3.0}}}
	cue, kind, ok := nearestPriorCue(cues, 2.2)
	if !ok || cue != 2.0 || kind != musicCueBeat {
		t.Fatalf("nearestPriorCue = (%v,%q,%v), want (2.0,beat,true)", cue, kind, ok)
	}
	if _, _, ok := nearestPriorCue(cues, 2.9); ok {
		t.Error("expected no snap when the nearest prior cue is beyond tolerance")
	}
}

func TestNearestPriorCueFallsBackThroughPriorities(t *testing.T) {
	cues := []cuePriority{
		{musicCueSection, nil},
		{musicCuePhrase, []float64{4.0}},
		{musicCueBeat, []float64{4.4}},
	}
	cue, kind, ok := nearestPriorCue(cues, 4.5)
	if !ok || cue != 4.0 || kind != musicCuePhrase {
		t.Fatalf("expected phrase fallback to 4.0, got (%v,%q,%v)", cue, kind, ok)
	}
}

func TestIsMajorChange(t *testing.T) {
	a := rankedFixture("a.mp4", 0, 5, 0.8)
	sameClipSmallGap := rankedFixture("a.mp4", 5, 10, 0.75)
	if isMajorChange(a, sameClipSmallGap) {
		t.Error("same clip with a small score gap should not be a major change")
	}
	differentClip := rankedFixture("b.mp4", 0, 5, 0.79)
	if !isMajorChange(a, differentClip) {
		t.Error("a different clip should be a major change")
	}
	bigGap := rankedFixture("a.mp4", 5, 10, 0.4)
	if !isMajorChange(a, bigGap) {
		t.Error("a large score gap should be a major change")
	}
}

func TestSnapCutsToMusicPrefersBeatsAndTrims(t *testing.T) {
	selected := []rankedCandidate{
		rankedFixture("a.mp4", 0, 5.4, 0.8), // natural cut at 5.4; nearest beat 5.0
		rankedFixture("a.mp4", 0, 4.0, 0.79),
	}
	cues := MusicCues{Beats: []float64{5.0, 10.0}}
	snaps := snapCutsToMusic(selected, cues)
	if snaps[0].cue != musicCueBeat {
		t.Errorf("first cut cue = %q, want beat", snaps[0].cue)
	}
	if snaps[0].end != 5.0 {
		t.Errorf("first cut end = %.2f, want it trimmed to the 5.0 beat", snaps[0].end)
	}
	if snaps[1].cue != "" {
		t.Errorf("the last segment must never be snapped, got cue %q", snaps[1].cue)
	}
	if snaps[1].end != 4.0 {
		t.Errorf("last segment end = %.2f, want its natural 4.0", snaps[1].end)
	}
}

func TestSnapCutsToMusicKeepsCutWhenNoNearbyCue(t *testing.T) {
	selected := []rankedCandidate{
		rankedFixture("a.mp4", 0, 5.0, 0.8),
		rankedFixture("a.mp4", 0, 4.0, 0.8),
	}
	cues := MusicCues{Beats: []float64{9.0}} // far from the 5.0 cut
	snaps := snapCutsToMusic(selected, cues)
	if snaps[0].cue != "" || snaps[0].end != 5.0 {
		t.Errorf("expected the cut to stay at 5.0 with no cue, got (%.2f,%q)", snaps[0].end, snaps[0].cue)
	}
}

func TestSnapCutsToMusicRespectsMinimumSegment(t *testing.T) {
	selected := []rankedCandidate{
		rankedFixture("a.mp4", 0, 0.9, 0.8), // beat at 0.6 would leave a 0.6s segment
		rankedFixture("a.mp4", 0, 4.0, 0.8),
	}
	cues := MusicCues{Beats: []float64{0.6, 5.0}}
	snaps := snapCutsToMusic(selected, cues)
	if snaps[0].end != 0.9 || snaps[0].cue != "" {
		t.Errorf("expected no snap below the minimum segment floor, got (%.2f,%q)", snaps[0].end, snaps[0].cue)
	}
}

func TestSnapCutsToMusicUsesPhraseForMajorChange(t *testing.T) {
	selected := []rankedCandidate{
		rankedFixture("a.mp4", 0, 8.3, 0.8),
		rankedFixture("b.mp4", 0, 4.0, 0.8), // different clip => major change
	}
	cues := MusicCues{
		Beats:   []float64{8.2, 16.0},
		Phrases: []float64{8.0, 16.0},
	}
	snaps := snapCutsToMusic(selected, cues)
	if snaps[0].cue != musicCuePhrase {
		t.Errorf("major change cut cue = %q, want phrase", snaps[0].cue)
	}
	if snaps[0].end != 8.0 {
		t.Errorf("major change cut end = %.2f, want the 8.0 phrase boundary", snaps[0].end)
	}
}

func TestValidateMusicRequiresProvenance(t *testing.T) {
	good := AudioSettings{Source: audioMusic, Music: &MusicSettings{
		Path:            "song.mp3",
		Fingerprint:     "v1:1:ab",
		DurationSeconds: 60,
		FadeOutSeconds:  2,
		Cues:            MusicCues{Beats: []float64{0.5, 1.0}},
	}}
	if err := validateMusic(good); err != nil {
		t.Fatalf("validateMusic(good) = %v, want nil", err)
	}

	cases := map[string]AudioSettings{
		"missing settings": {Source: audioMusic},
		"stray settings":   {Source: audioSource, Music: &MusicSettings{Path: "x"}},
		"no beats": {Source: audioMusic, Music: &MusicSettings{
			Path: "s", Fingerprint: "f", DurationSeconds: 1, FadeOutSeconds: 1}},
	}
	for name, audio := range cases {
		if err := validateMusic(audio); err == nil {
			t.Errorf("validateMusic(%s) = nil, want error", name)
		}
	}
}
