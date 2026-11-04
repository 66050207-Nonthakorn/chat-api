// Package imageproc validates and re-encodes static chat images.
package imageproc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	stdDraw "image/draw"
	"image/jpeg"
	"image/png"
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	MaxInputPixels = int64(25_000_000)
	MaxSide        = 16_384
	MaxOutputSide  = 2_560
	MaxOutputBytes = 5 * 1024 * 1024
)

type Result struct {
	Bytes       []byte
	ContentType string
	Width       int
	Height      int
}

func Normalize(ctx context.Context, source []byte) (Result, string, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, "", ctx.Err()
	}
	format, err := imageFormat(source)
	if err != nil {
		return Result{}, "", err
	}
	orientation := 1
	switch format {
	case "image/jpeg":
		orientation, err = jpegOrientation(source)
	case "image/png":
		orientation, err = pngOrientation(source)
	case "image/webp":
		orientation, err = webpOrientation(source)
	}
	if err != nil {
		return Result{}, "", err
	}
	if err := staticImage(source, format); err != nil {
		return Result{}, "", err
	}
	var config image.Config
	if format == "image/webp" {
		config, err = webp.DecodeConfig(bytes.NewReader(source))
	} else {
		config, _, err = image.DecodeConfig(bytes.NewReader(source))
	}
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > MaxSide || config.Height > MaxSide || int64(config.Width)*int64(config.Height) > MaxInputPixels {
		return Result{}, "", errors.New("image dimensions exceed limits or are invalid")
	}
	var decoded image.Image
	if format == "image/webp" {
		decoded, err = webp.Decode(bytes.NewReader(source))
	} else {
		decoded, _, err = image.Decode(bytes.NewReader(source))
	}
	if err != nil {
		return Result{}, "", errors.New("invalid image data")
	}
	view := orientedImage{source: decoded, orientation: orientation}
	w, h := view.Bounds().Dx(), view.Bounds().Dy()
	scale := math.Min(1, float64(MaxOutputSide)/float64(max(w, h)))
	outW, outH := max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	result := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	if outW == w && outH == h {
		stdDraw.Draw(result, result.Bounds(), view, view.Bounds().Min, stdDraw.Src)
	} else {
		xdraw.CatmullRom.Scale(result, result.Bounds(), view, view.Bounds(), stdDraw.Src, nil)
	}
	contentType := format
	if format == "image/webp" {
		if opaque(result) {
			contentType = "image/jpeg"
		} else {
			contentType = "image/png"
		}
	}
	var output bytes.Buffer
	switch contentType {
	case "image/jpeg":
		err = jpeg.Encode(&output, result, &jpeg.Options{Quality: 85})
	case "image/png":
		err = png.Encode(&output, result)
	default:
		err = errors.New("unsupported image encoding")
	}
	if err != nil || output.Len() == 0 || output.Len() > MaxOutputBytes {
		return Result{}, "", errors.New("processed image is invalid or exceeds the 5 MiB limit")
	}
	return Result{Bytes: output.Bytes(), ContentType: contentType, Width: outW, Height: outH}, format, nil
}

func imageFormat(data []byte) (string, error) {
	switch {
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg", nil
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}):
		return "image/png", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", nil
	default:
		return "", errors.New("only JPEG, PNG, and WebP images are supported")
	}
}

func staticImage(data []byte, format string) error {
	if format == "image/png" {
		for offset := uint64(8); offset+12 <= uint64(len(data)); {
			length := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
			end := offset + 12 + length
			if end > uint64(len(data)) {
				return errors.New("invalid PNG chunk")
			}
			if string(data[offset+4:offset+8]) == "acTL" {
				return errors.New("animated images are not supported")
			}
			if string(data[offset+4:offset+8]) == "IEND" {
				return nil
			}
			offset = end
		}
		return errors.New("invalid PNG container")
	}
	if format == "image/webp" {
		end := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
		if end > uint64(len(data)) || end < 12 {
			return errors.New("invalid WebP container")
		}
		for offset := uint64(12); offset+8 <= end; {
			length := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
			chunkEnd := offset + 8 + length
			if chunkEnd > end {
				return errors.New("invalid WebP chunk")
			}
			chunk := string(data[offset : offset+4])
			if chunk == "ANIM" || chunk == "ANMF" || (chunk == "VP8X" && length > 0 && data[offset+8]&0x02 != 0) {
				return errors.New("animated images are not supported")
			}
			offset = chunkEnd + length%2
		}
	}
	return nil
}

