//go:build amd64

package chaos

func fill(ptr *byte, n int, seed uint64)

func Fill(buf []byte, seed uint64) {
	if len(buf) > 0 {
		fill(&buf[0], len(buf), seed)
	}
}
