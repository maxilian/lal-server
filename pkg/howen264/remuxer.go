package howen264

import (
	"encoding/binary"
)

// AVCRemuxer caches SPS/PPS and produces FLV/AVC tags for H.264 Annex‑B frames.
type AVCRemuxer struct {
	sps           []byte
	pps           []byte
	seqHeaderSent bool
}

// NewAVCRemuxer creates a fresh remuxer.
func NewAVCRemuxer() *AVCRemuxer { return &AVCRemuxer{} }

// RemuxH264 consumes one Annex‑B frame from Howen (possibly containing AUD/SEI/SPS/PPS/IDR/P)
// and returns one or two FLV tags:
//   - [0]: avcC sequence header (if first time we got SPS+PPS)
//   - [1]: normal AVC NALU tag (length‑prefixed NAL units)
//
// If no payload should be output yet (e.g., waiting for SPS/PPS), it can return zero tags.
func (r *AVCRemuxer) RemuxH264(tsMs uint32, frame []byte, keyHint bool) (tags [][]byte, err error) {
	nals := splitAnnexB(frame)
	if len(nals) == 0 {
		return nil, nil
	}

	// update SPS/PPS if present
	for _, n := range nals {
		//fmt.Printf("NAL type=%d len=%d\n", nalType(n), len(n))
		switch nalType(n) {
		case nalSPS:
			// copy
			r.sps = append([]byte(nil), n...)
			//fmt.Println("Cached SPS")
		case nalPPS:
			r.pps = append([]byte(nil), n...)
			//fmt.Println("Cached PPS")
		case nalIDR:
			//fmt.Println("Got IDR frame")
		}
	}

	// If we have both SPS & PPS and haven't sent seq header yet, emit avcC first.
	if !r.seqHeaderSent && len(r.sps) > 0 && len(r.pps) > 0 {
		if avcC := buildAvcC(r.sps, r.pps); avcC != nil {
			tag := makeFlvVideoTag(tsMs, true /* key */, 0 /* seqHdr */, 0, avcC)
			tags = append(tags, tag)
			r.seqHeaderSent = true
		}
	}

	if !r.seqHeaderSent {
		// still not ready (no SPS/PPS yet) → no video payload
		return tags, nil
	}

	// Build AVC NALU payload (exclude SPS/PPS here).
	var avcPayload []byte
	var isKey = keyHint
	for _, n := range nals {
		t := nalType(n)
		if t == nalSPS || t == nalPPS {
			continue
		}
		if t == nalIDR {
			isKey = true
		}
		ln := uint32(len(n))
		var lnBE [4]byte
		binary.BigEndian.PutUint32(lnBE[:], ln)
		avcPayload = append(avcPayload, lnBE[:]...)
		avcPayload = append(avcPayload, n...)
	}
	if len(avcPayload) == 0 {
		return tags, nil
	}

	tag := makeFlvVideoTag(tsMs, isKey, 1 /* NALU */, 0, avcPayload)
	tags = append(tags, tag)
	return tags, nil
}
