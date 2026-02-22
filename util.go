package ipod

import "unicode/utf8"

func BoolToByte(b bool) byte {
	if b {
		return 0x01
	}
	return 0x00
}

func ByteToBool(b byte) bool {
	return b == 0x01
}

// StringToBytes converts a string to a null-terminated byte slice
func StringToBytes(s string) []byte {
	return append([]byte(s), 0x00)
}

// ClampString truncates s to at most maxBytes bytes while keeping valid UTF-8
// rune boundaries. Most car head units silently discard or show garbage for
// strings longer than 255 bytes; real iPods never send more.
func ClampString(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk rune boundaries so we don't split a multi-byte character.
	b := s[:maxBytes]
	for !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

// TruncateRunes truncates s to at most n Unicode code points.
// If truncation occurs, the last rune is replaced with "…".
func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
