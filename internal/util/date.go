package util

import "time"

func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}
