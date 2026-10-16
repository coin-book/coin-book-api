package apierr

import "sort"

func sortedCodes() []Code {
	codes := Codes()
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}
