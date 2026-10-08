//go:build windows

package main

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed assets/antgpu-connect.ico
var embeddedAppIconICO []byte

//go:embed assets/antgpu-connect-tray.ico
var embeddedTrayIconICO []byte

var procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")

func loadEmbeddedTrayIcon(desired int32) (uintptr, error) {
	img, err := pickICOImage(embeddedTrayIconICO, desired)
	if err != nil {
		return 0, err
	}
	if len(img) == 0 {
		return 0, fmt.Errorf("ico image payload is empty")
	}

	r, _, callErr := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&img[0])),
		uintptr(uint32(len(img))),
		1,
		0x00030000,
		uintptr(desired),
		uintptr(desired),
		0,
	)
	if r == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx: %v", callErr)
	}
	return r, nil
}

func pickICOImage(data []byte, desired int32) ([]byte, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("invalid ico header")
	}
	if binary.LittleEndian.Uint16(data[0:2]) != 0 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		return nil, fmt.Errorf("unsupported ico format")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count <= 0 || len(data) < 6+16*count {
		return nil, fmt.Errorf("invalid ico directory")
	}

	bestIdx := -1
	bestDist := int(^uint(0) >> 1)
	bestSize := 0
	want := int(desired)
	if want <= 0 {
		want = 32
	}

	for i := 0; i < count; i++ {
		off := 6 + i*16
		w := int(data[off])
		h := int(data[off+1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := w
		if h < size {
			size = h
		}
		dist := size - want
		if dist < 0 {
			dist = -dist
		}
		if bestIdx == -1 || dist < bestDist || (dist == bestDist && size > bestSize) {
			bestIdx = i
			bestDist = dist
			bestSize = size
		}
	}
	if bestIdx < 0 {
		return nil, fmt.Errorf("no ico image found")
	}

	off := 6 + bestIdx*16
	imgSize := int(binary.LittleEndian.Uint32(data[off+8 : off+12]))
	imgOffset := int(binary.LittleEndian.Uint32(data[off+12 : off+16]))
	if imgSize <= 0 || imgOffset < 0 || imgOffset+imgSize > len(data) {
		return nil, fmt.Errorf("invalid ico payload bounds")
	}
	return data[imgOffset : imgOffset+imgSize], nil
}

func destroyIconHandle(icon uintptr) {
	if icon == 0 {
		return
	}
	if h := windows.Handle(icon); h != 0 {
		_, _, _ = procDestroyIcon.Call(icon)
	}
}