func jpegOrientation(data []byte) (int, error) {
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			return 1, errors.New("invalid JPEG marker")
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) || data[offset] == 0xda || data[offset] == 0xd9 {
			return 1, nil
		}
		marker := data[offset]
		offset++
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if offset+2 > len(data) {
			return 1, errors.New("invalid JPEG segment")
		}
		length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if length < 2 || offset+length > len(data) {
			return 1, errors.New("invalid JPEG segment")
		}
		payload := data[offset+2 : offset+length]
		if marker == 0xe1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			return tiffOrientation(payload[6:])
		}
		offset += length
	}
	return 1, nil
}

func pngOrientation(data []byte) (int, error) {
	for offset := uint64(8); offset+12 <= uint64(len(data)); {
		length := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		end := offset + 12 + length
		if end > uint64(len(data)) {
			return 1, errors.New("invalid PNG chunk")
		}
		if string(data[offset+4:offset+8]) == "eXIf" {
			return tiffOrientation(data[offset+8 : offset+8+length])
		}
		if string(data[offset+4:offset+8]) == "IEND" {
			return 1, nil
		}
		offset = end
	}
	return 1, errors.New("invalid PNG container")
}

func webpOrientation(data []byte) (int, error) {
	end := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
	if end > uint64(len(data)) || end < 12 {
		return 1, errors.New("invalid WebP container")
	}
	for offset := uint64(12); offset+8 <= end; {
		length := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		chunkEnd := offset + 8 + length
		if chunkEnd > end {
			return 1, errors.New("invalid WebP chunk")
		}
		if string(data[offset:offset+4]) == "EXIF" {
			payload := data[offset+8 : chunkEnd]
			payload = bytes.TrimPrefix(payload, []byte("Exif\x00\x00"))
			return tiffOrientation(payload)
		}
		offset = chunkEnd + length%2
	}
	return 1, nil
}

func tiffOrientation(data []byte) (int, error) {
	if len(data) < 8 {
		return 0, errors.New("invalid EXIF data")
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, errors.New("invalid EXIF byte order")
	}
	if order.Uint16(data[2:4]) != 42 {
		return 0, errors.New("invalid EXIF header")
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset+2 > uint64(len(data)) {
		return 0, errors.New("invalid EXIF directory")
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	if offset+2+count*12 > uint64(len(data)) {
		return 0, errors.New("invalid EXIF entries")
	}
	for index := uint64(0); index < count; index++ {
		entry := data[offset+2+index*12 : offset+2+(index+1)*12]
		if order.Uint16(entry[:2]) != 0x0112 {
			continue
		}
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 0, errors.New("invalid EXIF orientation")
		}
		orientation := int(order.Uint16(entry[8:10]))
		if orientation < 1 || orientation > 8 {
			return 0, errors.New("invalid EXIF orientation")
		}
		return orientation, nil
	}
	return 1, nil
}

type orientedImage struct {
	source      image.Image
	orientation int
}

func (i orientedImage) Bounds() image.Rectangle {
	bounds := i.source.Bounds()
	if i.orientation >= 5 && i.orientation <= 8 {
		return image.Rect(0, 0, bounds.Dy(), bounds.Dx())
	}
	return image.Rect(0, 0, bounds.Dx(), bounds.Dy())
}

func (i orientedImage) ColorModel() color.Model { return i.source.ColorModel() }

func (i orientedImage) At(x, y int) color.Color {
	bounds := i.source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	sx, sy := x, y
	switch i.orientation {
	case 2:
		sx = w - 1 - x
	case 3:
		sx, sy = w-1-x, h-1-y
	case 4:
		sy = h - 1 - y
	case 5:
		sx, sy = y, x
	case 6:
		sx, sy = y, h-1-x
	case 7:
		sx, sy = w-1-y, h-1-x
	case 8:
		sx, sy = w-1-y, x
	}
	return i.source.At(bounds.Min.X+sx, bounds.Min.Y+sy)
}

func opaque(img image.Image) bool {
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			_, _, _, alpha := img.At(x, y).RGBA()
			if alpha != 0xffff {
				return false
			}
		}
	}
	return true
}
