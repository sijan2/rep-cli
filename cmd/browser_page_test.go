package cmd

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadShotURLsKeepsOrderAndSkipsComments(t *testing.T) {
	file := filepath.Join(t.TempDir(), "urls.txt")
	if err := os.WriteFile(file, []byte("# targets\nhttps://b.example\n\n  https://c.example  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	urls, err := readShotURLs([]string{"https://a.example"}, file, strings.NewReader("https://d.example\n#x\n"))
	if err != nil || strings.Join(urls, ",") != "https://a.example,https://b.example,https://c.example,https://d.example" {
		t.Fatalf("urls = %v, %v", urls, err)
	}
	if _, err := readShotURLs(nil, "", strings.NewReader("\n# only comments\n")); err == nil {
		t.Fatal("an empty URL list was accepted")
	}
	if _, err := readShotURLs(nil, filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("a missing URL file was accepted")
	}
}

func TestShotFileNamesAreSafeAndOrdered(t *testing.T) {
	for raw, want := range map[string]string{
		"https://example.com/a/b?q=1#x":       "0001-example.com_a_b_q_1_x.png",
		"http://[::1]:8080/../../etc":         "0001-1_8080_.._.._etc.png",
		"https://":                            "0001-page.png",
		"https://" + strings.Repeat("a", 200): "0001-" + strings.Repeat("a", 80) + ".png",
	} {
		if got := shotFileName(0, raw, ".png"); got != want {
			t.Fatalf("%q -> %q, want %q", raw, got, want)
		}
		if strings.ContainsAny(shotFileName(0, raw, ".png"), `/\`) {
			t.Fatalf("path separator in file name for %q", raw)
		}
	}
	if shotFileName(41, "https://x.test", ".webp") != "0042-x.test.webp" {
		t.Fatal("index is not one-based and zero-padded")
	}
}

func TestImageDimensionsForEveryScreenshotFormat(t *testing.T) {
	var encoded bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 7, 3))
	canvas.Set(0, 0, color.White)
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	if width, height := imageDimensions("png", encoded.Bytes()); width != 7 || height != 3 {
		t.Fatalf("png = %dx%d", width, height)
	}
	vp8x := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X"), make([]byte, 18)...)
	vp8x[24], vp8x[25], vp8x[26] = 0xff, 0x04, 0x00 // width-1 = 1279
	vp8x[27], vp8x[28], vp8x[29] = 0xf4, 0x02, 0x00 // height-1 = 756
	if width, height := imageDimensions("webp", vp8x); width != 1280 || height != 757 {
		t.Fatalf("webp VP8X = %dx%d", width, height)
	}
	vp8l := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x00\x00\x00\x00\x2f"), make([]byte, 9)...)
	bits := uint32(99) | uint32(49)<<14 // 100x50
	vp8l[21], vp8l[22], vp8l[23], vp8l[24] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
	if width, height := imageDimensions("webp", vp8l); width != 100 || height != 50 {
		t.Fatalf("webp VP8L = %dx%d", width, height)
	}
	if width, height := imageDimensions("webp", []byte("not an image at all, long enough to parse")); width != 0 || height != 0 {
		t.Fatal("garbage produced dimensions")
	}
}

func TestShotsReportEscapesPageText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.html")
	summary := shotsSummary{Total: 1, Captured: 1, Parallel: 1, Results: []shotResult{{URL: `https://x.test/"><script>alert(1)</script>`, Title: "<img src=x onerror=alert(1)>", reportFile: "0001-x.png", HTTPStatus: 200}}}
	if err := writeShotsReport(path, summary); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "<script>alert") || strings.Contains(string(data), "<img src=x") {
		t.Fatalf("page-controlled text was not escaped:\n%s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("report is not private: %v %v", info, err)
	}
}

func TestSaveTaskArtifactIsPrivateAndUnique(t *testing.T) {
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	t.Setenv("TMPDIR", t.TempDir())
	first, err := saveTaskArtifact("screenshots", "shot-*.png", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := saveTaskArtifact("screenshots", "shot-*.png", []byte("b"))
	if err != nil || first == second {
		t.Fatalf("artifacts collided: %q %q %v", first, second, err)
	}
	for _, path := range []string{first, second} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("artifact %s is not private: %v", path, err)
		}
	}
	if info, err := os.Stat(filepath.Dir(first)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("artifact directory is not private: %v", err)
	}
}
