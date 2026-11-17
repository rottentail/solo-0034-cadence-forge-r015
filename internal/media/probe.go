package media

import (
	"cadenceforge/internal/domain"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Prober struct{ Binary string }

// frameRate parses an ffprobe rational frame rate ("30000/1001") into the
// nearest integer fps. Variable or unparseable rates fall back to 0 so callers
// can decide rather than silently assuming a cadence.
func frameRate(rational string) int {
	num, den, found := strings.Cut(rational, "/")
	if !found {
		return 0
	}
	n, err1 := strconv.ParseFloat(strings.TrimSpace(num), 64)
	d, err2 := strconv.ParseFloat(strings.TrimSpace(den), 64)
	if err1 != nil || err2 != nil || d <= 0 || n <= 0 {
		return 0
	}
	return int(math.Round(n / d))
}

func (p Prober) Probe(ctx context.Context, path string) (domain.Media, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, err := Run(ctx, p.Binary, []string{"-v", "error", "-protocol_whitelist", "file,pipe", "-format_whitelist", Formats, "-show_format", "-show_streams", "-of", "json", path}, 1<<20)
	if err != nil {
		return domain.Media{}, err
	}
	var raw struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type        string `json:"codec_type"`
			Codec       string `json:"codec_name"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			Rate        string `json:"sample_rate"`
			FrameRate   string `json:"r_frame_rate"`
			Channels    int    `json:"channels"`
			Duration    string `json:"duration"`
			Disposition struct {
				Attached int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return domain.Media{}, err
	}
	duration, _ := strconv.ParseFloat(raw.Format.Duration, 64)
	m := domain.Media{}
	for _, s := range raw.Streams {
		if s.Type == "video" && s.Disposition.Attached == 0 && !m.Video {
			m.Video = true
			m.Width = s.Width
			m.Height = s.Height
			m.VideoCodec = s.Codec
			m.FPS = frameRate(s.FrameRate)
		}
		if s.Type == "audio" && !m.Audio {
			m.Audio = true
			m.SampleRate, _ = strconv.Atoi(s.Rate)
			m.Channels = s.Channels
			m.AudioCodec = s.Codec
		}
		if duration == 0 {
			d, _ := strconv.ParseFloat(s.Duration, 64)
			if d > duration {
				duration = d
			}
		}
	}
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 121 {
		return m, fmt.Errorf("media duration must be >0 and <=121 seconds")
	}
	if !m.Video && !m.Audio {
		return m, fmt.Errorf("no usable audio or video stream")
	}
	if m.Video && (m.Width < 1 || m.Height < 1 || m.Width > 3840 || m.Height > 2160) {
		return m, fmt.Errorf("video exceeds processing dimensions")
	}
	if m.Audio && (m.Channels < 1 || m.Channels > 8 || m.SampleRate < 8000 || m.SampleRate > 192000) {
		return m, fmt.Errorf("unsupported audio layout")
	}
	m.DurationMS = int64(math.Round(duration * 1000))
	return m, nil
}
