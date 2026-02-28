package main

import "fmt"

const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var b64decode [256]byte

func init() {
	for i := range b64decode {
		b64decode[i] = 0xFF
	}
	for i := 0; i < len(b64chars); i++ {
		b64decode[b64chars[i]] = byte(i)
	}
	b64decode['='] = 0
}

func main() {
	tests := []struct {
		input    string
		expected []byte
	}{
		{"AQID", []byte{1, 2, 3}},
		{"AQIDBA==", []byte{1, 2, 3, 4}},
		{"AQIDBAUGBwgJCgsMDQ4PEA==", []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}},
		{"SGVsbG8gV29ybGQ=", []byte("Hello World")},
	}

	for i, tt := range tests {
		decoded := decode([]byte(tt.input))
		if len(decoded) != len(tt.expected) {
			fmt.Printf("FAIL test %d: len=%d expected=%d\n", i, len(decoded), len(tt.expected))
			continue
		}
		match := true
		for j := range decoded {
			if decoded[j] != tt.expected[j] {
				fmt.Printf("FAIL test %d: byte %d got %d expected %d\n", i, j, decoded[j], tt.expected[j])
				match = false
				break
			}
		}
		if match {
			fmt.Printf("PASS test %d: %q -> %d bytes\n", i, tt.input, len(decoded))
		}
	}
}

func decode(src []byte) []byte {
	groups := len(src) / 4
	if groups == 0 {
		return nil
	}

	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if src[len(src)-2] == '=' {
		padCount++
	}

	buf := make([]byte, groups*3)

	go for i := range buf {
		group := byte(i / 3)
		pos := byte(i % 3)

		c0 := b64decode[src[group*4+0]]
		c1 := b64decode[src[group*4+1]]
		c2 := b64decode[src[group*4+2]]
		c3 := b64decode[src[group*4+3]]

		switch pos {
		case 0:
			buf[i] = (c0 << 2) | (c1 >> 4)
		case 1:
			buf[i] = (c1 << 4) | (c2 >> 2)
		case 2:
			buf[i] = (c2 << 6) | c3
		}
	}

	return buf[:groups*3-padCount]
}
