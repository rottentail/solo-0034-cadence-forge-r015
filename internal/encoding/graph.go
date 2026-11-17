package encoding

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"cadenceforge/internal/privacy"
	"fmt"
	"strings"
)

func seconds(ms int64) string { return fmt.Sprintf("%.3f", float64(ms)/1000) }

// Arguments builds the real FFmpeg filter graph. When the render freezes
// privacy masks, each video segment's drawbox chain is inserted after scaling
// and frame-rate normalization (so boxes are crisp output-pixel rectangles)
// and before the pixel format conversion.
func Arguments(render domain.Render, paths map[string]string, output string) ([]string, error) {
	r := render.Recipe
	args := []string{"-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-filter_complex_threads", "1"}
	filters := []string{}
	joins := []string{}
	var coverage *privacy.CoveragePlan
	if len(render.Masks) > 0 {
		planner, err := privacy.NewPlanner(render.Masks, render.Sources, r.Width, r.Height, r.FPS)
		if err != nil {
			return nil, err
		}
		cov, err := privacy.Build(planner, render.Plan)
		if err != nil {
			return nil, err
		}
		coverage = &cov
	}
	for i, p := range render.Plan.Placements {
		path, ok := paths[p.SourceID]
		if !ok {
			return nil, fmt.Errorf("source object missing")
		}
		args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats, "-i", path)
		audio := fmt.Sprintf("[%d:a:0]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=%d,aformat=sample_fmts=fltp:channel_layouts=stereo,volume=%.3fdB[a%d]", i, seconds(p.SourceInMS), seconds(p.SourceOutMS), r.SampleRate, p.GainDB, i)
		if !render.Sources[p.SourceID].Media.Audio {
			audio = fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d]", r.SampleRate, seconds(p.OutputOutMS-p.OutputInMS), i)
		}
		filters = append(filters, audio)
		if r.Format == "mp4" {
			chain := fmt.Sprintf("[%d:v:0]trim=start=%s:end=%s,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%d", i, seconds(p.SourceInMS), seconds(p.SourceOutMS), r.Width, r.Height, r.Width, r.Height, r.FPS)
			if coverage != nil {
				if boxFilters := privacy.DrawboxFilters(*coverage, i); len(boxFilters) > 0 {
					chain += "," + strings.Join(boxFilters, ",")
				}
			}
			chain += fmt.Sprintf(",format=yuv420p[v%d]", i)
			filters = append(filters, chain)
			joins = append(joins, fmt.Sprintf("[v%d][a%d]", i, i))
		} else {
			joins = append(joins, fmt.Sprintf("[a%d]", i))
		}
	}
	if len(joins) == 0 {
		return nil, fmt.Errorf("empty render")
	}
	if r.Format == "mp4" {
		filters = append(filters, strings.Join(joins, "")+fmt.Sprintf("concat=n=%d:v=1:a=1[vout][acat]", len(joins)))
	} else {
		filters = append(filters, strings.Join(joins, "")+fmt.Sprintf("concat=n=%d:v=0:a=1[acat]", len(joins)))
	}
	if r.Normalize {
		filters = append(filters, fmt.Sprintf("[acat]loudnorm=I=%.2f:TP=-1.5:LRA=11,aresample=%d[aout]", r.TargetLUFS, r.SampleRate))
	} else {
		filters = append(filters, "[acat]anull[aout]")
	}
	args = append(args, "-filter_complex", strings.Join(filters, ";"))
	if r.Format == "mp4" {
		args = append(args, "-map", "[vout]", "-map", "[aout]", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-threads", "1", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart")
	} else {
		args = append(args, "-map", "[aout]", "-c:a", "pcm_s16le")
	}
	return append(args, "-ar", fmt.Sprint(r.SampleRate), "-t", seconds(render.Plan.DurationMS), "-map_metadata", "-1", output), nil
}
