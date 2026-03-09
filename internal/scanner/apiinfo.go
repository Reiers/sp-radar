package scanner

import "strings"

// APIInfo parses FULLNODE_API_INFO in standard Lotus format: [token:]<url>
type APIInfo struct {
	Token string
	Addr  string
}

func ParseAPIInfo(s string) APIInfo {
	var ai APIInfo
	if !strings.HasPrefix(s, "http") && !strings.HasPrefix(s, "ws") {
		if sp := strings.SplitN(s, ":", 2); len(sp) == 2 {
			ai.Token = sp[0]
			ai.Addr = sp[1]
			return ai
		}
	}
	ai.Addr = s
	return ai
}
