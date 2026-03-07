package buildinfo

import (
	"fmt"
	"strings"
)

var (
	Version         = "dev"
	Commit          = "unknown"
	BuildDate       = "unknown"
	Channel         = "dev"
	UpdatePublicKey = ""
)

func Summary() string {
	return fmt.Sprintf(
		"version=%s commit=%s build_date=%s channel=%s",
		ValueOrUnknown(Version),
		ValueOrUnknown(Commit),
		ValueOrUnknown(BuildDate),
		ValueOrUnknown(Channel),
	)
}

func ValueOrUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}
