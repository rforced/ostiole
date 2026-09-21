package smart

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Drive is everything one drive says about itself.
type Drive struct {
	Device
	Model      string `json:"model"`
	Family     string `json:"family,omitempty"`
	Serial     string `json:"serial"`
	Firmware   string `json:"firmware"`
	Capacity   uint64 `json:"capacity"` // bytes
	Kind       string `json:"kind"`     // ssd, hdd, nvme, unknown
	RPM        int    `json:"rpm,omitempty"`
	FormFactor string `json:"formFactor,omitempty"`
	Interface  string `json:"interface,omitempty"` // "SATA 3.2 at 6.0 Gb/s", "NVMe 1.4"
	InDatabase bool   `json:"inDatabase"`
	// Health is passed, failed, or unknown when SMART is absent or off.
	Health  string `json:"health"`
	SMARTOn bool   `json:"smartOn"`
	// Partial says a SMART command failed, so some sections are missing.
	Partial      bool    `json:"partial,omitempty"`
	Temperature  *int    `json:"temperature,omitempty"`    // °C
	TempMax      *int    `json:"temperatureMax,omitempty"` // lifetime, when the drive keeps it
	PowerOnHours *int    `json:"powerOnHours,omitempty"`
	PowerCycles  *int    `json:"powerCycles,omitempty"`
	Wear         *int    `json:"wear,omitempty"`  // percent used
	Spare        *int    `json:"spare,omitempty"` // percent available
	Written      *uint64 `json:"written,omitempty"`
	Read         *uint64 `json:"read,omitempty"`

	SelfTest   SelfTest     `json:"selfTest"`
	Attributes []Attribute  `json:"attributes,omitempty"` // ATA
	NVMe       *NVMeHealth  `json:"nvme,omitempty"`
	TestLog    []TestEntry  `json:"testLog"`
	Errors     []ErrorEntry `json:"errors"`
	ErrorCount int          `json:"errorCount"`
	ReadAt     time.Time    `json:"readAt"`
}

// SelfTest is what the drive will run on demand, and what it is running.
type SelfTest struct {
	Supported  bool   `json:"supported"`
	Conveyance bool   `json:"conveyance"`
	Running    bool   `json:"running"`
	Remaining  int    `json:"remaining,omitempty"` // percent, while running
	Status     string `json:"status"`              // smartctl's sentence, lowercase
	Passed     *bool  `json:"passed,omitempty"`    // of the last test
	// Minutes a test takes, as the drive states them; zero when it does
	// not say.
	ShortMinutes      int `json:"shortMinutes,omitempty"`
	ExtendedMinutes   int `json:"extendedMinutes,omitempty"`
	ConveyanceMinutes int `json:"conveyanceMinutes,omitempty"`
}

// Attribute is one row of an ATA drive's SMART table.
type Attribute struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Value     int    `json:"value"`
	Worst     int    `json:"worst"`
	Threshold int    `json:"threshold"`
	Prefail   bool   `json:"prefail"`
	Raw       uint64 `json:"raw"`
	RawString string `json:"rawString"`
	// WhenFailed is empty, "now" or "past".
	WhenFailed string `json:"whenFailed,omitempty"`
	// Critical marks the attributes whose raw count above zero means the
	// drive is losing sectors.
	Critical bool `json:"critical,omitempty"`
}

// NVMeHealth is the health information log an NVMe drive keeps instead of
// an attribute table.
type NVMeHealth struct {
	CriticalWarning int    `json:"criticalWarning"`
	AvailableSpare  int    `json:"availableSpare"`
	SpareThreshold  int    `json:"spareThreshold"`
	PercentageUsed  int    `json:"percentageUsed"`
	UnsafeShutdowns uint64 `json:"unsafeShutdowns"`
	MediaErrors     uint64 `json:"mediaErrors"`
	ErrorLogEntries uint64 `json:"errorLogEntries"`
	Sensors         []int  `json:"sensors,omitempty"`
}

