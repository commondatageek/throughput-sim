package linear

import "strings"

type TeamKey = string

// TeamKeyList is a flag.Value for a comma-separated list of Linear team keys
// (e.g. "ENG,DESIGN"). Keys are upper-cased and trimmed on Set.
type TeamKeyList []TeamKey

func (k *TeamKeyList) String() string { return strings.Join(*k, ",") }

func (k *TeamKeyList) Set(val string) error {
	*k = nil
	for _, part := range strings.Split(val, ",") {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part != "" {
			*k = append(*k, part)
		}
	}
	return nil
}
