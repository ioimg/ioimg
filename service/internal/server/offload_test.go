package server

import (
	"errors"
	"testing"

	"github.com/ioimg/ioimg/service/internal/imaging"
	"github.com/ioimg/ioimg/service/pkg/ops"
)

type stubProcessor struct {
	data []byte
	err  error
}

func (s stubProcessor) Process([]byte, ops.Ops, imaging.Options) (imaging.Result, error) {
	return imaging.Result{Data: s.data}, s.err
}

// runPipeline offloads the transform to its own goroutine by default so libvips
// keeps its decode and encode threads off celeris' single pinned core. The
// offload must be transparent: same bytes, same error, both ways.
func TestRunPipelineIsTransparent(t *testing.T) {
	t.Setenv("IOIMG_OFFLOAD", "")
	if offloadDisabled() {
		t.Fatal("offload must default to on")
	}
	t.Setenv("IOIMG_OFFLOAD", "false")
	if !offloadDisabled() {
		t.Fatal("IOIMG_OFFLOAD=false must disable the offload")
	}

	res, err := runPipeline(stubProcessor{data: []byte("ok")}, nil, ops.Ops{}, imaging.Options{})
	if err != nil || string(res.Data) != "ok" {
		t.Fatalf("offloaded run = %q, %v", res.Data, err)
	}

	t.Setenv("IOIMG_OFFLOAD", "0")
	boom := errors.New("decode")
	res, err = runPipeline(stubProcessor{err: boom}, nil, ops.Ops{}, imaging.Options{})
	if !errors.Is(err, boom) {
		t.Fatalf("inline run error = %v, want %v", err, boom)
	}
	if len(res.Data) != 0 {
		t.Fatalf("inline run returned data on error: %q", res.Data)
	}
}
