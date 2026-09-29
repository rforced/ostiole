package timezone

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// The kernel already at the offset asked for is left as it is, which is
// also the one call a test may make unprivileged.
func TestSetKernelOffsetLeavesTheOffsetItHas(t *testing.T) {
	t.Parallel()
	have, err := kernelOffset()
	if err != nil {
		t.Fatal(err)
	}
	if err := (System{}).SetKernelOffset(-int(have.minutesWest) * 60); err != nil {
		t.Errorf("SetKernelOffset at the kernel's own offset: %v", err)
	}
}

// A different offset is a call only root may make, and its refusal is
// reported. Never run as root: the test would move the host's offset.
func TestSetKernelOffsetReportsARefusal(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("as root this would change the kernel's UTC offset")
	}
	have, err := kernelOffset()
	if err != nil {
		t.Fatal(err)
	}
	other := -int(have.minutesWest)*60 + 3600
	if err := (System{}).SetKernelOffset(other); !errors.Is(err, unix.EPERM) {
		t.Errorf("SetKernelOffset unprivileged = %v, want EPERM", err)
	}
	if now, err := kernelOffset(); err != nil || now != have {
		t.Errorf("the kernel's offset moved: %+v, %v", now, err)
	}
}
