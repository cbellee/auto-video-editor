package edit

import (
	"fmt"
	"strconv"
	"strings"
)

// Music ducking profiles tune how far the Music Track drops beneath dialogue.
const (
	duckSubtle   = "subtle"
	duckBalanced = "balanced"
	duckStrong   = "strong"
)

var knownDuckProfiles = map[string]bool{
	duckSubtle:   true,
	duckBalanced: true,
	duckStrong:   true,
}

// audioCrossfadeSeconds is the short overlap used to join adjacent Selected
// Segments' audio so cuts do not click. It stays well under the one-second
// minimum segment length.
const audioCrossfadeSeconds = 0.25

// Final loudness and clip-protection targets applied to every Finished Video.
const (
	loudnormIntegrated   = -16.0
	loudnormTruePeak     = -1.5
	loudnormRange        = 11.0
	limiterCeilingLinear = 0.95
)

// normalizeDuckProfile validates the ducking profile, defaulting empty to
// balanced.
func normalizeDuckProfile(profile string) (string, error) {
	switch strings.TrimSpace(profile) {
	case "":
		return duckBalanced, nil
	case duckSubtle, duckBalanced, duckStrong:
		return strings.TrimSpace(profile), nil
	default:
		return "", fmt.Errorf("unsupported ducking profile %q; use subtle, balanced, or strong", profile)
	}
}

// duckParams returns the sidechain compressor threshold (linear) and ratio for
// a ducking profile. A lower threshold and higher ratio duck the music harder.
func duckParams(profile string) (threshold float64, ratio float64) {
	switch profile {
	case duckSubtle:
		return 0.2, 2
	case duckStrong:
		return 0.05, 8
	default: // balanced
		return 0.1, 4
	}
}

// audioSegmentPlan is one Selected Segment's contribution to the source-audio
// bed. label is a pre-split branch to consume; when silent it is a slice of a
// silent source and start is ignored.
type audioSegmentPlan struct {
	label  string
	start  float64
	end    float64
	silent bool
}

// buildAudioFilter appends the Finished Video's audio graph to filter and
// returns the final audio map label. Each Selected Segment's source audio is
// trimmed (or replaced with silence when unusable), the segments are joined
// with short crossfades and loudness-normalized, an optional Music Track is
// ducked beneath the source audio and mixed back in, and the result is
// limited against clipping and padded or trimmed to the exact edit length.
func buildAudioFilter(
	filter *strings.Builder,
	segments []audioSegmentPlan,
	music *musicRender,
	musicInputIndex int,
	duckProfile string,
	totalDuration float64,
	settings RenderSettings,
) string {
	sampleFormat := fmt.Sprintf(
		"aformat=sample_rates=%d:channel_layouts=%s",
		settings.AudioSampleRate,
		settings.AudioChannelLayout,
	)

	for index, segment := range segments {
		start := segment.start
		end := segment.end
		if segment.silent {
			start = 0
			end = segment.end - segment.start
		}
		fmt.Fprintf(filter,
			"[%s]atrim=start=%s:end=%s,asetpts=N/SR/TB,%s[aseg%d];",
			segment.label,
			strconv.FormatFloat(start, 'f', 6, 64),
			strconv.FormatFloat(end, 'f', 6, 64),
			sampleFormat,
			index,
		)
	}

	source := "aseg0"
	for index := 1; index < len(segments); index++ {
		joined := fmt.Sprintf("acat%d", index)
		fmt.Fprintf(filter,
			"[%s][aseg%d]acrossfade=d=%s[%s];",
			source,
			index,
			strconv.FormatFloat(audioCrossfadeSeconds, 'f', 6, 64),
			joined,
		)
		source = joined
	}

	fmt.Fprintf(filter,
		"[%s]loudnorm=I=%s:TP=%s:LRA=%s[srcnorm];",
		source,
		strconv.FormatFloat(loudnormIntegrated, 'f', 1, 64),
		strconv.FormatFloat(loudnormTruePeak, 'f', 1, 64),
		strconv.FormatFloat(loudnormRange, 'f', 1, 64),
	)

	premix := "srcnorm"
	if music != nil {
		fadeStart := totalDuration - music.fadeOut
		if fadeStart < 0 {
			fadeStart = 0
		}
		fmt.Fprintf(filter,
			"[%d:a:0]atrim=0:%s,asetpts=N/SR/TB",
			musicInputIndex,
			strconv.FormatFloat(totalDuration, 'f', 6, 64),
		)
		if music.fadeOut > 0 {
			fmt.Fprintf(filter,
				",afade=t=out:st=%s:d=%s",
				strconv.FormatFloat(fadeStart, 'f', 6, 64),
				strconv.FormatFloat(music.fadeOut, 'f', 6, 64),
			)
		}
		fmt.Fprintf(filter, ",%s[musicbed];", sampleFormat)

		// Split the normalized source audio so it both keys the ducking and
		// survives in the final mix: dialogue stays audible while the music
		// drops beneath it.
		filter.WriteString("[srcnorm]asplit=2[srckey][srcmix];")
		threshold, ratio := duckParams(duckProfile)
		fmt.Fprintf(filter,
			"[musicbed][srckey]sidechaincompress=threshold=%s:ratio=%s:attack=20:release=250[ducked];",
			strconv.FormatFloat(threshold, 'f', 3, 64),
			strconv.FormatFloat(ratio, 'f', 1, 64),
		)
		filter.WriteString("[ducked][srcmix]amix=inputs=2:normalize=0[premix];")
		premix = "premix"
	}

	fmt.Fprintf(filter,
		"[%s]apad,atrim=0:%s,alimiter=limit=%s,%s[audio]",
		premix,
		strconv.FormatFloat(totalDuration, 'f', 6, 64),
		strconv.FormatFloat(limiterCeilingLinear, 'f', 2, 64),
		sampleFormat,
	)
	return "[audio]"
}
