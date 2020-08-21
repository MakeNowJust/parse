package js

func AsIdentifierName(b []byte) bool {
	if len(b) == 0 || !identifierStartTable[b[0]] {
		return false
	}

	i := 1
	for i < len(b) {
		if identifierTable[b[i]] {
			i++
		} else {
			return false
		}
	}
	return true
}

func AsDecimalLiteral(b []byte) bool {
	if len(b) == 0 || (b[0] < '0' || '9' < b[0]) && b[0] != '.' {
		return false
	}
	i := 1
	if b[0] != '.' {
		for i < len(b) && '0' <= b[i] && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && b[i] == '.' {
		i++
		for i < len(b) && '0' <= b[i] && b[i] <= '9' {
			i++
		}
	}
	return i == len(b)
}
