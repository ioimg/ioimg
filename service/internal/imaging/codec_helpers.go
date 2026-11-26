package imaging

// isTrue reports whether an optional boolean argument is present and true.
func isTrue(b *bool) bool { return b != nil && *b }

// pngColors normalises pngo quantization_colors: 0 means the encoder default.
// Both backends use this so the accepted values and default agree.
func pngColors(n int) int {
	if n < 2 {
		return 256
	}
	if n > 256 {
		return 256
	}
	return n
}
