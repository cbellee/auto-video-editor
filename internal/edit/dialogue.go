package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// whisperBinaries lists the whisper.cpp CLI names ave will use to transcribe
// source dialogue, in preference order. Matches the names ave doctor checks.
var whisperBinaries = []string{"whisper-cli", "whisper-cpp"}

// continuityWindow is how close to a cut dialogue must fall on both sides for
// the boundary to count as a dialogue-carrying J/L cut.
const continuityWindow = 0.75

// continuityMinSpeech is the least speech, in seconds, that must sit inside the
// window on each side of a cut before continuity is recorded; it keeps a stray
// word from inventing a continuity relationship.
const continuityMinSpeech = 0.2

// dialogueClip is one Selected Segment's final source range to transcribe.
type dialogueClip struct {
	path  string
	start float64
	end   float64
}

// whisperTranscript mirrors the subset of whisper.cpp JSON output ave reads:
// the detected language and per-utterance millisecond offsets with text.
type whisperTranscript struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Offsets struct {
			From int `json:"from"`
			To   int `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	} `json:"transcription"`
}

// resolveWhisper finds the whisper.cpp CLI on PATH. Dialogue analysis underpins
// audio-aware cuts and ducking provenance, so a missing binary is a hard error
// that points the operator at ave doctor rather than silently skipping speech.
func resolveWhisper() (string, error) {
	for _, name := range whisperBinaries {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("whisper.cpp not found on PATH; run `ave doctor` and install whisper-cpp")
}

// transcribeDialogue runs whisper.cpp over every Selected Segment, returning the
// resolved language, whether it was detected or overridden, and the detected
// speech spans per clip in source-clip time. An explicit language override is
// honored as-is; otherwise the first clip's detected language is reported.
func transcribeDialogue(ctx context.Context, clips []dialogueClip, language string) (string, string, [][]DialogueSpan, error) {
	binary, err := resolveWhisper()
	if err != nil {
		return "", "", nil, err
	}
	requested := strings.TrimSpace(language)
	if requested == "" {
		requested = "auto"
	}

	spans := make([][]DialogueSpan, len(clips))
	detectedLanguage := ""
	for index, clip := range clips {
		language, clipSpans, err := transcribeClip(ctx, binary, clip, requested)
		if err != nil {
			return "", "", nil, err
		}
		spans[index] = clipSpans
		if detectedLanguage == "" && language != "" {
			detectedLanguage = language
		}
	}

	resolved := detectedLanguage
	source := "detected"
	if requested != "auto" {
		resolved = requested
		source = "override"
	}
	if strings.TrimSpace(resolved) == "" {
		resolved = "unknown"
	}
	return resolved, source, spans, nil
}

// transcribeClip extracts a 16 kHz mono WAV for one clip window and runs
// whisper.cpp over it, returning the detected language and speech spans shifted
// into source-clip time so they align with Selected Segment ranges.
func transcribeClip(ctx context.Context, binary string, clip dialogueClip, language string) (string, []DialogueSpan, error) {
	duration := clip.end - clip.start
	if duration <= 0 {
		return "", nil, fmt.Errorf("dialogue window %.2f-%.2f is empty", clip.start, clip.end)
	}

	wavPath, cleanup, err := tempWavFile()
	if err != nil {
		return "", nil, err
	}
	defer cleanup()

	extractArgs := []string{
		"-hide_banner", "-loglevel", "error",
		"-ss", formatSeconds(clip.start),
		"-i", clip.path,
		"-t", formatSeconds(duration),
		"-vn", "-ac", "1", "-ar", "16000",
		"-y", wavPath,
	}
	if output, err := exec.CommandContext(ctx, "ffmpeg", extractArgs...).CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("extract dialogue audio for %s: %s: %w",
			clip.path, strings.TrimSpace(string(output)), err)
	}

	prefix := strings.TrimSuffix(wavPath, ".wav")
	jsonPath := prefix + ".json"
	defer func() { _ = os.Remove(jsonPath) }()

	whisperArgs := []string{"-f", wavPath, "-l", language, "-oj", "-of", prefix}
	if output, err := exec.CommandContext(ctx, binary, whisperArgs...).CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("transcribe dialogue for %s: %s: %w",
			clip.path, strings.TrimSpace(string(output)), err)
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return "", nil, fmt.Errorf("read transcript for %s: %w", clip.path, err)
	}
	var transcript whisperTranscript
	if err := json.Unmarshal(data, &transcript); err != nil {
		return "", nil, fmt.Errorf("decode transcript for %s: %w", clip.path, err)
	}

	spans := make([]DialogueSpan, 0, len(transcript.Transcription))
	for _, utterance := range transcript.Transcription {
		start := clip.start + float64(utterance.Offsets.From)/1000.0
		end := clip.start + float64(utterance.Offsets.To)/1000.0
		if end <= start {
			continue
		}
		spans = append(spans, DialogueSpan{
			StartSecond: start,
			EndSecond:   end,
			Text:        strings.TrimSpace(utterance.Text),
		})
	}
	return strings.TrimSpace(transcript.Result.Language), spans, nil
}

// tempWavFile creates a temporary WAV sink and returns its path plus cleanup.
func tempWavFile() (string, func(), error) {
	file, err := os.CreateTemp("", "ave-dialogue-*.wav")
	if err != nil {
		return "", func() {}, fmt.Errorf("create dialogue audio file: %w", err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", func() {}, fmt.Errorf("close dialogue audio file: %w", closeErr)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

// detectContinuity records dialogue that bridges a cut: when the outgoing
// segment ends mid-speech and the incoming segment opens mid-speech, the
// boundary is a constrained J/L cut carrying related dialogue. The cut is an
// L-cut when the trailing speech dominates and a J-cut when the leading speech
// does, so the Edit Plan documents how dialogue crosses the boundary.
func detectContinuity(segments []SelectedSegment) []DialogueContinuity {
	var continuity []DialogueContinuity
	for index := 0; index+1 < len(segments); index++ {
		prev := segments[index]
		next := segments[index+1]
		tail := speechWithin(prev.Dialogue, prev.EndSecond-continuityWindow, prev.EndSecond)
		head := speechWithin(next.Dialogue, next.StartSecond, next.StartSecond+continuityWindow)
		if tail < continuityMinSpeech || head < continuityMinSpeech {
			continue
		}
		kind := continuityLCut
		seconds := tail
		if head > tail {
			kind = continuityJCut
			seconds = head
		}
		continuity = append(continuity, DialogueContinuity{
			FromSegment: index,
			ToSegment:   index + 1,
			Kind:        kind,
			Seconds:     roundSeconds(seconds),
		})
	}
	return continuity
}

// speechWithin sums the seconds of dialogue overlapping [from, to].
func speechWithin(spans []DialogueSpan, from, to float64) float64 {
	if to <= from {
		return 0
	}
	ordered := make([]DialogueSpan, len(spans))
	copy(ordered, spans)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].StartSecond < ordered[right].StartSecond
	})
	var total float64
	for _, span := range ordered {
		start := span.StartSecond
		if start < from {
			start = from
		}
		end := span.EndSecond
		if end > to {
			end = to
		}
		if end > start {
			total += end - start
		}
	}
	return total
}

// roundSeconds rounds a continuity duration to milliseconds so the Edit Plan
// records a stable, reproducible value.
func roundSeconds(seconds float64) float64 {
	return float64(int64(seconds*1000+0.5)) / 1000.0
}
