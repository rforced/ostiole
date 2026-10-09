package logfile

// ReadStats is what reading a log back found beside its entries.
type ReadStats struct {
	// Skipped counts lines that did not decode.
	Skipped int
	// Unknown names the files holding a member in a format this build
	// cannot read, which was passed over.
	Unknown []string
	// Broken names the files cut short, read up to the cut.
	Broken []string
}
