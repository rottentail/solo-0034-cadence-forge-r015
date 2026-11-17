package domain

// RoundFrames converts a millisecond duration to a frame count at the given
// frame rate using nearest-integer rounding, matching the frame count the fps
// filter emits for the trimmed segment.
func RoundFrames(durationMS int64, fps int) int64 {
	if fps <= 0 {
		return 0
	}
	return (durationMS*int64(fps) + 500) / 1000
}
