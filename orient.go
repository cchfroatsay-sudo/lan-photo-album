package main

import (
	"bytes"
	"image"
	"image/jpeg"
)

// applyOrientation returns the visually upright version of an image according
// to its EXIF orientation (1-8). Cameras store the sensor raster and rely on
// this tag, which Go's JPEG decoder does not apply.
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	dstWidth, dstHeight := width, height
	if orientation >= 5 {
		dstWidth, dstHeight = height, width
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstWidth, dstHeight))
	srcRGBA, _ := src.(*image.RGBA)
	for y := 0; y < dstHeight; y++ {
		for x := 0; x < dstWidth; x++ {
			var sx, sy int
			switch orientation {
			case 2:
				sx, sy = width-1-x, y
			case 3:
				sx, sy = width-1-x, height-1-y
			case 4:
				sx, sy = x, height-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, height-1-x
			case 7:
				sx, sy = width-1-y, height-1-x
			case 8:
				sx, sy = width-1-y, x
			}
			if srcRGBA != nil {
				source := srcRGBA.PixOffset(bounds.Min.X+sx, bounds.Min.Y+sy)
				target := dst.PixOffset(x, y)
				copy(dst.Pix[target:target+4], srcRGBA.Pix[source:source+4])
			} else {
				dst.Set(x, y, src.At(bounds.Min.X+sx, bounds.Min.Y+sy))
			}
		}
	}
	return dst
}

// orientJPEG rotates an already-encoded small image (for example an embedded
// EXIF thumbnail) when the source photo needs EXIF rotation.
func orientJPEG(data []byte, orientation int) []byte {
	if orientation < 2 || orientation > 8 {
		return data
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return data
	}
	oriented := applyOrientation(img, orientation)
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, oriented, &jpeg.Options{Quality: 85}); err != nil {
		return data
	}
	return buffer.Bytes()
}
