package edit

import (
	"strings"
	"testing"
)

func TestNormalizeEncoderMode(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", encoderAuto, false},
		{"auto", encoderAuto, false},
		{"hardware", encoderHardware, false},
		{"software", encoderSoftware, false},
		{" software ", encoderSoftware, false},
		{"turbo", "", true},
	}
	for _, tc := range cases {
		got, err := normalizeEncoderMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeEncoderMode(%q) error = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeEncoderMode(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("normalizeEncoderMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveVideoEncoder(t *testing.T) {
	both := map[string]bool{hardwareH264Codec: true, softwareH264Codec: true}
	softwareOnly := map[string]bool{softwareH264Codec: true}
	hardwareOnly := map[string]bool{hardwareH264Codec: true}
	none := map[string]bool{}

	cases := []struct {
		name      string
		mode      string
		available map[string]bool
		want      string
		wantErr   string
	}{
		{"auto prefers hardware", encoderAuto, both, hardwareH264Codec, ""},
		{"auto falls back to software", encoderAuto, softwareOnly, softwareH264Codec, ""},
		{"auto with none fails", encoderAuto, none, "", "supported H.264 encoder"},
		{"hardware uses hardware", encoderHardware, hardwareOnly, hardwareH264Codec, ""},
		{"hardware without hardware fails", encoderHardware, softwareOnly, "", "hardware H.264 encoder"},
		{"software uses software", encoderSoftware, softwareOnly, softwareH264Codec, ""},
		{"software without software fails", encoderSoftware, hardwareOnly, "", "software H.264 encoder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveVideoEncoder(tc.mode, tc.available)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("codec = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHardwareBitrateKbps(t *testing.T) {
	// 1280*720*30*0.10 = 2,764,800 bits/s -> 2765 kbps.
	if got := hardwareBitrateKbps(1280, 720, 30); got != 2765 {
		t.Errorf("720p30 bitrate = %d kbps, want 2765", got)
	}
	// Tiny canvas is floored to the minimum allocation.
	if got := hardwareBitrateKbps(320, 240, 24); got != minHardwareKbps {
		t.Errorf("small canvas bitrate = %d kbps, want floor %d", got, minHardwareKbps)
	}
}

func TestApplyQualityTarget(t *testing.T) {
	hw := RenderSettings{VideoCodec: hardwareH264Codec, VideoWidth: 1280, VideoHeight: 720, VideoFrameRate: 30}
	applyQualityTarget(&hw)
	if hw.VideoBitrate == "" {
		t.Error("hardware encoder should receive an explicit bitrate")
	}
	if hw.VideoCRF != 0 {
		t.Errorf("hardware encoder CRF = %d, want 0", hw.VideoCRF)
	}

	sw := RenderSettings{VideoCodec: softwareH264Codec, VideoWidth: 1280, VideoHeight: 720, VideoFrameRate: 30}
	applyQualityTarget(&sw)
	if sw.VideoCRF != softwareH264CRF {
		t.Errorf("software CRF = %d, want %d", sw.VideoCRF, softwareH264CRF)
	}
	if sw.VideoBitrate != "" {
		t.Errorf("software bitrate = %q, want empty", sw.VideoBitrate)
	}
}

func TestVideoQualityArgs(t *testing.T) {
	bitrateArgs := strings.Join(videoQualityArgs(RenderSettings{VideoBitrate: "2800k"}), " ")
	for _, want := range []string{"-b:v 2800k", "-maxrate 4200k", "-bufsize 5600k"} {
		if !strings.Contains(bitrateArgs, want) {
			t.Errorf("bitrate args %q missing %q", bitrateArgs, want)
		}
	}

	crfArgs := strings.Join(videoQualityArgs(RenderSettings{VideoCRF: 20}), " ")
	for _, want := range []string{"-crf 20", "-preset medium"} {
		if !strings.Contains(crfArgs, want) {
			t.Errorf("crf args %q missing %q", crfArgs, want)
		}
	}

	if got := videoQualityArgs(RenderSettings{}); got != nil {
		t.Errorf("no quality target should yield nil args, got %v", got)
	}
}
