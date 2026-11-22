package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

var errNoEXIFThumb = errors.New("no embedded exif thumbnail")

const exifHeadBytes = 128 * 1024

// exifInfo returns the JPEG thumbnail embedded in the EXIF block of a photo
// along with its EXIF orientation. iPhones and most phones store a thumbnail,
// so the gallery can render a tile without decoding the full image.
func exifInfo(full string) ([]byte, int, error) {
	file, err := os.Open(full)
	if err != nil {
		return nil, 1, err
	}
	defer file.Close()

	head := make([]byte, exifHeadBytes)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, 1, err
	}
	data := head[:n]
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, 1, errNoEXIFThumb
	}

	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xFF {
			pos++
			continue
		}
		marker := data[pos+1]
		if marker == 0xFF {
			pos++
			continue
		}
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			pos += 2
			continue
		}
		if marker == 0xD9 || marker == 0xDA {
			break
		}
		if pos+4 > len(data) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		if segLen < 2 || pos+2+segLen > len(data) {
			break
		}
		if marker == 0xE1 && segLen >= 8 && string(data[pos+4:pos+10]) == "Exif\x00\x00" {
			return parseEXIF(data[pos+10 : pos+2+segLen])
		}
		pos += 2 + segLen
	}
	return nil, 1, errNoEXIFThumb
}

func parseEXIF(tiff []byte) ([]byte, int, error) {
	if len(tiff) < 8 {
		return nil, 1, errNoEXIFThumb
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return nil, 1, errNoEXIFThumb
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return nil, 1, errNoEXIFThumb
	}
	ifd0 := int(order.Uint32(tiff[4:8]))
	first, next, ok := readIFD(tiff, ifd0, order)
	orientation := 1
	if ok {
		if value, found := first[0x0112]; found && value >= 1 && value <= 8 {
			orientation = int(value)
		}
	}
	if !ok || next == 0 {
		return nil, orientation, errNoEXIFThumb
	}
	entries, _, ok := readIFD(tiff, next, order)
	if !ok {
		return nil, orientation, errNoEXIFThumb
	}
	offset, okOffset := entries[0x0201]
	length, okLength := entries[0x0202]
	if !okOffset || !okLength {
		return nil, orientation, errNoEXIFThumb
	}
	start := int(offset)
	end := start + int(length)
	if start < 0 || end <= start || end > len(tiff) {
		return nil, orientation, errNoEXIFThumb
	}
	thumb := tiff[start:end]
	if len(thumb) < 4 || thumb[0] != 0xFF || thumb[1] != 0xD8 {
		return nil, orientation, errNoEXIFThumb
	}
	return thumb, orientation, nil
}

func readIFD(tiff []byte, off int, order binary.ByteOrder) (map[uint16]uint32, int, bool) {
	if off < 0 || off+2 > len(tiff) {
		return nil, 0, false
	}
	count := int(order.Uint16(tiff[off : off+2]))
	if count <= 0 || count > 512 || off+2+count*12+4 > len(tiff) {
		return nil, 0, false
	}
	entries := make(map[uint16]uint32, count)
	for i := 0; i < count; i++ {
		base := off + 2 + i*12
		tag := order.Uint16(tiff[base : base+2])
		typ := order.Uint16(tiff[base+2 : base+4])
		valueCount := order.Uint32(tiff[base+4 : base+8])
		if valueCount != 1 {
			continue
		}
		switch typ {
		case 3:
			entries[tag] = uint32(order.Uint16(tiff[base+8 : base+10]))
		case 4:
			entries[tag] = order.Uint32(tiff[base+8 : base+12])
		}
	}
	next := int(order.Uint32(tiff[off+2+count*12 : off+6+count*12]))
	return entries, next, true
}

