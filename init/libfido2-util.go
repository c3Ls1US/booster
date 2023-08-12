package main

/*
#cgo CFLAGS: -I/usr/include/
#cgo LDFLAGS: -lfido2
#include <stdio.h>
#include <stdlib.h>
#include <fido.h>
*/
import "C"
import (
	"fmt"
	"sync"
)

// FIDO2 Device
type Device struct {
	path string
	dev  *C.fido_dev_t
	sync.Mutex
}

func NewFido2Device(path string) *Device {
	return &Device{
		path: fmt.Sprintf("%s", path),
	}
}

func (d *Device) openFido2Device() (*C.fido_dev_t, error) {
	dev := C.fido_dev_new()
	if cErr := C.fido_dev_open(dev, C.CString(d.path)); cErr != C.FIDO_OK {
		// TODO: needs better handling of various error types thrown from libfido2
		return nil, fmt.Errorf("failed to open hidraw device")
	}
	d.dev = dev
	return dev, nil
}

func (d *Device) closeFido2Device(dev *C.fido_dev_t) {
	d.Lock()
	d.dev = nil
	d.Unlock()
	if cErr := C.fido_dev_close(dev); cErr != C.FIDO_OK {
		// TODO: needs better handling of various error types thrown from libfido2
		info("failed to close hidraw device")
	}
	C.fido_dev_free(&dev)
}

// attempts to open and close the device for validation
func (d *Device) IsFido2() (bool, error) {
	dev, err := d.openFido2Device()
	if err != nil {
		return false, err
	}
	defer d.closeFido2Device(dev)
	isFido2 := bool(C.fido_dev_is_fido2(dev))
	return isFido2, nil
}
