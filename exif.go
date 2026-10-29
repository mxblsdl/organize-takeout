package main

import (
	"encoding/binary"
	"strings"
	"time"
)

func isExifExtension(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func exifDate(data []byte) (time.Time, bool) {
	if len(data) >= 4 && data[0] == 0xff && data[1] == 0xd8 {
		for offset := 2; offset+4 <= len(data); {
			if data[offset] != 0xff {
				offset++
				continue
			}
			for offset < len(data) && data[offset] == 0xff {
				offset++
			}
			if offset >= len(data) {
				break
			}
			marker := data[offset]
			offset++
			if marker == 0xd9 || marker == 0xda {
				break
			}
			if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
				continue
			}
			if offset+2 > len(data) {
				break
			}
			length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
			if length < 2 || offset+length > len(data) {
				break
			}
			payload := data[offset+2 : offset+length]
			if marker == 0xe1 && len(payload) >= 6 && string(payload[:6]) == "Exif\x00\x00" {
				if date, ok := tiffExifDate(payload[6:]); ok {
					return date, true
				}
			}
			offset += length
		}
		return time.Time{}, false
	}
	return tiffExifDate(data)
}

func tiffExifDate(data []byte) (time.Time, bool) {
	if len(data) < 8 {
		return time.Time{}, false
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return time.Time{}, false
	}
	if order.Uint16(data[2:4]) != 42 {
		return time.Time{}, false
	}
	ifdOffset := uint64(order.Uint32(data[4:8]))
	root, ok := readTIFFDirectory(data, order, ifdOffset)
	if !ok {
		return time.Time{}, false
	}
	if exifOffset, exists := root.long[0x8769]; exists {
		if exif, valid := readTIFFDirectory(data, order, uint64(exifOffset)); valid {
			if date, parsed := parseExifTime(exif.text[0x9003]); parsed {
				return date, true
			}
			if date, parsed := parseExifTime(exif.text[0x9004]); parsed {
				return date, true
			}
		}
	}
	if date, ok := parseExifTime(root.text[0x0132]); ok {
		return date, true
	}
	return parseExifTime(root.text[0x9003])
}

type tiffDirectory struct {
	text map[uint16]string
	long map[uint16]uint32
}

func readTIFFDirectory(data []byte, order binary.ByteOrder, offset uint64) (tiffDirectory, bool) {
	if offset+2 > uint64(len(data)) {
		return tiffDirectory{}, false
	}
	start := int(offset)
	count := int(order.Uint16(data[start : start+2]))
	if count > (len(data)-start-2)/12 {
		return tiffDirectory{}, false
	}
	values := tiffDirectory{text: make(map[uint16]string), long: make(map[uint16]uint32)}
	for i := 0; i < count; i++ {
		entry := data[start+2+i*12 : start+2+(i+1)*12]
		tag := order.Uint16(entry[:2])
		typeID := order.Uint16(entry[2:4])
		valueCount := uint64(order.Uint32(entry[4:8]))
		if typeID == 4 && valueCount == 1 {
			values.long[tag] = order.Uint32(entry[8:12])
			continue
		}
		if typeID != 2 || valueCount == 0 {
			continue
		}
		var value []byte
		if valueCount <= 4 {
			value = entry[8 : 8+int(valueCount)]
		} else {
			valueOffset := uint64(order.Uint32(entry[8:12]))
			if valueOffset+valueCount > uint64(len(data)) {
				continue
			}
			value = data[int(valueOffset):int(valueOffset+valueCount)]
		}
		values.text[tag] = strings.TrimRight(string(value), "\x00")
	}
	return values, true
}

func parseExifTime(value string) (time.Time, bool) {
	date, err := time.ParseInLocation("2006:01:02 15:04:05", strings.TrimSpace(value), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return date, true
}
