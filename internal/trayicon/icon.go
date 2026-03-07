package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"sync"
)

var (
	iconOnce sync.Once
	iconData []byte
)

func ProxyICO() []byte {
	iconOnce.Do(func() {
		img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
		drawIcon(img)

		var pngBuf bytes.Buffer
		if err := png.Encode(&pngBuf, img); err != nil {
			return
		}
		pngData := pngBuf.Bytes()

		var ico bytes.Buffer
		_ = binary.Write(&ico, binary.LittleEndian, uint16(0))
		_ = binary.Write(&ico, binary.LittleEndian, uint16(1))
		_ = binary.Write(&ico, binary.LittleEndian, uint16(1))
		ico.WriteByte(16)
		ico.WriteByte(16)
		ico.WriteByte(0)
		ico.WriteByte(0)
		_ = binary.Write(&ico, binary.LittleEndian, uint16(1))
		_ = binary.Write(&ico, binary.LittleEndian, uint16(32))
		_ = binary.Write(&ico, binary.LittleEndian, uint32(len(pngData)))
		_ = binary.Write(&ico, binary.LittleEndian, uint32(22))
		_, _ = ico.Write(pngData)

		iconData = ico.Bytes()
	})
	if len(iconData) == 0 {
		return nil
	}
	out := make([]byte, len(iconData))
	copy(out, iconData)
	return out
}

func drawIcon(img *image.NRGBA) {
	bg := color.NRGBA{R: 10, G: 88, B: 92, A: 255}
	inner := color.NRGBA{R: 50, G: 175, B: 170, A: 255}
	fg := color.NRGBA{R: 245, G: 248, B: 250, A: 255}

	fillRoundedRect(img, image.Rect(1, 1, 15, 15), 4, bg)
	fillRoundedRect(img, image.Rect(3, 3, 13, 13), 3, inner)

	for y := 4; y <= 11; y++ {
		img.SetNRGBA(5, y, fg)
	}
	for x := 5; x <= 10; x++ {
		img.SetNRGBA(x, 4, fg)
		img.SetNRGBA(x, 7, fg)
	}
	for y := 4; y <= 7; y++ {
		img.SetNRGBA(10, y, fg)
	}
}

func fillRoundedRect(img *image.NRGBA, rect image.Rectangle, radius int, c color.NRGBA) {
	rr := radius * radius
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if insideRoundedRect(x, y, rect, radius, rr) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func insideRoundedRect(x, y int, rect image.Rectangle, radius, radiusSquared int) bool {
	if x >= rect.Min.X+radius && x < rect.Max.X-radius {
		return true
	}
	if y >= rect.Min.Y+radius && y < rect.Max.Y-radius {
		return true
	}

	cx := rect.Min.X + radius
	if x >= rect.Max.X-radius {
		cx = rect.Max.X - radius - 1
	}
	cy := rect.Min.Y + radius
	if y >= rect.Max.Y-radius {
		cy = rect.Max.Y - radius - 1
	}
	dx := x - cx
	dy := y - cy
	return dx*dx+dy*dy <= radiusSquared
}
