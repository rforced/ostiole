package timezone

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// kernelZone is the kernel's struct timezone. The second field is a
// daylight saving flag nothing reads.
type kernelZone struct {
	minutesWest int32
	dstTime     int32
}

// kernelOffset reads the offset the kernel keeps, in minutes west of UTC.
func kernelOffset() (kernelZone, error) {
	var tz kernelZone
	if _, _, errno := unix.Syscall(unix.SYS_GETTIMEOFDAY, 0, uintptr(unsafe.Pointer(&tz)), 0); errno != 0 {
		return tz, fmt.Errorf("read the kernel's UTC offset: %w", errno)
	}
	return tz, nil
}

// SetKernelOffset gives the kernel the router's offset from UTC. systemd
// boots it at UTC, and timedatectl moves it only when the zone changes,
// never for daylight saving.
func (System) SetKernelOffset(seconds int) error {
	have, err := kernelOffset()
	if err != nil {
		return err
	}
	// The kernel takes fifteen hours either way; zones stop at fourteen.
	west := -seconds / 60
	if west < -15*60 || west > 15*60 {
		return fmt.Errorf("%d seconds is no offset from UTC", seconds)
	}
	want := kernelZone{minutesWest: int32(west)}
	if have.minutesWest == want.minutesWest {
		return nil
	}
	// The first call since boot that names an offset also moves the clock
	// by it, for a hardware clock kept in local time. systemd makes that
	// call at boot; if nothing has, one naming the offset the kernel
	// already has uses it up and moves nothing.
	for _, tz := range []kernelZone{have, want} {
		if _, _, errno := unix.Syscall(unix.SYS_SETTIMEOFDAY, 0, uintptr(unsafe.Pointer(&tz)), 0); errno != 0 {
			return fmt.Errorf("set the kernel's UTC offset: %w", errno)
		}
	}
	return nil
}
