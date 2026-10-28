package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func checkerboard(width, height, cell int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if (x/cell+y/cell)%2 == 0 {
				img.Set(x, y, color.RGBA{255, 255, 255, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 0, 255})
			}
		}
	}
	return img
}

func TestFitJPEGHalvesDeviceScreenshots(t *testing.T) {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, checkerboard(800, 600, 1)); err != nil {
		t.Fatal(err)
	}
	result, err := FitJPEG(buffer.Bytes(), 0.5, 85)
	if err != nil || result.Width != 400 || result.Height != 300 || result.Format != "jpeg" || result.Scale != 0.5 {
		t.Fatalf("bad fit: %+v %v", result, err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(result.Data))
	if err != nil {
		t.Fatal(err)
	}
	// A 1px checkerboard averaged 2x2 is mid-grey everywhere: averaging, not sampling.
	r, g, b, _ := decoded.At(100, 100).RGBA()
	for _, channel := range []uint32{r >> 8, g >> 8, b >> 8} {
		if channel < 96 || channel > 160 {
			t.Fatalf("downscale sampled instead of averaging: %d %d %d", r>>8, g>>8, b>>8)
		}
	}
}

func TestFitJPEGPassesThroughAndRejectsBadInput(t *testing.T) {
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, checkerboard(300, 300, 10), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	same, err := FitJPEG(buffer.Bytes(), 1, 80)
	if err != nil || !bytes.Equal(same.Data, buffer.Bytes()) || same.Width != 300 {
		t.Fatalf("scale 1 JPEG must pass through: %v", err)
	}
	if _, err := FitJPEG([]byte("nope"), 0.5, 80); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := FitJPEG(buffer.Bytes(), 1.5, 80); err == nil {
		t.Fatal("upscaling accepted")
	}
}
