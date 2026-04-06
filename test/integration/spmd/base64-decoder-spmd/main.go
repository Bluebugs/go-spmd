package main

import "fmt"

var decodeTable [256]byte

func init() {
	for i := range decodeTable {
		decodeTable[i] = 0xff
	}
	for i := byte('A'); i <= byte('Z'); i++ {
		decodeTable[i] = i - 'A'
	}
	for i := byte('a'); i <= byte('z'); i++ {
		decodeTable[i] = i - 'a' + 26
	}
	for i := byte('0'); i <= byte('9'); i++ {
		decodeTable[i] = i - '0' + 52
	}
	decodeTable['+'] = 62
	decodeTable['/'] = 63
	decodeTable['='] = 0
}

func main() {
	testCases := []string{
		"SGVsbG8gV29ybGQ=",
		"Zm9vYmFy",
		"YWJjZA==",
	}

	for _, input := range testCases {
		decoded, ok := Decode([]byte(input))
		if ok {
			fmt.Printf("'%s' -> '%s'\n", input, string(decoded))
		} else {
			fmt.Printf("'%s' -> ERROR\n", input)
		}
	}
}

func Decode(src []byte) ([]byte, bool) {
	if len(src) == 0 {
		return nil, true
	}
	if len(src)%4 != 0 {
		return nil, false
	}

	groups := len(src) / 4
	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if src[len(src)-2] == '=' {
		padCount++
	}
	if padCount > 2 {
		return nil, false
	}

	hotGroups := groups
	if padCount != 0 {
		hotGroups--
	}

	dst := make([]byte, groups*3+12+3*4)
	hotSrc := src[:hotGroups*4]
	if !validateHot(hotSrc) {
		return nil, false
	}
	decodeHot(dst, hotSrc)

	if hotGroups != groups {
		tail, ok := decodeQuartetScalar(src[hotGroups*4:])
		if !ok {
			return nil, false
		}
		copy(dst[hotGroups*3:], tail)
	}

	return dst[:groups*3-padCount], true
}

func validateHot(src []byte) bool {
	for i := range src {
		if decodeTable[src[i]] == 0xff {
			return false
		}
	}
	return true
}

func decodeHot(dst, src []byte) {
	groups := len(src) / 4
	
	go for g := range groups {
		c0 := decodeTable[src[g*4+0]]
		c1 := decodeTable[src[g*4+1]]
		c2 := decodeTable[src[g*4+2]]
		c3 := decodeTable[src[g*4+3]]
		dst[g*3+0] = (c0 << 2) | (c1 >> 4)
		dst[g*3+1] = (c1 << 4) | (c2 >> 2)
		dst[g*3+2] = (c2 << 6) | c3
	}
	return
}

func decodeQuartetScalar(raw []byte) ([]byte, bool) {
	if len(raw) != 4 {
		return nil, false
	}

	pads := [4]bool{
		raw[0] == '=',
		raw[1] == '=',
		raw[2] == '=',
		raw[3] == '=',
	}
	if pads[0] || pads[1] {
		return nil, false
	}
	if pads[2] && !pads[3] {
		return nil, false
	}

	sextets := [4]byte{}
	for i := range sextets {
		ch := raw[i]
		if ch == '=' {
			ch = 'A'
		}
		sextet := decodeTable[ch]
		if sextet == 0xff && raw[i] != '=' {
			return nil, false
		}
		sextets[i] = sextet
	}

	byte0 := (sextets[0] << 2) | (sextets[1] >> 4)
	byte1 := (sextets[1] << 4) | (sextets[2] >> 2)
	byte2 := (sextets[2] << 6) | sextets[3]

	switch {
	case pads[2]:
		return []byte{byte0}, true
	case pads[3]:
		return []byte{byte0, byte1}, true
	default:
		return []byte{byte0, byte1, byte2}, true
	}
}
