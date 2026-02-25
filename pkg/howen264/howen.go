package howen264

import "encoding/binary"

// BuildHowenControlEnvelope wraps a JSON string into Howen’s 8‑byte WS envelope (action=2).
func BuildHowenControlEnvelope(jsonBody []byte) []byte {
    buf := make([]byte, 8+len(jsonBody))
    buf[0] = 0x48 // 'H'
    buf[1] = 0x01 // version
    binary.LittleEndian.PutUint16(buf[2:], ActionJSON)
    binary.LittleEndian.PutUint32(buf[4:], uint32(len(jsonBody)))
    copy(buf[8:], jsonBody)
    return buf
}

// ParseHowenEnvelope parses the 8‑byte Howen WS header.
// Returns (action, payload, ok).
func ParseHowenEnvelope(msg []byte) (uint16, []byte, bool) {
    if len(msg) < 8 || msg[0] != 0x48 || msg[1] != 0x01 {
        return 0, nil, false
    }
    action := binary.LittleEndian.Uint16(msg[2:])
    // uint32 payloadLen := binary.LittleEndian.Uint32(msg[4:]) // server may not rely on this
    return action, msg[8:], true
}

// ParseHowenMedia parses the 12‑byte media frame header inside the payload (action=1000).
// Layout:
//   +0..1  frameType (LE16): 1=I,2=P,3=audio
//   +2..3  timeOffset (LE16) -- ignore
//   +4..7  frameLen (LE32)
//   +8..11 tsMs     (LE32)
//   +12..  frame bytes (length = frameLen)
func ParseHowenMedia(payload []byte) (ft FrameType, tsMs uint32, frame []byte, ok bool) {
    if len(payload) < 12 {
        return
    }
    ft = FrameType(binary.LittleEndian.Uint16(payload[0:2]))
    frameLen := binary.LittleEndian.Uint32(payload[4:8])
    tsMs = binary.LittleEndian.Uint32(payload[8:12])

    if int(12+frameLen) > len(payload) {
        return
    }
    frame = payload[12 : 12+frameLen]
    ok = true
    return
}