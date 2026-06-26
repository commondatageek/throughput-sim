package util

// OrdinalSuffix returns the English ordinal suffix ("st", "nd", "rd", "th") for
// n, so it can be appended to form ordinals like "1st", "22nd" or "13th".
func OrdinalSuffix(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return "th"
	case n%10 == 1:
		return "st"
	case n%10 == 2:
		return "nd"
	case n%10 == 3:
		return "rd"
	default:
		return "th"
	}
}
