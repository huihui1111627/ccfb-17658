package model

import "strconv"

// CmpVersion compares dotted firmware versions numerically: "22.0.6" > "18.4.0".
func CmpVersion(a, b string) int {
	pa, pb := splitVer(a), splitVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVer(v string) [3]int {
	var out [3]int
	field, cur := 0, 0
	digits := false
	for _, c := range v {
		if c == '.' {
			if field < 3 {
				out[field] = cur
			}
			field++
			cur, digits = 0, false
			continue
		}
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			digits = true
		}
	}
	if digits && field < 3 {
		out[field] = cur
	}
	return out
}

// Atoi is a tiny helper for audit messages.
func Atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
