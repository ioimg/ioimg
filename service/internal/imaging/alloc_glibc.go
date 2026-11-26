//go:build vips && linux

// The libvips path frees large per-request buffers (AVIF decode frames, RGBA
// copies) on a different thread than the one that allocated them. glibc's
// default per-thread arenas keep that freed memory instead of returning it, and
// RSS grows with every request: measured with a 2040x1356 AVIF source and a
// 12-request sequence, RSS went 224 -> 288 -> 324 MB. Capping the arenas at 2
// keeps the same sequence flat at 141 -> 119 -> 144 MB.
//
// MALLOC_ARENA_MAX has to be set before malloc initialises, so a constructor is
// the only place a binary can do this for itself. The container image sets it
// too; this covers everyone who runs the binary directly.
//
// Set MALLOC_ARENA_MAX yourself to override (the constructor then does nothing).
package imaging

/*
#include <malloc.h>
#include <stdlib.h>

__attribute__((constructor))
static void ioimg_limit_arenas(void) {
	if (getenv("MALLOC_ARENA_MAX") == NULL) {
		mallopt(M_ARENA_MAX, 2);
	}
}
*/
import "C"

// arenaCapSupported records that the constructor above is compiled in, so a
// startup log line can say the process is protected without reading /proc.
const arenaCapSupported = true

var _ = C.M_ARENA_MAX
