package expand

import (
	"testing"
	"time"
)

// These expectations were checked against GNU Bash 5.3 printf %(fmt)T.
// They cover independent POSIX rules as well as IANA names so a fixture-only
// timezone mapping cannot satisfy the test.
func TestPrintfExplicitTZ(t *testing.T) {
	const summer = int64(1275250155)
	const winter = int64(1262304000)
	tests := []struct {
		name string
		tz   string
		unix int64
		want string
	}{
		{"US summer", "EST5EDT,M3.2.0/2,M11.1.0/2", summer, "2010-05-30 16:09:15 -0400 EDT"},
		{"US winter", "EST5EDT,M3.2.0/2,M11.1.0/2", winter, "2009-12-31 19:00:00 -0500 EST"},
		{"Europe summer", "CET-1CEST,M3.5.0/2,M10.5.0/3", summer, "2010-05-30 22:09:15 +0200 CEST"},
		{"Europe winter", "CET-1CEST,M3.5.0/2,M10.5.0/3", winter, "2010-01-01 01:00:00 +0100 CET"},
		{"half hour", "IST-5:30", summer, "2010-05-31 01:39:15 +0530 IST"},
		{"west fixed", "PST8", summer, "2010-05-30 12:09:15 -0800 PST"},
		{"quoted name", "<+03>-3", summer, "2010-05-30 23:09:15 +0300 +03"},
		{"IANA", "America/New_York", summer, "2010-05-30 16:09:15 -0400 EDT"},
		{"colon IANA", ":Europe/Berlin", summer, "2010-05-30 22:09:15 +0200 CEST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := time.Unix(tt.unix, 0).In(printfLocationFromTZ(tt.tz)).Format("2006-01-02 15:04:05 -0700 MST")
			if got != tt.want {
				t.Fatalf("TZ=%q: got %q, want %q", tt.tz, got, tt.want)
			}
		})
	}
}
