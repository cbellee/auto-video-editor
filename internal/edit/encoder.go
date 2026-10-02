package edit

import (
	"fmt"
	"math"
	"strings"
)

// Encoder selection modes exposed through the --encoder flag.
const (
	encoderAuto     = "auto"
	encoderHardware = "hardware"
	encoderSoftware = "software"
)

// H.264 encoder names in FFmpeg capability output.
const (
	hardwareH264Codec = "h264_videotoolbox"
	softwareH264Codec = "libx264"
)

// Quality targets. Software uses a constant rate factor; hardware uses an
// explicit bitrate derived from the canvas so VideoToolbox output stays close
// to the software baseline instead of ballooning at its default quality.
const (
	softwareH264CRF   = 20
	hardwareBitsPerPx = 0.10
	minHardwareKbps   = 2000
)

// normalizeEncoderMode validates the requested encoder strategy, defaulting an
// empty value to auto.
func normalizeEncoderMode(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "", encoderAuto:
		return encoderAuto, nil
	case encoderHardware:
		return encoderHardware, nil
	case encoderSoftware:
		return encoderSoftware, nil
	default:
		return "", fmt.Errorf("unsupported encoder %q; use auto, hardware, or software", mode)
	}
}

// resolveVideoEncoder picks the H.264 encoder for the requested mode given the
// encoders FFmpeg reports as available. auto prefers hardware and falls back to
// software; hardware and software fail with an actionable error rather than
// silently substituting a different encoder.
func resolveVideoEncoder(mode string, available map[string]bool) (string, error) {
	switch mode {
	case encoderAuto:
		switch {
		case available[hardwareH264Codec]:
			return hardwareH264Codec, nil
		case available[softwareH264Codec]:
			return softwareH264Codec, nil
		default:
			return "", fmt.Errorf("FFmpeg does not provide a supported H.264 encoder")
		}
	case encoderHardware:
		if !available[hardwareH264Codec] {
			return "", fmt.Errorf(
				"hardware H.264 encoder %s is not available; re-run with --encoder software or --encoder auto",
				hardwareH264Codec)
		}
		return hardwareH264Codec, nil
	case encoderSoftware:
		if !available[softwareH264Codec] {
			return "", fmt.Errorf(
				"software H.264 encoder %s is not available; install an FFmpeg build with libx264",
				softwareH264Codec)
		}
		return softwareH264Codec, nil
	default:
		return "", fmt.Errorf("unsupported encoder %q; use auto, hardware, or software", mode)
	}
}

// hardwareBitrateKbps derives an explicit bitrate target (in kbps) for hardware
// H.264 from the canvas and frame rate, floored so small canvases still get a
// reasonable allocation.
func hardwareBitrateKbps(width, height, frameRate int) int {
	bitsPerSecond := float64(width) * float64(height) * float64(frameRate) * hardwareBitsPerPx
	kbps := int(math.Round(bitsPerSecond / 1000))
	if kbps < minHardwareKbps {
		kbps = minHardwareKbps
	}
	return kbps
}

// applyQualityTarget sets the quality fields for the resolved encoder: a CRF for
// software, or an explicit bitrate for hardware.
func applyQualityTarget(settings *RenderSettings) {
	switch settings.VideoCodec {
	case hardwareH264Codec:
		kbps := hardwareBitrateKbps(settings.VideoWidth, settings.VideoHeight, settings.VideoFrameRate)
		settings.VideoBitrate = fmt.Sprintf("%dk", kbps)
		settings.VideoCRF = 0
	case softwareH264Codec:
		settings.VideoCRF = softwareH264CRF
		settings.VideoBitrate = ""
	}
}

// videoQualityArgs returns the FFmpeg arguments that enforce the recorded
// quality target. A bitrate target (hardware) also sets maxrate and bufsize so
// VideoToolbox stays near the target; a CRF target (software) pairs with a
// deterministic preset.
func videoQualityArgs(settings RenderSettings) []string {
	if settings.VideoBitrate != "" {
		kbps := parseKbps(settings.VideoBitrate)
		return []string{
			"-b:v", settings.VideoBitrate,
			"-maxrate", fmt.Sprintf("%dk", kbps*3/2),
			"-bufsize", fmt.Sprintf("%dk", kbps*2),
		}
	}
	if settings.VideoCRF > 0 {
		return []string{
			"-crf", fmt.Sprintf("%d", settings.VideoCRF),
			"-preset", "medium",
		}
	}
	return nil
}

// parseKbps reads the leading integer from a bitrate string like "2800k".
func parseKbps(bitrate string) int {
	trimmed := strings.TrimSuffix(strings.TrimSpace(bitrate), "k")
	kbps := 0
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			break
		}
		kbps = kbps*10 + int(r-'0')
	}
	return kbps
}
