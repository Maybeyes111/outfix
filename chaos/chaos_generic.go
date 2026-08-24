//go:build !amd64

package chaos

import "math/bits"

const multiplier = 0x2545F4914F6CDD1D

var structTbl = []byte{0x7B, 0x7D, 0x5B, 0x5D, 0x22, 0x3A, 0x2C, 0x27}
var wordTbl = []byte{0x74, 0x68, 0x6E, 0x6B}

func Fill(buf []byte, seed uint64) {
	x := seed
	for i := range buf {
		x ^= x >> 12
		x ^= x << 25
		x ^= x >> 27
		x *= multiplier
		switch x & 15 {
		case 0:
			buf[i] = structTbl[(x>>32)&7]
		case 1:
			buf[i] = 0x3C
		case 2:
			buf[i] = 0x2F
		case 3:
			buf[i] = wordTbl[(x>>45)&3]
		case 4:
			buf[i] = 0x30 + byte((x>>50)&7)
		case 5:
			buf[i] = 0x0A
		case 6:
			buf[i] = 0xC0 | byte(x>>56)
		case 7:
			buf[i] = 0x22
		default:
			buf[i] = 0x61 + byte((x>>40)&15)
		}
	}
}
