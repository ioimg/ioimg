package server

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ioimg/ioimg/service/internal/config"
)

func bombPNGHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8
	ihdr[9] = 2
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(ihdr)))
	buf.Write(length[:])
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(chunk))
	buf.Write(crc[:])
	return buf.Bytes()
}

func passthroughBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRawStreamsSourceUnchanged(t *testing.T) {
	h := newHarness(t, nil)
	src := jpegBytes(t)
	source := h.origin.URL + "/photo.jpg"

	resp := h.get(t, "/raw_1/"+source, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("raw content type = %q", ct)
	}
	if body := passthroughBody(t, resp); !bytes.Equal(body, src) {
		t.Fatal("raw response differs from the source bytes")
	}

	// A header-only 100k x 100k PNG would fail the ordinary dimension guard;
	// raw must still stream it byte-for-byte and sniff the media type.
	bomb := bombPNGHeader(100_000, 100_000)
	resp = h.get(t, "/raw_1/"+h.origin.URL+"/bomb.png", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw bomb status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("raw bomb content type = %q", ct)
	}
	if body := passthroughBody(t, resp); !bytes.Equal(body, bomb) {
		t.Fatal("raw bomb response differs from the source bytes")
	}

	// Non-raw still enforces the guard.
	resp = h.get(t, "/f_png/"+h.origin.URL+"/bomb.png", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("guarded request status = %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSkipProcessingPassThrough(t *testing.T) {
	h := newHarness(t, nil)
	src := jpegBytes(t)
	source := h.origin.URL + "/photo.jpg"

	for _, raw := range []string{
		"/f_jpeg,skp_jpg/" + source,
		"/w_40,f_jpeg,skp_jpg:png/" + source,
		"/w_40,f_jpeg,skp_jpg,png/" + source,
	} {
		resp := h.get(t, raw, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", raw, resp.StatusCode)
		}
		if body := passthroughBody(t, resp); !bytes.Equal(body, src) {
			t.Fatalf("%s did not return the source bytes unchanged", raw)
		}
	}

	// The output format differs from the source, so skp_ must not fire.
	resp := h.get(t, "/f_png,skp_jpg/"+source, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mismatched skp status = %d", resp.StatusCode)
	}
	if body := passthroughBody(t, resp); bytes.Equal(body, src) {
		t.Fatal("skp_ fired even though the output format differs from the source")
	}

	// Same-format skp_ bypasses the dimension guard.
	bomb := bombPNGHeader(100_000, 100_000)
	resp = h.get(t, "/f_png,skp_png/"+h.origin.URL+"/bomb.png", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("skp bomb status = %d", resp.StatusCode)
	}
	if body := passthroughBody(t, resp); !bytes.Equal(body, bomb) {
		t.Fatal("skp bomb response differs from the source bytes")
	}
}

func TestPassthroughCacheKeyAndSignatureDiffer(t *testing.T) {
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"

	etags := map[string]string{}
	for _, raw := range []string{
		"/f_jpeg/" + source,
		"/f_jpeg,skp_jpg/" + source,
		"/f_jpeg,raw_1/" + source,
	} {
		resp := h.get(t, raw, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", raw, resp.StatusCode)
		}
		etags[raw] = resp.Header.Get("ETag")
		resp.Body.Close()
	}
	if etags["/f_jpeg/"+source] == etags["/f_jpeg,skp_jpg/"+source] ||
		etags["/f_jpeg/"+source] == etags["/f_jpeg,raw_1/"+source] ||
		etags["/f_jpeg,skp_jpg/"+source] == etags["/f_jpeg,raw_1/"+source] {
		t.Fatalf("passthrough cache keys collide: %+v", etags)
	}

	path := h.signed(t, "f_jpeg,raw_1", source, time.Now().Add(time.Hour))
	resp := h.get(t, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed raw status = %d", resp.StatusCode)
	}
	if body := passthroughBody(t, resp); !bytes.Equal(body, jpegBytes(t)) {
		t.Fatal("signed raw response differs from the source bytes")
	}
	bad := strings.Replace(path, "raw_1", "raw_0", 1)
	code, errCode := decodeErr(t, h.get(t, bad, nil))
	if code != http.StatusUnauthorized || errCode != "invalid_signature" {
		t.Fatalf("tampered raw signature = %d %s, want 401 invalid_signature", code, errCode)
	}
}

// imgproxy treats only raw_ as unchecked: skip_processing skips the transform but
// keeps the source-file-size guard, because the bytes are still buffered here.
func TestRawBypassesSourceSizeGuardButSkipDoesNot(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SourceMaxBytes = 64 })
	src := jpegBytes(t)
	if len(src) <= 64 {
		t.Fatalf("test payload is only %d bytes", len(src))
	}
	source := h.origin.URL + "/photo.jpg"

	resp := h.get(t, "/f_jpeg/"+source, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("guarded request status = %d, want 413", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.get(t, "/raw_1/"+source, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw status = %d, want 200", resp.StatusCode)
	}
	if body := passthroughBody(t, resp); !bytes.Equal(body, src) {
		t.Fatal("raw did not return the oversized source unchanged")
	}

	resp = h.get(t, "/f_jpeg,skp_jpg/"+source, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("skp status = %d, want 413 (skip must keep the source-size guard)", resp.StatusCode)
	}
	resp.Body.Close()
}
