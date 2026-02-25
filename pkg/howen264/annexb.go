package howen264

// splitAnnexB splits an Annex‑B bytestream into NAL units (including the 1‑byte NAL header).
// It recognizes both 0x000001 and 0x00000001 start codes.
func splitAnnexB(b []byte) [][]byte {
    var out [][]byte
    i := 0
    for {
        start, payload := findStartCode(b, i)
        if start < 0 {
            break
        }
        start2, payload2 := findStartCode(b, payload)
        if start2 < 0 {
            if payload < len(b) {
                out = append(out, b[payload:])
            }
            break
        }
        out = append(out, b[payload:start2])
        i = start2
    }
    return out
}

func findStartCode(b []byte, from int) (start, payload int) {
    n := len(b)
    for i := from; i+3 < n; i++ {
        // 0x00000001
        if b[i] == 0x00 && b[i+1] == 0x00 && b[i+2] == 0x00 && b[i+3] == 0x01 {
            return i, i + 4
        }
        // 0x000001
        if b[i] == 0x00 && b[i+1] == 0x00 && b[i+2] == 0x01 {
            return i, i + 3
        }
    }
    return -1, -1
}

func nalType(nal []byte) byte {
    if len(nal) == 0 {
        return 0
    }
    return nal[0] & 0x1F
}

func isIDR(nal []byte) bool { return nalType(nal) == nalIDR }
func isSPS(nal []byte) bool { return nalType(nal) == nalSPS }
func isPPS(nal []byte) bool { return nalType(nal) == nalPPS }
