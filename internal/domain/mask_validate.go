package domain

import (
	"regexp"
	"strings"
)

var labelPattern = regexp.MustCompile(`^obj-[a-z0-9]{1,24}$`)

const (
	MaxMaskObjects   = 8
	MaxKeyframes     = 48
	MaxKeyframeGapMS = int64(1000)
	MinBoxSide       = 2
	// gateErodePX mirrors privacy.GateErodePX: the pixel gate samples only the
	// box interior inset by this many pixels, so the validated box must keep a
	// gateable interior after padding and clamping.
	gateErodePX = 2
)

// ValidateMask validates an immutable mask revision against the probed source.
// Bounds are deliberately conservative: boxes live in source pixel space, pins
// must be densely spaced enough that linear interpolation plus the motion pad
// cannot leave a fast-moving target exposed between pins.
func ValidateMask(m Mask, source Source) error {
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 160 {
		return Invalid("mask name must contain 1..160 bytes")
	}
	if source.ID == "" {
		return Missing("source", m.SourceID)
	}
	if m.SourceID != source.ID {
		return Invalid("mask source does not match the pinned source")
	}
	if !source.Media.Video {
		return Invalid("privacy masks require a video source")
	}
	width, height := source.Media.Width, source.Media.Height
	if len(m.Objects) == 0 || len(m.Objects) > MaxMaskObjects {
		return Invalid("a mask requires 1..%d objects", MaxMaskObjects)
	}
	labels := map[string]bool{}
	for oi, obj := range m.Objects {
		if !labelPattern.MatchString(obj.Label) {
			return Invalid("object %d label must match obj-[a-z0-9]{1,24} and stay opaque", oi)
		}
		if labels[obj.Label] {
			return Invalid("object label %s is duplicated", obj.Label)
		}
		labels[obj.Label] = true
		if obj.ActiveMS[0] < 0 || obj.ActiveMS[1] <= obj.ActiveMS[0] || obj.ActiveMS[1] > source.Media.DurationMS {
			return Invalid("object %s active interval is outside source duration", obj.Label)
		}
		if len(obj.Keyframes) == 0 || len(obj.Keyframes) > MaxKeyframes {
			return Invalid("object %s requires 1..%d keyframes", obj.Label, MaxKeyframes)
		}
		last := int64(-1)
		for ki, kf := range obj.Keyframes {
			if kf.AtMS < obj.ActiveMS[0] || kf.AtMS > obj.ActiveMS[1] {
				return Invalid("object %s keyframe %d is outside its active interval", obj.Label, ki)
			}
			if kf.AtMS <= last {
				return Invalid("object %s keyframes must be strictly increasing in time", obj.Label)
			}
			last = kf.AtMS
			if kf.Width < MinBoxSide || kf.Height < MinBoxSide {
				return Invalid("object %s keyframe %d box is too small", obj.Label, ki)
			}
			if kf.X < 0 || kf.Y < 0 || kf.X+kf.Width > width || kf.Y+kf.Height > height {
				return Invalid("object %s keyframe %d box leaves source frame bounds", obj.Label, ki)
			}
			// After the interior gate erosion the target must still have cover.
			if kf.Width <= 2*gateErodePX+2 || kf.Height <= 2*gateErodePX+2 {
				return Invalid("object %s keyframe %d box leaves no gateable interior", obj.Label, ki)
			}
		}
		if obj.Keyframes[0].AtMS != obj.ActiveMS[0] || obj.Keyframes[len(obj.Keyframes)-1].AtMS != obj.ActiveMS[1] {
			return Invalid("object %s needs pins at both active interval edges (no extrapolation)", obj.Label)
		}
		for ki := 1; ki < len(obj.Keyframes); ki++ {
			gap := obj.Keyframes[ki].AtMS - obj.Keyframes[ki-1].AtMS
			if gap > MaxKeyframeGapMS {
				return Invalid("object %s keyframe gap %dms exceeds %dms; add pins for fast motion", obj.Label, gap, MaxKeyframeGapMS)
			}
		}
	}
	return nil
}
