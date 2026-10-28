// Package imaging resizes screenshots so they satisfy a model's documented image
// limits. It decodes PNG or JPEG, area-averages to a smaller size, and encodes a
// baseline JPEG. It is used because clipped, scaled CDP captures return empty
// data on some Chromium builds, while a full viewport capture always works.
package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"math"
)

// Result describes the encoded output and the scale applied to the source.
type Result struct {
	Data   []byte
	Width  int
	Height int
	Scale  float64 // output pixels per source pixel (1 when unchanged)
	Format string
}

const defaultQuality = 85

// FitJPEG decodes data and scales it by scale (0 < scale <= 1), encoding the
// result as a baseline JPEG. A scale of 1 still re-encodes PNG input as JPEG;
// JPEG input at scale 1 is returned unchanged.
func FitJPEG(data []byte, scale float64, quality int) (Result, error) {
	if len(data) == 0 {
		return Result{}, errors.New("empty image")
	}
	if scale <= 0 || scale > 1 || math.IsNaN(scale) {
		return Result{}, errors.New("scale must be within (0, 1]")
	}
	if quality <= 0 || quality > 100 {
		quality = defaultQuality
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{}, errors.New("unsupported image data")
	}
	if scale == 1 && format == "jpeg" {
		return Result{Data: data, Width: config.Width, Height: config.Height, Scale: 1, Format: "jpeg"}, nil
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, errors.New("cannot decode image")
	}
	target := source
	if scale < 1 {
		target = downscale(source, scale)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, target, &jpeg.Options{Quality: quality}); err != nil {
		return Result{}, err
	}
	bounds := target.Bounds()
	return Result{Data: out.Bytes(), Width: bounds.Dx(), Height: bounds.Dy(), Scale: float64(bounds.Dx()) / float64(source.Bounds().Dx()), Format: "jpeg"}, nil
}

// downscale area-averages source into a smaller RGBA image. Each destination
// pixel covers a source box of about 1/scale pixels per side, so text and thin
// lines are averaged rather than dropped as nearest-neighbour sampling would.
func downscale(source image.Image, scale float64) *image.RGBA {
	bounds := source.Bounds()
	width := max(1, int(math.Round(float64(bounds.Dx())*scale)))
	height := max(1, int(math.Round(float64(bounds.Dy())*scale)))
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	at := pixelReader(source)
	for y := 0; y < height; y++ {
		sy0 := bounds.Min.Y + int(math.Floor(float64(y)*float64(bounds.Dy())/float64(height)))
		sy1 := bounds.Min.Y + int(math.Ceil(float64(y+1)*float64(bounds.Dy())/float64(height)))
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < width; x++ {
			sx0 := bounds.Min.X + int(math.Floor(float64(x)*float64(bounds.Dx())/float64(width)))
			sx1 := bounds.Min.X + int(math.Ceil(float64(x+1)*float64(bounds.Dx())/float64(width)))
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b, a, n uint64
			for sy := sy0; sy < sy1 && sy < bounds.Max.Y; sy++ {
				for sx := sx0; sx < sx1 && sx < bounds.Max.X; sx++ {
					pr, pg, pb, pa := at(sx, sy)
					r, g, b, a, n = r+uint64(pr), g+uint64(pg), b+uint64(pb), a+uint64(pa), n+1
				}
			}
			if n == 0 {
				continue
			}
			offset := out.PixOffset(x, y)
			out.Pix[offset], out.Pix[offset+1], out.Pix[offset+2], out.Pix[offset+3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
		}
	}
	return out
}

// pixelReader returns 8-bit RGBA without the per-pixel interface conversion for
// the common decoded types; other formats fall back to the generic path.
func pixelReader(source image.Image) func(x, y int) (uint8, uint8, uint8, uint8) {
	switch img := source.(type) {
	case *image.RGBA:
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			o := img.PixOffset(x, y)
			return img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]
		}
	case *image.NRGBA:
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			o := img.PixOffset(x, y)
			return img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]
		}
	case *image.YCbCr:
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			c := img.YCbCrAt(x, y)
			r, g, b := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
			return r, g, b, 255
		}
	}
	return func(x, y int) (uint8, uint8, uint8, uint8) {
		r, g, b, a := source.At(x, y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)
	}
}
