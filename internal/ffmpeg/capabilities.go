// Package ffmpeg provides shared FFmpeg capability parsing.
package ffmpeg

import "strings"

// CapabilityNames returns the named filters or encoders from FFmpeg list output.
func CapabilityNames(output string) map[string]bool {
	names := make(map[string]bool)
	for line := range strings.Lines(output) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] == "=" {
			continue
		}
		names[fields[1]] = true
	}
	return names
}