// TestEntry is one line of the self-test log.
type TestEntry struct {
	Type   string  `json:"type"`
	Status string  `json:"status"`
	Passed bool    `json:"passed"`
	Hours  int     `json:"hours"`
	LBA    *uint64 `json:"lba,omitempty"`
}

// ErrorEntry is one line of the error log.
type ErrorEntry struct {
	Number      int    `json:"number"`
	Hours       int    `json:"hours"`
	Description string `json:"description"`
}

// criticalIDs are the attributes whose raw count above zero means the
// drive is losing sectors: reallocated, reported uncorrectable, command
// timeout, pending, and offline uncorrectable.
var criticalIDs = map[int]bool{5: true, 187: true, 188: true, 197: true, 198: true}

// dataUnit is what an NVMe drive counts reads and writes in.
const dataUnit = 512_000

// document is the part of smartctl's JSON this package reads. Everything
// is a pointer or a zero value that means "the drive did not say".
type document struct {
	Smartctl struct {
		Version    []int `json:"version"`
		ExitStatus int   `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Devices []struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"devices"`

	ModelName       string `json:"model_name"`
	ModelFamily     string `json:"model_family"`
	SerialNumber    string `json:"serial_number"`
	FirmwareVersion string `json:"firmware_version"`
	UserCapacity    struct {
		Bytes uint64 `json:"bytes"`
	} `json:"user_capacity"`
	LogicalBlockSize uint64 `json:"logical_block_size"`
	RotationRate     *int   `json:"rotation_rate"`
	FormFactor       struct {
		Name string `json:"name"`
	} `json:"form_factor"`
	InDatabase  bool `json:"in_smartctl_database"`
	SATAVersion struct {
		String string `json:"string"`
	} `json:"sata_version"`
	InterfaceSpeed struct {
		Current struct {
			String string `json:"string"`
		} `json:"current"`
	} `json:"interface_speed"`
	SMARTSupport struct {
		Available bool `json:"available"`
		Enabled   bool `json:"enabled"`
	} `json:"smart_support"`
	SMARTStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *int `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount *int     `json:"power_cycle_count"`
	EnduranceUsed   *percent `json:"endurance_used"`
	SpareAvailable  *percent `json:"spare_available"`

	ATASmartData struct {
		SelfTest struct {
			Status struct {
				Value     int    `json:"value"`
				String    string `json:"string"`
				Remaining *int   `json:"remaining_percent"`
				Passed    *bool  `json:"passed"`
			} `json:"status"`
			PollingMinutes struct {
				Short      int `json:"short"`
				Extended   int `json:"extended"`
				Conveyance int `json:"conveyance"`
			} `json:"polling_minutes"`
		} `json:"self_test"`
		Capabilities struct {
			SelfTestsSupported bool `json:"self_tests_supported"`
			ConveyanceSupport  bool `json:"conveyance_self_test_supported"`
		} `json:"capabilities"`
	} `json:"ata_smart_data"`
	ATASCTStatus struct {
		Temperature struct {
			LifetimeMin *int `json:"lifetime_min"`
			LifetimeMax *int `json:"lifetime_max"`
		} `json:"temperature"`
	} `json:"ata_sct_status"`
	ATASmartAttributes struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			Worst      int    `json:"worst"`
			Thresh     int    `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Flags      struct {
				Prefailure bool `json:"prefailure"`
			} `json:"flags"`
			Raw struct {
				Value  uint64 `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	// A drive keeps each log twice: the 28-bit one a plain read returns,
	// and the general-purpose one -x prefers. Which of the two smartctl
	// answered with is the drive's business, so both are read.
	ATASelfTestLog struct {
		Standard ataTestLog `json:"standard"`
		Extended ataTestLog `json:"extended"`
	} `json:"ata_smart_self_test_log"`
	ATAErrorLog struct {
		Summary  ataErrorLog `json:"summary"`
		Extended ataErrorLog `json:"extended"`
	} `json:"ata_smart_error_log"`
	ATADeviceStatistics struct {
		Pages []struct {
			Number int    `json:"number"`
			Name   string `json:"name"`
			Table  []struct {
				Name  string `json:"name"`
				Value uint64 `json:"value"`
			} `json:"table"`
		} `json:"pages"`
	} `json:"ata_device_statistics"`

	NVMeVersion struct {
		String string `json:"string"`
	} `json:"nvme_version"`
	NVMeTotalCapacity uint64 `json:"nvme_total_capacity"`
	NVMeHealthLog     *struct {
		CriticalWarning  int    `json:"critical_warning"`
		Temperature      *int   `json:"temperature"`
		AvailableSpare   int    `json:"available_spare"`
		SpareThreshold   int    `json:"available_spare_threshold"`
		PercentageUsed   int    `json:"percentage_used"`
		DataUnitsRead    uint64 `json:"data_units_read"`
		DataUnitsWritten uint64 `json:"data_units_written"`
		PowerCycles      *int   `json:"power_cycles"`
		PowerOnHours     *int   `json:"power_on_hours"`
		UnsafeShutdowns  uint64 `json:"unsafe_shutdowns"`
		MediaErrors      uint64 `json:"media_errors"`
		ErrorLogEntries  uint64 `json:"num_err_log_entries"`
		Sensors          []int  `json:"temperature_sensors"`
	} `json:"nvme_smart_health_information_log"`
	NVMeSelfTestLog *struct {
		CurrentOperation struct {
			Value  int    `json:"value"`
			String string `json:"string"`
		} `json:"current_self_test_operation"`
		CompletionPercent *int `json:"current_self_test_completion_percent"`
		Table             []struct {
			Code struct {
				String string `json:"string"`
			} `json:"self_test_code"`
			Result struct {
				Value  int    `json:"value"`
				String string `json:"string"`
			} `json:"self_test_result"`
			PowerOnHours int `json:"power_on_hours"`
		} `json:"table"`
	} `json:"nvme_self_test_log"`
	NVMeErrorLog *struct {
		Read  int `json:"read"`
		Table []struct {
			ErrorCount  int `json:"error_count"`
			StatusField struct {
				String string `json:"string"`
			} `json:"status_field"`
		} `json:"table"`
	} `json:"nvme_error_information_log"`
}

type ataTestLog struct {
	Count int `json:"count"`
	Table []struct {
		Type struct {
			String string `json:"string"`
		} `json:"type"`
		Status struct {
			String string `json:"string"`
			Passed bool   `json:"passed"`
		} `json:"status"`
		LifetimeHours int     `json:"lifetime_hours"`
		LBA           *uint64 `json:"lba"`
	} `json:"table"`
}

type ataErrorLog struct {
	Count int `json:"count"`
	Table []struct {
		ErrorNumber   int    `json:"error_number"`
		LifetimeHours int    `json:"lifetime_hours"`
		Description   string `json:"error_description"`
	} `json:"table"`
}

// percent is how smartctl reports wear and spare capacity; the key it
// uses has changed, so both spellings are read.
type percent struct {
	Current        *int `json:"current"`
	CurrentPercent *int `json:"current_percent"`
}

func (p *percent) value() *int {
	switch {
	case p == nil:
		return nil
	case p.CurrentPercent != nil:
		return p.CurrentPercent
	default:
		return p.Current
	}
}

func (d *document) version() string {
	parts := make([]string, 0, len(d.Smartctl.Version))
	for _, n := range d.Smartctl.Version {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ".")
}

// messages joins what smartctl had to say when something went wrong.
func (d *document) messages() string {
	var out []string
	for _, m := range d.Smartctl.Messages {
		if s := strings.TrimSpace(m.String); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
}

// failure is the error for the two exit bits that mean the document is
// not worth reading; everything else the fields already carry.
func (d *document) failure() error {
	if d.Smartctl.ExitStatus&(exitCommandLine|exitOpenFailed) == 0 {
		return nil
	}
	if msg := d.messages(); msg != "" {
		return errors.New(msg)
	}
	return errors.New("smartctl exited " + strconv.Itoa(d.Smartctl.ExitStatus))
}

// drive maps a document onto what the page shows.
func (d *document) drive(dev Device) *Drive {
	nvme := strings.EqualFold(dev.Protocol, "NVMe") || d.NVMeHealthLog != nil
	out := &Drive{
		Device:     dev,
		Model:      d.ModelName,
		Family:     d.ModelFamily,
		Serial:     d.SerialNumber,
		Firmware:   d.FirmwareVersion,
		Capacity:   d.UserCapacity.Bytes,
		FormFactor: d.FormFactor.Name,
		InDatabase: d.InDatabase,
		Health:     "unknown",
		SMARTOn:    d.SMARTSupport.Enabled,
		Partial:    d.Smartctl.ExitStatus&exitCommandFailed != 0,
		TestLog:    []TestEntry{},
		Errors:     []ErrorEntry{},
		ReadAt:     time.Now(),
	}
	if out.Capacity == 0 {
		out.Capacity = d.NVMeTotalCapacity
	}
	if d.SMARTStatus != nil {
		out.Health = "failed"
		if d.SMARTStatus.Passed {
			out.Health = "passed"
		}
	}
	switch {
	case nvme:
		out.Kind = "nvme"
	case d.RotationRate == nil:
		out.Kind = "unknown"
	case *d.RotationRate > 0:
		out.Kind, out.RPM = "hdd", *d.RotationRate
	default:
		out.Kind = "ssd"
	}
	out.Interface = d.iface(nvme)
	if d.Temperature != nil {
		out.Temperature = d.Temperature.Current
	}
	out.TempMax = d.ATASCTStatus.Temperature.LifetimeMax
	if d.PowerOnTime != nil {
		out.PowerOnHours = d.PowerOnTime.Hours
	}
	out.PowerCycles = d.PowerCycleCount
	out.Wear = d.EnduranceUsed.value()
	out.Spare = d.SpareAvailable.value()
	d.transfers(out)
	d.selfTest(out, nvme)
	if nvme {
		d.nvmeHealth(out)
		d.nvmeLogs(out)
	} else {
		d.attributes(out)
		d.ataLogs(out)
	}
	return out
}

// iface is how the drive is attached, in the words the drive uses.
func (d *document) iface(nvme bool) string {
	if nvme {
		if d.NVMeVersion.String == "" {
			return "NVMe"
		}
		return "NVMe " + d.NVMeVersion.String
	}
	switch {
	case d.SATAVersion.String != "" && d.InterfaceSpeed.Current.String != "":
		return d.SATAVersion.String + " at " + d.InterfaceSpeed.Current.String
	default:
		return d.SATAVersion.String
	}
}

// transfers is how much has crossed the interface in the drive's life.
// ATA keeps it in sectors on a statistics page that only -x reads; NVMe
// counts in units of 512,000 bytes.
func (d *document) transfers(out *Drive) {
	if d.NVMeHealthLog != nil {
		written := d.NVMeHealthLog.DataUnitsWritten * dataUnit
		read := d.NVMeHealthLog.DataUnitsRead * dataUnit
		out.Written, out.Read = &written, &read
		return
	}
	block := d.LogicalBlockSize
	if block == 0 {
		return
	}
	for _, page := range d.ATADeviceStatistics.Pages {
		for _, row := range page.Table {
			switch row.Name {
			case "Logical Sectors Written":
				n := row.Value * block
				out.Written = &n
			case "Logical Sectors Read":
				n := row.Value * block
				out.Read = &n
			}
		}
	}
}

func (d *document) selfTest(out *Drive, nvme bool) {
	if nvme {
		log := d.NVMeSelfTestLog
		if log == nil {
			return
		}
		out.SelfTest.Supported = true
		out.SelfTest.Status = strings.ToLower(log.CurrentOperation.String)
		if log.CurrentOperation.Value != 0 {
			out.SelfTest.Running = true
			if log.CompletionPercent != nil {
				out.SelfTest.Remaining = 100 - *log.CompletionPercent
			}
		}
		return
	}
	st := d.ATASmartData.SelfTest
	out.SelfTest.Supported = d.ATASmartData.Capabilities.SelfTestsSupported
	out.SelfTest.Conveyance = d.ATASmartData.Capabilities.ConveyanceSupport
	out.SelfTest.Status = strings.ToLower(st.Status.String)
	out.SelfTest.Passed = st.Status.Passed
	if st.Status.Remaining != nil {
		out.SelfTest.Running = true
		out.SelfTest.Remaining = *st.Status.Remaining
	}
	out.SelfTest.ShortMinutes = st.PollingMinutes.Short
	out.SelfTest.ExtendedMinutes = st.PollingMinutes.Extended
	out.SelfTest.ConveyanceMinutes = st.PollingMinutes.Conveyance
}

func (d *document) attributes(out *Drive) {
	for _, a := range d.ATASmartAttributes.Table {
		out.Attributes = append(out.Attributes, Attribute{
			ID:         a.ID,
			Name:       a.Name,
			Value:      a.Value,
			Worst:      a.Worst,
			Threshold:  a.Thresh,
			Prefail:    a.Flags.Prefailure,
			Raw:        a.Raw.Value,
			RawString:  a.Raw.String,
			WhenFailed: strings.ToLower(a.WhenFailed),
			Critical:   criticalIDs[a.ID],
		})
	}
}

func (d *document) ataLogs(out *Drive) {
	tests := d.ATASelfTestLog.Standard
	if len(tests.Table) == 0 && tests.Count == 0 {
		tests = d.ATASelfTestLog.Extended
	}
	for _, e := range tests.Table {
		out.TestLog = append(out.TestLog, TestEntry{
			Type:   e.Type.String,
			Status: e.Status.String,
			Passed: e.Status.Passed,
			Hours:  e.LifetimeHours,
			LBA:    e.LBA,
		})
	}
	errs := d.ATAErrorLog.Summary
	if len(errs.Table) == 0 && errs.Count == 0 {
		errs = d.ATAErrorLog.Extended
	}
	out.ErrorCount = errs.Count
	for _, e := range errs.Table {
		out.Errors = append(out.Errors, ErrorEntry{
			Number:      e.ErrorNumber,
			Hours:       e.LifetimeHours,
			Description: e.Description,
		})
	}
}

func (d *document) nvmeHealth(out *Drive) {
	log := d.NVMeHealthLog
	if log == nil {
		return
	}
	out.NVMe = &NVMeHealth{
		CriticalWarning: log.CriticalWarning,
		AvailableSpare:  log.AvailableSpare,
		SpareThreshold:  log.SpareThreshold,
		PercentageUsed:  log.PercentageUsed,
		UnsafeShutdowns: log.UnsafeShutdowns,
		MediaErrors:     log.MediaErrors,
		ErrorLogEntries: log.ErrorLogEntries,
		Sensors:         log.Sensors,
	}
	if out.Temperature == nil {
		out.Temperature = log.Temperature
	}
	if out.PowerOnHours == nil {
		out.PowerOnHours = log.PowerOnHours
	}
	if out.PowerCycles == nil {
		out.PowerCycles = log.PowerCycles
	}
	if out.Wear == nil {
		used := log.PercentageUsed
		out.Wear = &used
	}
	if out.Spare == nil {
		spare := log.AvailableSpare
		out.Spare = &spare
	}
}

func (d *document) nvmeLogs(out *Drive) {
	if log := d.NVMeSelfTestLog; log != nil {
		for _, e := range log.Table {
			out.TestLog = append(out.TestLog, TestEntry{
				Type:   e.Code.String,
				Status: e.Result.String,
				Passed: e.Result.Value == 0,
				Hours:  e.PowerOnHours,
			})
		}
	}
	log := d.NVMeErrorLog
	if log == nil {
		return
	}
	out.ErrorCount = log.Read
	for _, e := range log.Table {
		out.Errors = append(out.Errors, ErrorEntry{
			Number:      e.ErrorCount,
			Description: e.StatusField.String,
		})
	}
}
