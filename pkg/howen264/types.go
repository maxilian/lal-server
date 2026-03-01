package howen264

// Action codes in Howen WS envelope.
const (
    ActionJSON  uint16 = 2    // JSON control message
    ActionMedia uint16 = 1000 // Media packet
)

// FrameType in Howen media payload (first 2 bytes, LE16).
type FrameType uint16

const (
    FrameInvalid FrameType = 0
    FrameI       FrameType = 1 // I-frame
    FrameP       FrameType = 2 // P-frame
    FrameAudio   FrameType = 3 // audio frame (not handled in this package)
)

// NAL unit types (H.264)
const (
    nalNonIDR = 1
    nalIDR    = 5
    nalSEI    = 6
    nalSPS    = 7
    nalPPS    = 8
)