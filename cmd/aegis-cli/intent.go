package main

import (
	"regexp"
	"strconv"
)

var (
	scaleIntentZH = regexp.MustCompile(`(?i)([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*).*?(?:从|from)\s*(\d+)\s*(?:个)?.*?(?:扩到|扩容到|到|to)\s*(\d+)`)
	scaleIntentEN = regexp.MustCompile(`(?i)([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*).*?from\s*(\d+)\s*(?:replicas?)?\s*to\s*(\d+)`)
)

func parseScaleIntent(text string) (ns, name string, from, to int, ok bool) {
	for _, re := range []*regexp.Regexp{scaleIntentZH, scaleIntentEN} {
		m := re.FindStringSubmatch(text)
		if len(m) != 5 {
			continue
		}
		f, err1 := strconv.Atoi(m[3])
		t, err2 := strconv.Atoi(m[4])
		if err1 != nil || err2 != nil || f < 0 || t < 0 || f == t {
			continue
		}
		return m[1], m[2], f, t, true
	}
	return "", "", 0, 0, false
}
