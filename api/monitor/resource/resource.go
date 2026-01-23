package resource

type Type string
type Part string

const (
	// The dmesg resource.
	// It has no parts.
	ResourceTypeDmesg Type = "dmesg"
	// The file resource.
	// It has one part that is the path of the file.
	ResourceTypeFile Type = "file"
	// The journal resource.
	// It has one part that is the name of the systemd unit.
	ResourceTypeJournal Type = "journal"
)
