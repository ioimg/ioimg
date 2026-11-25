package ops

import (
	"sort"
	"strconv"
	"strings"
)

// Negotiate chooses the concrete output format for f_auto.
//
// Preference follows the client's Accept header with q-values. AVIF and WebP
// are only offered when the build can encode them; callers pass an availability
// set. When nothing matches, JPEG is returned (or PNG when the source has
// alpha and the caller asks to preserve it).
func Negotiate(accept string, available map[Format]bool, preserveAlpha bool) Format {
	fallback := FormatJPEG
	if preserveAlpha && available[FormatPNG] {
		fallback = FormatPNG
	}
	if strings.TrimSpace(accept) == "" || strings.Contains(accept, "*/*") {
		// Conservative default from the spec: WebP when supported.
		if available[FormatWebP] {
			return FormatWebP
		}
		return fallback
	}

	q := map[Format]float64{}
	for _, part := range strings.Split(accept, ",") {
		media, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		media = strings.ToLower(strings.TrimSpace(media))
		quality := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					quality = f
				}
			}
		}
		switch media {
		case "image/avif":
			q[FormatAVIF] = quality
		case "image/webp":
			q[FormatWebP] = quality
		case "image/jpeg", "image/jpg":
			q[FormatJPEG] = quality
		case "image/png":
			q[FormatPNG] = quality
		case "image/gif":
			q[FormatGIF] = quality
		case "image/*":
			for _, f := range []Format{FormatAVIF, FormatWebP, FormatPNG, FormatJPEG} {
				if _, exists := q[f]; !exists {
					q[f] = quality * 0.5
				}
			}
		}
	}

	type candidate struct {
		f Format
		q float64
	}
	var candidates []candidate
	for f, quality := range q {
		if !available[f] || quality <= 0 {
			continue
		}
		candidates = append(candidates, candidate{f, quality})
	}
	if len(candidates) == 0 {
		return fallback
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].q == candidates[j].q {
			return formatRank(candidates[i].f) < formatRank(candidates[j].f)
		}
		return candidates[i].q > candidates[j].q
	})
	return candidates[0].f
}

func formatRank(f Format) int {
	switch f {
	case FormatAVIF:
		return 0
	case FormatWebP:
		return 1
	case FormatJPEG:
		return 2
	case FormatPNG:
		return 3
	case FormatGIF:
		return 4
	}
	return 9
}
