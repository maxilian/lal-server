package howen264

// buildAvcC builds AVCDecoderConfigurationRecord from SPS and PPS (each includes its NAL header byte).
// We assume 4-byte length size for NAL units (lengthSizeMinusOne = 3).
func buildAvcC(sps, pps []byte) []byte {
    if len(sps) < 4 || len(pps) == 0 {
        return nil
    }
    profile := sps[1]
    compat := sps[2]
    level := sps[3]

    // configurationVersion
    var avcC []byte
    avcC = append(avcC, 0x01)
    avcC = append(avcC, profile)
    avcC = append(avcC, compat)
    avcC = append(avcC, level)
    // lengthSizeMinusOne (4 bytes nalu length -> 3)
    avcC = append(avcC, 0xFF)
    // numOfSPS (lower 5 bits)
    avcC = append(avcC, 0xE1) // 11100001 -> 1 SPS
    // SPS length + data
    avcC = append(avcC, byte(len(sps)>>8), byte(len(sps)))
    avcC = append(avcC, sps...)
    // numOfPPS
    avcC = append(avcC, 0x01)
    avcC = append(avcC, byte(len(pps)>>8), byte(len(pps)))
    avcC = append(avcC, pps...)
    return avcC
}

// makeFlvVideoTag creates a single FLV Video tag (TagType=9) with AVC payload,
// and appends the 4-byte PreviousTagSize. It does NOT prepend the FLV file header.
func makeFlvVideoTag(tsMs uint32, isKey bool, avcPacketType byte, cts int32, avcPayload []byte) []byte {
    // dataSize = 1(video hdr) + 1(AVCPacketType) + 3(CTS) + payload
    dataSize := 5 + len(avcPayload)

    tag := make([]byte, 11+dataSize+4)
    off := 0

    // TagType: 9 (video)
    tag[off] = 0x09
    off++

    // DataSize (24-bit)
    writeU24(tag[off:], uint32(dataSize))
    off += 3

    // Timestamp (lower 24) + Extended
    writeU24(tag[off:], tsMs&0xFFFFFF)
    off += 3
    tag[off] = byte((tsMs >> 24) & 0xFF)
    off++

    // StreamID (always 0)
    tag[off], tag[off+1], tag[off+2] = 0, 0, 0
    off += 3

    // Video header: 4 bits frame type | 4 bits codec id (7 = AVC)
    // FrameType: 1=keyframe, 2=inter frame
    if isKey {
        tag[off] = (1 << 4) | 7
    } else {
        tag[off] = (2 << 4) | 7
    }
    off++

    // AVCPacketType: 0 = sequence header, 1 = NALU
    tag[off] = avcPacketType
    off++

    // CompositionTime (CTS), signed 24-bit (we pass 0 for now)
    writeU24(tag[off:], uint32(cts))
    off += 3

    // AVC payload
    copy(tag[off:], avcPayload)
    off += len(avcPayload)

    // PreviousTagSize = 11 + dataSize
    prev := 11 + dataSize
    writeU32(tag[off:], uint32(prev))
    return tag
}

func writeU24(b []byte, v uint32) {
    b[0] = byte((v >> 16) & 0xFF)
    b[1] = byte((v >> 8) & 0xFF)
    b[2] = byte(v & 0xFF)
}
func writeU32(b []byte, v uint32) {
    b[0] = byte((v >> 24) & 0xFF)
    b[1] = byte((v >> 16) & 0xFF)
    b[2] = byte((v >> 8) & 0xFF)
    b[3] = byte(v & 0xFF)
}